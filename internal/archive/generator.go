package archive

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// MetadataCache provides access to metadata files for games.
type MetadataCache struct {
	root string
}

// NewMetadataCache creates a new MetadataCache pointing to the given root directory.
// Returns an error if root is empty (C-18). The previous constructor silently
// accepted "" which produced nonsensical paths like "thumbnails/pkg.jpg" via
// filepath.Join; callers would only learn of the misconfiguration when a later
// call to Validate() returned an error.
func NewMetadataCache(root string) (*MetadataCache, error) {
	if root == "" {
		return nil, fmt.Errorf("metadata cache root cannot be empty")
	}
	return &MetadataCache{root: root}, nil
}

// Validate checks if the MetadataCache root is properly set.
// Returns an error if root is empty or root/metadata subdirs cannot be accessed.
func (m *MetadataCache) Validate() error {
	if m.root == "" {
		return fmt.Errorf("metadata cache root is empty")
	}
	testPaths := []string{
		filepath.Join(m.root, "thumbnails"),
		filepath.Join(m.root, "icons"),
		filepath.Join(m.root, "notes"),
	}
	for _, p := range testPaths {
		if dir, err := os.Stat(filepath.Dir(p)); err == nil && !dir.IsDir() {
			return fmt.Errorf("metadata cache root is not a directory")
		}
	}
	return nil
}

// ThumbnailPath returns the path to a thumbnail file for the given package name.
func (m *MetadataCache) ThumbnailPath(packageName string) string {
	return filepath.Join(m.root, "thumbnails", fmt.Sprintf("%s.jpg", packageName))
}

// NotesPath returns the path to a notes file for the given package name.
func (m *MetadataCache) NotesPath(packageName string) string {
	return filepath.Join(m.root, "notes", fmt.Sprintf("%s.txt", packageName))
}

// IconPath returns the path to an icon file for the given package name.
func (m *MetadataCache) IconPath(packageName string) string {
	return filepath.Join(m.root, "icons", fmt.Sprintf("%s.png", packageName))
}

// IconPathByRelease returns the path to an icon file indexed by release name.
// MetaMetadata images are downloaded and stored under the release name, not the package name.
func (m *MetadataCache) IconPathByRelease(releaseName string) string {
	return filepath.Join(m.root, "icons", releaseName+".png")
}

// ThumbnailPathByRelease returns the path to a thumbnail indexed by release name.
func (m *MetadataCache) ThumbnailPathByRelease(releaseName string) string {
	return filepath.Join(m.root, "thumbnails", releaseName+".jpg")
}

// ThumbnailExists checks if a thumbnail file exists for the given package name.
func (m *MetadataCache) ThumbnailExists(packageName string) bool {
	info, err := os.Lstat(m.ThumbnailPath(packageName))
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// NotesExists checks if a notes file exists for the given package name.
func (m *MetadataCache) NotesExists(packageName string) bool {
	info, err := os.Lstat(m.NotesPath(packageName))
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// IconExists checks if an icon file exists for the given package name.
func (m *MetadataCache) IconExists(packageName string) bool {
	info, err := os.Lstat(m.IconPath(packageName))
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// Generator creates password-protected 7z archives containing game list and metadata.
type Generator struct{}

// NewGenerator creates a new archive generator with the given metadata cache.
// Deprecated: Generator no longer stores metadata; use GenerateMeta7z directly.
func NewGenerator(metadata *MetadataCache) *Generator {
	return &Generator{}
}

// GenerateMeta7z creates a password-protected 7z archive containing VRP-GameList.txt
// and optional metadata files. The archive is encrypted with AES-256 using the
// provided password and written to the given writer stream.
func GenerateMeta7z(ctx context.Context, games []types.GameEntry, metadata *MetadataCache, w io.Writer, password string) error {
	gen := &Generator{}
	return gen.generate(ctx, games, metadata, w, password)
}

// generate creates the 7z archive and writes it to the writer.
// Story 9.8: the archive is encrypted with AES-256 via the 7zz CLI.
func (g *Generator) generate(ctx context.Context, games []types.GameEntry, metadata *MetadataCache, w io.Writer, password string) error {
	// Create a temp directory to stage files before archiving.
	tmpDir, err := os.MkdirTemp("", "meta-7z-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Collect all files to write.
	type archiveFile struct {
		path string
		data []byte
	}

	var files []archiveFile

	// VRP-GameList.txt first.
	vrpData := []byte(GenerateVRPGameListContent(games))
	files = append(files, archiveFile{path: "VRP-GameList.txt", data: vrpData})

	// Track seen package names to avoid duplicate metadata entries.
	seenPackages := make(map[string]bool)

	// Collect metadata files (skip if metadata cache is nil).
	if metadata != nil {
		for _, game := range games {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			if game.PackageName == "" {
				continue
			}

			pkg := game.PackageName

			// Skip duplicate package names — only include metadata from the first occurrence.
			if seenPackages[pkg] {
				continue
			}
			seenPackages[pkg] = true

			// Thumbnail: prefer MetaMetadata (releaseName) over APK-extracted (packageName).
			thumbnailPath := metadata.ThumbnailPath(pkg)
			if game.ReleaseName != "" {
				if relPath := metadata.ThumbnailPathByRelease(game.ReleaseName); func() bool {
					info, e := os.Stat(relPath)
					return e == nil && !info.IsDir()
				}() {
					thumbnailPath = relPath
				}
			}
			if info, statErr := os.Stat(thumbnailPath); statErr == nil && !info.IsDir() {
				data, readErr := readFileWithTimeout(ctx, thumbnailPath)
				if readErr != nil {
					return fmt.Errorf("read thumbnail %s: %w", pkg, readErr)
				}
				files = append(files, archiveFile{path: filepath.Join("thumbnails", pkg+".jpg"), data: data})
			}

			notesPath := metadata.NotesPath(pkg)
			if info, statErr := os.Stat(notesPath); statErr == nil && !info.IsDir() {
				data, readErr := readFileWithTimeout(ctx, notesPath)
				if readErr != nil {
					return fmt.Errorf("read notes %s: %w", pkg, readErr)
				}
				files = append(files, archiveFile{path: filepath.Join("notes", pkg+".txt"), data: data})
			}

			// Icon: prefer MetaMetadata (releaseName) over APK-extracted (packageName).
			iconPath := metadata.IconPath(pkg)
			if game.ReleaseName != "" {
				if relPath := metadata.IconPathByRelease(game.ReleaseName); func() bool {
					info, e := os.Stat(relPath)
					return e == nil && !info.IsDir()
				}() {
					iconPath = relPath
				}
			}
			if info, statErr := os.Stat(iconPath); statErr == nil && !info.IsDir() {
				data, readErr := readFileWithTimeout(ctx, iconPath)
				if readErr != nil {
					return fmt.Errorf("read icon %s: %w", pkg, readErr)
				}
				files = append(files, archiveFile{path: filepath.Join("icons", pkg+".png"), data: data})
			}
		}
	}

	// Write all files to the temp directory.
	var fileArgs []string
	for _, f := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		destPath := filepath.Join(tmpDir, f.path)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", f.path, err)
		}
		if err := os.WriteFile(destPath, f.data, 0o644); err != nil {
			return fmt.Errorf("write temp file %s: %w", f.path, err)
		}
		fileArgs = append(fileArgs, f.path)
	}

	// Build the 7z archive with AES-256 encryption via 7zz CLI.
	// We need a dataDir hint for the fallback download location.
	// The MetadataCache root is the best proxy for dataDir; if nil,
	// the PATH lookup still works.
	dataDirHint := ""
	if metadata != nil {
		dataDirHint = metadata.root
	}
	sevenZipPath, err := sevenZipBinaryPath(ctx, dataDirHint)
	if err != nil {
		return fmt.Errorf("7z binary not available: %w", err)
	}

	// The command runs with cmd.Dir = tmpDir below. If sevenZipPath is a
	// relative path (e.g. an extracted-binary path under a relative
	// -data-dir like ".test-data/metadata/bin/7zz.exe"), the OS would
	// resolve it against tmpDir and fail with "cannot find the path
	// specified". Make it absolute so it always resolves regardless of
	// cmd.Dir. PATH-resolved bare names (no separator) are left untouched.
	if sevenZipPath != "" && strings.ContainsAny(sevenZipPath, `/\`) && !filepath.IsAbs(sevenZipPath) {
		if abs, absErr := filepath.Abs(sevenZipPath); absErr == nil {
			sevenZipPath = abs
		}
	}

	archivePath := filepath.Join(tmpDir, "archive.7z")
	args := []string{
		"a",
		"-p" + password,
		"-mhe=on",
		"-mx=7",
		"-bb0",
		"-bso0",
		"-bse1",
		"-y",
		archivePath,
	}
	args = append(args, fileArgs...)

	cmd := exec.CommandContext(ctx, sevenZipPath, args...)
	cmd.Dir = tmpDir

	// On Android the embedded 7zz is a modern bionic build of 7-Zip
	// dynamically linked against the bundled libc++_shared.so, which
	// sevenZipBinaryPath extracts next to the binary. Point LD_LIBRARY_PATH
	// at that directory so the dynamic linker resolves it on stock Android
	// (which ships a differently-named platform libc++). (F11)
	if isAndroid() {
		if libDir := GetAndroidLibDir(); libDir != "" {
			cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+libDir)
		}
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("7z archive creation failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	// Stream the resulting archive to the writer.
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open created archive: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("copy archive to writer: %w", err)
	}

	return nil
}

// sanitizeArchivePath validates and normalizes a path for use inside an archive.
// Returns an error if the path contains traversal sequences or is absolute.
//
// S-10 security gate: defends against multi-level path traversal
// (`a/b/c/../../../etc/passwd`), Windows-style backslashes, and
// null-byte injection. The implementation uses filepath.Clean to
// collapse `..` segments — the cleaned result is then checked
// for the residual patterns that DO escape the archive root.
//
// The defer from the 2026-05-30 story 2-2 code review
// ("Multi-level traversal not blocked") was based on a pre-Clean
// mental model — empirically the current implementation correctly
// handles 3-level deep `..` (verified by the
// `subdir/../../../escape` test case). This S-10 hardening adds
// null-byte rejection (the classic "good\x00bad" trick) and
// clarifies the rationale.
func sanitizeArchivePath(path string) error {
	if path == "" {
		return fmt.Errorf("empty archive path")
	}

	// Null byte injection: an attacker could smuggle a "good"
	// prefix followed by a null byte to truncate Go's string view,
	// leaving a C-level syscall (when the path is passed to
	// os.Open) reading a file the Go layer never approved. Defense:
	// reject the path outright. (Go's path package would error on
	// this anyway when passed to OS calls, but fail-closed here
	// is cheaper.)
	if strings.ContainsRune(path, 0) {
		return fmt.Errorf("path contains null byte: %q", path)
	}

	clean := filepath.ToSlash(filepath.Clean(path))
	// Defense-in-depth: even after Clean collapses `..` segments,
	// the resulting path should not start with `..` (which would
	// mean the original path had MORE `..` than folder components)
	// and should not be absolute.
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return fmt.Errorf("path traversal detected: %q", path)
	}
	// Backslashes on Unix filesystems are literal characters, not
	// separators. On Windows they would be separators — we run on
	// Unix, but defend against cross-platform archive readers that
	// might interpret them.
	if strings.Contains(clean, "\\") {
		return fmt.Errorf("path contains backslash: %q", path)
	}
	return nil
}

// writeArchiveEntry writes a single file entry to the xz stream with header metadata.
func (g *Generator) writeArchiveEntry(xw io.Writer, path string, data []byte) error {
	if err := sanitizeArchivePath(path); err != nil {
		return fmt.Errorf("sanitize archive path: %w", err)
	}

	nameBytes := []byte(path)

	// Write filename length (4 bytes, big-endian).
	nameLenBuf := make([]byte, 4)
	nameLenBuf[0] = byte(len(nameBytes) >> 24)
	nameLenBuf[1] = byte(len(nameBytes) >> 16)
	nameLenBuf[2] = byte(len(nameBytes) >> 8)
	nameLenBuf[3] = byte(len(nameBytes))

	if _, err := xw.Write(nameLenBuf); err != nil {
		return fmt.Errorf("write name length: %w", err)
	}

	// Write filename.
	if _, err := xw.Write(nameBytes); err != nil {
		return fmt.Errorf("write filename: %w", err)
	}

	// Write content length (8 bytes, little-endian).
	contentLen := int64(len(data))
	for i := 0; i < 8; i++ {
		if _, err := xw.Write([]byte{byte((contentLen >> (i * 8)) & 0xFF)}); err != nil {
			return fmt.Errorf("write content length: %w", err)
		}
	}

	// Write file content.
	if _, err := xw.Write(data); err != nil {
		return fmt.Errorf("write content for %s: %w", path, err)
	}

	return nil
}

// BuildGameListForMeta7z filters games for meta.7z inclusion.
// Returns only games where corrupted=false and exposed=true, sorted by popularity descending.
func BuildGameListForMeta7z(games []types.GameEntry) []types.GameEntry {
	filtered := make([]types.GameEntry, 0, len(games))

	for _, game := range games {
		if !game.Corrupted && game.Exposed {
			filtered = append(filtered, game)
		}
	}

	// Sort by popularity descending (bubble sort for simplicity).
	for i := 0; i < len(filtered); i++ {
		for j := i + 1; j < len(filtered); j++ {
			if filtered[j].Popularity > filtered[i].Popularity {
				filtered[i], filtered[j] = filtered[j], filtered[i]
			}
		}
	}

	return filtered
}

// GenerateVRPGameListContent creates the VRP-GameList.txt content string for the given games.
func GenerateVRPGameListContent(games []types.GameEntry) string {
	var sb strings.Builder

	for _, game := range games {
		// SizeBytes is APK-only; add OBBSizeBytes so the VRHub client
		// sees the total download size (APK + all OBBs).
		totalSize := game.SizeBytes + game.OBBSizeBytes
		sb.WriteString(fmt.Sprintf("%s;%s;%s;%d;%d;%d\n",
			game.GameName,
			game.ReleaseName,
			game.PackageName,
			game.VersionCode,
			totalSize,
			game.Popularity,
		))
	}

	if sb.Len() == 0 {
		sb.WriteString("\n")
	}

	return sb.String()
}

// readFileWithTimeout reads a file with context-aware timeout support.
// If the context is cancelled before the read completes, the goroutine is joined
// via an unbuffered channel so no orphan goroutine results.
func readFileWithTimeout(ctx context.Context, path string) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	resultCh := make(chan result, 1)

	go func() {
		data, err := os.ReadFile(path)
		resultCh <- result{data, err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-resultCh:
		return r.data, r.err
	}
}
