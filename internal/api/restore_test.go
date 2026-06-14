package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTestBackupZip returns a zip containing config.toml (the supplied
// content) and a manifest.json listing it. The zip is materialized in
// memory so tests can submit it via multipart.
func buildTestBackupZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// Always include a manifest that lists whatever we're putting in.
	contents := make([]string, 0, len(entries))
	for name := range entries {
		contents = append(contents, name)
	}
	mf, _ := json.Marshal(map[string]any{
		"created_at": "2026-06-07T20:00:00Z",
		"version":    "1.0",
		"contents":   contents,
	})

	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	w, _ := zw.Create("manifest.json")
	w.Write(mf)

	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

// submitRestore fakes a multipart upload of the supplied zip and invokes
// the handler. Returns the response recorder.
func submitRestore(t *testing.T, h *AdminHandler, zipBytes []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(zipBytes); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/scripts/restore", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.HandleRestorePOST(rr, req)
	return rr
}

func TestHandleRestorePOST_ValidBackup_RestoresConfigAndDB(t *testing.T) {
	dataDir := t.TempDir()
	h := &AdminHandler{DataDir: dataDir}

	configContent := []byte("server:\n  port: 9090\n")
	dbContent := []byte("sqlite-db-payload-test")
	zipBytes := buildTestBackupZip(t, map[string][]byte{
		"config.toml": configContent,
		"vrhub.db":    dbContent,
	})

	rr := submitRestore(t, h, zipBytes)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}

	// Verify config.toml was restored to dataDir with exact content.
	restoredCfg, err := os.ReadFile(filepath.Join(dataDir, "config.toml"))
	if err != nil {
		t.Fatalf("config.toml not restored: %v", err)
	}
	if !bytes.Equal(restoredCfg, configContent) {
		t.Errorf("config.toml mismatch:\n  got:  %q\n  want: %q", restoredCfg, configContent)
	}

	// Verify vrhub.db was restored.
	restoredDB, err := os.ReadFile(filepath.Join(dataDir, "vrhub.db"))
	if err != nil {
		t.Fatalf("vrhub.db not restored: %v", err)
	}
	if !bytes.Equal(restoredDB, dbContent) {
		t.Errorf("vrhub.db mismatch:\n  got:  %q\n  want: %q", restoredDB, dbContent)
	}

	// No tmp files left behind.
	for _, name := range []string{"config.toml.restore.tmp", "vrhub.db.restore.tmp"} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); !os.IsNotExist(err) {
			t.Errorf("tmp file %s should have been renamed away, got err=%v", name, err)
		}
	}
}

func TestHandleRestorePOST_NotAZip_400(t *testing.T) {
	h := &AdminHandler{DataDir: t.TempDir()}
	rr := submitRestore(t, h, []byte("definitely not a zip"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "INVALID_BACKUP") {
		t.Errorf("expected INVALID_BACKUP code, got: %s", rr.Body.String())
	}
}

func TestHandleRestorePOST_PathTraversal_Rejected(t *testing.T) {
	h := &AdminHandler{DataDir: t.TempDir()}

	// Build a zip containing a malicious entry.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../../etc/passwd")
	w.Write([]byte("pwned"))
	w2, _ := zw.Create("manifest.json")
	w2.Write([]byte(`{"contents":["../../etc/passwd"]}`))
	zw.Close()

	rr := submitRestore(t, h, buf.Bytes())
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "INVALID_BACKUP_PATH") {
		t.Errorf("expected INVALID_BACKUP_PATH code, got: %s", rr.Body.String())
	}
}

func TestHandleRestorePOST_EntryNotInAllowlist_Rejected(t *testing.T) {
	h := &AdminHandler{DataDir: t.TempDir()}

	// Build a zip with an allowed entry name that's not whitelisted
	// (e.g. an attempt to overwrite the server binary).
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("vrhub-server.exe")
	w.Write([]byte("malicious"))
	w2, _ := zw.Create("manifest.json")
	w2.Write([]byte(`{"contents":["vrhub-server.exe"]}`))
	zw.Close()

	rr := submitRestore(t, h, buf.Bytes())
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestHandleRestorePOST_MissingManifest_400(t *testing.T) {
	h := &AdminHandler{DataDir: t.TempDir()}

	// Zip with config.toml but no manifest.json.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("config.toml")
	w.Write([]byte("c"))
	zw.Close()

	rr := submitRestore(t, h, buf.Bytes())
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestHandleRestorePOST_ManifestEntryMissing_400(t *testing.T) {
	h := &AdminHandler{DataDir: t.TempDir()}

	// Manifest claims vrhub.db is present, but the zip doesn't include it.
	// We craft a fresh zip with a lying manifest (buildTestBackupZip's
	// manifest is auto-generated from actual entries, so it would never
	// lie).
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("config.toml")
	w.Write([]byte("c"))
	w2, _ := zw.Create("manifest.json")
	w2.Write([]byte(`{"contents":["config.toml","vrhub.db"]}`))
	zw.Close()

	rr := submitRestore(t, h, buf.Bytes())
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestHandleRestorePOST_WrongMethod_405(t *testing.T) {
	h := &AdminHandler{DataDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/restore", nil)
	rr := httptest.NewRecorder()
	h.HandleRestorePOST(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestHandleRestorePOST_MissingFileField_400(t *testing.T) {
	h := &AdminHandler{DataDir: t.TempDir()}

	// Multipart with no 'file' field.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("other", "value")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/scripts/restore", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.HandleRestorePOST(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

// Ensure the readZipEntry helper round-trips correctly (smoke test).
func TestReadZipEntry_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("hello.txt")
	w.Write([]byte("world"))
	zw.Close()

	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	data, err := readZipEntry(zr.File[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world" {
		t.Errorf("got %q, want %q", data, "world")
	}
}

// Sanity: validateRestoreEntryName rejects the cases we care about.
func TestValidateRestoreEntryName(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantError bool
	}{
		{"config.toml ok", "config.toml", false},
		{"vrhub.db ok", "vrhub.db", false},
		{"manifest.json ok", "manifest.json", false},
		{"path separator", "subdir/file", true},
		{"backslash", `subdir\file`, true},
		{"parent ref", "../etc/passwd", true},
		{"absolute", "/etc/passwd", true},
		{"not in allowlist", "vrhub-server.exe", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRestoreEntryName(tc.input)
			if tc.wantError && err == nil {
				t.Errorf("expected error for %q, got nil", tc.input)
			}
			if !tc.wantError && err != nil {
				t.Errorf("expected nil for %q, got %v", tc.input, err)
			}
		})
	}
}

// Use io to satisfy import; remove if test already uses it via other means.
var _ = io.EOF
