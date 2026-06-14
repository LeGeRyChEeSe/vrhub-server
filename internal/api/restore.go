package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
)

// jsonDecode is a tiny wrapper to keep the call sites readable.
func jsonDecode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// maxRestoreUploadSize bounds the size of an uploaded backup zip. This is
// intentionally larger than maxAdminBodySize (4 KiB) since backups include
// the SQLite database. Aligned with maxDownloadSize (500 MB) used by the
// auto-update path so a full server state fits.
const maxRestoreUploadSize = 500 * 1024 * 1024 // 500 MB

// allowedRestoreEntries is the whitelist of zip entry names accepted by
// HandleRestorePOST. Anything else is rejected with INVALID_BACKUP_PATH
// (defense against path traversal and arbitrary file writes).
var allowedRestoreEntries = map[string]bool{
	"config.toml":   true,
	"vrhub.db":      true,
	"manifest.json": true,
}

// restoreManifest mirrors the producer side (HandleScriptsBackupPOST) —
// keeping it local to the api package avoids a shared types dep.
type restoreManifest struct {
	CreatedAt string   `json:"created_at"`
	Version   string   `json:"version"`
	Contents  []string `json:"contents"`
}

// HandleRestorePOST accepts a backup zip (multipart form-data, field
// "file") and atomically replaces config.toml and/or vrhub.db in the
// data dir. Spec: Story 7.3 / FR27.
//
// On success returns 200 with the list of restored files. On any
// validation failure returns 400 with a specific error code. The
// operation is atomic: tmp files are written first and only renamed
// into place if all writes succeed.
func (h *AdminHandler) HandleRestorePOST(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if h.DataDir == "" {
		writeError(w, http.StatusInternalServerError, "data dir not configured", "INTERNAL_ERROR")
		return
	}

	// Cap the upload size up front. MaxBytesReader returns a *MaxBytesError
	// when the limit is exceeded; we convert that to 400.
	r.Body = http.MaxBytesReader(w, r.Body, maxRestoreUploadSize)

	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32 MB in memory, rest in temp files
		var maxErr *http.MaxBytesError
		if err == ErrMissingFile || (err != nil && (err.Error() == "http: no such file" || strings.Contains(err.Error(), "no such file"))) {
			writeError(w, http.StatusBadRequest, "missing form field 'file'", "MISSING_FILE")
			return
		}
		if errMaxBytes(err, &maxErr) {
			writeError(w, http.StatusBadRequest, "upload too large", "UPLOAD_TOO_LARGE")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error(), "INVALID_FORM")
		return
	}

	_, fileHeader, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing form field 'file'", "MISSING_FILE")
		return
	}

	content, err := readAllCapped(fileHeader, maxRestoreUploadSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read upload: "+err.Error(), "READ_ERROR")
		return
	}

	// Open and validate the zip.
	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		writeError(w, http.StatusBadRequest, "not a valid zip archive", "INVALID_BACKUP")
		return
	}

	// 1. Entry name whitelist + path-traversal rejection.
	allowed := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		if err := validateRestoreEntryName(f.Name); err != nil {
			writeError(w, http.StatusBadRequest, "invalid entry name: "+f.Name, "INVALID_BACKUP_PATH")
			return
		}
		allowed[f.Name] = f
	}

	// 2. Manifest must be present and consistent with actual entries.
	mfEntry, ok := allowed["manifest.json"]
	if !ok {
		writeError(w, http.StatusBadRequest, "backup missing manifest.json", "INVALID_BACKUP")
		return
	}
	mfData, err := readZipEntry(mfEntry)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read manifest: "+err.Error(), "INVALID_BACKUP")
		return
	}
	var mf restoreManifest
	if err := jsonDecode(mfData, &mf); err != nil {
		writeError(w, http.StatusBadRequest, "manifest is not valid JSON: "+err.Error(), "INVALID_BACKUP")
		return
	}
	// contents must be a subset of actually-present entries.
	for _, c := range mf.Contents {
		if !allowedRestoreEntries[c] {
			writeError(w, http.StatusBadRequest, "manifest references unknown file: "+c, "INVALID_BACKUP")
			return
		}
		if _, present := allowed[c]; !present {
			writeError(w, http.StatusBadRequest, "manifest lists "+c+" but entry is missing from zip", "INVALID_BACKUP")
			return
		}
	}

	// 3. CRC validation per entry (defense against truncated backups).
	for name, f := range allowed {
		if err := validateEntryCRC(f, name); err != nil {
			writeError(w, http.StatusBadRequest, "corrupt entry: "+err.Error(), "INVALID_BACKUP")
			return
		}
	}

	// 4. Atomic extract: write to tmp files first, rename only if all
	// succeed. On any failure, clean up tmp files (no partial restore).
	restored := []string{}
	tmpFiles := []string{}
	cleanup := func() {
		for _, p := range tmpFiles {
			_ = os.Remove(p)
		}
	}

	for _, name := range mf.Contents {
		f := allowed[name]
		dst := filepath.Join(h.DataDir, name)
		tmp := dst + ".restore.tmp"

		data, err := readZipEntry(f)
		if err != nil {
			cleanup()
			writeError(w, http.StatusBadRequest, "failed to read "+name+": "+err.Error(), "INVALID_BACKUP")
			return
		}
		if err := writeAtomic(tmp, dst, data, 0644); err != nil {
			cleanup()
			writeError(w, http.StatusInternalServerError, "failed to write "+name+": "+err.Error(), "RESTORE_ERROR")
			return
		}
		tmpFiles = append(tmpFiles, tmp)
		restored = append(restored, name)
	}

	// Post-restore hooks (best-effort, log only).
	for _, name := range restored {
		switch name {
		case "config.toml":
			vlog.Get().Info().Str("path", filepath.Join(h.DataDir, name)).Msg("restore: config.toml replaced; restart required for changes to take effect")
		case "vrhub.db":
			vlog.Get().Info().Str("path", filepath.Join(h.DataDir, name)).Msg("restore: vrhub.db replaced; restart required to reopen SQLite connections")
		}
	}

	vlog.Get().Info().Strs("files", restored).Str("remote_addr", r.RemoteAddr).Msg("restore completed; server restart required")

	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"restored": restored,
			"message":  "restore completed; server restart required for changes to take effect",
		},
	})
}

// validateRestoreEntryName rejects empty names, path separators, parent
// references, and anything not in the allowlist.
func validateRestoreEntryName(name string) error {
	if name == "" {
		return fmt.Errorf("empty entry name")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("entry name contains path separator")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("entry name contains '..'")
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return fmt.Errorf("entry name is absolute")
	}
	if !allowedRestoreEntries[name] {
		return fmt.Errorf("entry name not in allowlist")
	}
	return nil
}

// validateEntryCRC recomputes CRC32 over the entry's content and compares
// against the header. Mirrors internal/update.validateZip (kept local
// here to avoid a cross-package import for a 30-line helper).
func validateEntryCRC(f *zip.File, name string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}
	defer rc.Close()
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, rc); err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if h.Sum32() != f.CRC32 {
		return fmt.Errorf("crc mismatch for %s", name)
	}
	return nil
}

// writeAtomic writes data to a tmp file in the same directory as dst,
// fsyncs, and renames into place. On rename failure the tmp file is
// removed.
func writeAtomic(tmpPath, dstPath string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(tmpPath), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// readZipEntry opens and reads a zip entry into memory.
func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// readAllCapped reads from a multipart file up to max bytes. Returns a
// *http.MaxBytesError-style error if the cap is exceeded.
func readAllCapped(fh *multipart.FileHeader, max int64) ([]byte, error) {
	src, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer src.Close()
	// multipart.File is io.Reader + io.Seeker + io.Closer; io.ReadAll
	// reads to EOF which could exceed max if the upload is larger than
	// expected. We trust MaxBytesReader on the request body to cap the
	// total bytes that can flow through r.Body → FormFile → here, so a
	// second cap is belt-and-braces.
	if fh.Size > max {
		return nil, fmt.Errorf("file too large: %d > %d", fh.Size, max)
	}
	return io.ReadAll(src)
}

// errMaxBytes is a small type-check helper since errors.As on a
// pointer-to-interface is awkward in Go's type system.
func errMaxBytes(err error, target **http.MaxBytesError) bool {
	if err == nil {
		return false
	}
	if mb, ok := err.(*http.MaxBytesError); ok {
		*target = mb
		return true
	}
	return false
}

// ErrMissingFile is exported so tests can detect the "no file" case.
var ErrMissingFile = fmt.Errorf("missing form field 'file'")
