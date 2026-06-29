package api

import (
	"context"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/archive"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// makeGameArchive generates a real split-7z archive under dataDir for a game and
// returns its part basenames. Skips the test if no 7z binary is available.
func makeGameArchive(t *testing.T, dataDir, hash, release, pkg, password string) []string {
	t.Helper()
	src := t.TempDir()
	apk := filepath.Join(src, "Game.apk")
	buf := make([]byte, 250*1024)
	rand.New(rand.NewSource(7)).Read(buf)
	if err := os.WriteFile(apk, buf, 0o644); err != nil {
		t.Fatalf("write apk: %v", err)
	}
	spec := archive.GameArchiveSpec{
		DataDir: dataDir, Hash: hash, ReleaseName: release, PackageName: pkg,
		APKPath: apk, VersionCode: 1, Password: password, SplitSize: "100k",
	}
	if err := archive.GenerateGameArchive(context.Background(), spec); err != nil {
		t.Skipf("cannot generate game archive (no 7z binary?): %v", err)
	}
	parts, err := archive.GameArchiveParts(dataDir, hash, release)
	if err != nil || len(parts) == 0 {
		t.Fatalf("no parts generated: %v", err)
	}
	return parts
}

// TestPackageListing_ShowsArchiveParts asserts that once a split-7z archive
// exists, GET /{hash}/ lists the .7z.* parts (not the raw {packageName}/ subdir).
func TestPackageListing_ShowsArchiveParts(t *testing.T) {
	dataDir := t.TempDir()
	hash := "abc123def456789012345678abcdef00"
	release := "com.test.game"
	pkg := "com.test.game"
	parts := makeGameArchive(t, dataDir, hash, release, pkg, "pw-secret-123")

	game := &types.GameEntry{GameName: "Test Game", PackageName: pkg, ReleaseName: release, Hash: hash}
	deps := fileServerDeps{
		FileDB:     &mockFileServerDB{game: game, packages: []string{pkg}},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: dataDir},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/"+hash+"/", nil)
	servePackageListing(w, r, deps, game)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, p := range parts {
		if !strings.Contains(body, p) {
			t.Errorf("listing missing part %q\nbody:\n%s", p, body)
		}
	}
	// Must NOT advertise the raw package subdir when the archive is present
	// (would double the download for rclone-based clients).
	if strings.Contains(body, "href=\""+pkg+"/\"") {
		t.Errorf("listing should not expose the raw %q/ subdir alongside archive parts", pkg)
	}
}

// TestPackageListing_FallsBackToRaw asserts the raw {packageName}/ listing is
// served when no archive has been generated yet.
func TestPackageListing_FallsBackToRaw(t *testing.T) {
	dataDir := t.TempDir()
	hash := "abc123def456789012345678abcdef00"
	pkg := "com.test.game"
	game := &types.GameEntry{GameName: "Test Game", PackageName: pkg, ReleaseName: pkg, Hash: hash}
	deps := fileServerDeps{
		FileDB:     &mockFileServerDB{game: game, packages: []string{pkg}},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: dataDir},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/"+hash+"/", nil)
	servePackageListing(w, r, deps, game)

	if !strings.Contains(w.Body.String(), "href=\""+pkg+"/\"") {
		t.Errorf("expected raw package subdir link when no archive present\nbody:\n%s", w.Body.String())
	}
}

// TestServeArchivePart_FullAndRange checks a part download (200 + bytes) and a
// Range request (206).
func TestServeArchivePart_FullAndRange(t *testing.T) {
	dataDir := t.TempDir()
	hash := "abc123def456789012345678abcdef00"
	release := "com.test.game"
	pkg := "com.test.game"
	parts := makeGameArchive(t, dataDir, hash, release, pkg, "pw-secret-123")
	first := parts[0]
	if !strings.HasSuffix(first, ".7z.001") {
		t.Fatalf("first part = %q, want suffix .7z.001", first)
	}

	game := &types.GameEntry{GameName: "Test Game", PackageName: pkg, ReleaseName: release, Hash: hash}
	deps := fileServerDeps{
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: dataDir},
	}

	// Full download.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/"+hash+"/"+first, nil)
	serveArchivePart(w, r, deps, game, first)
	if w.Code != http.StatusOK {
		t.Fatalf("full download status = %d, want 200", w.Code)
	}
	if w.Header().Get("Accept-Ranges") != "bytes" {
		t.Error("missing Accept-Ranges: bytes")
	}
	onDisk, _ := os.ReadFile(filepath.Join(archive.GameArchiveDir(dataDir, hash), first))
	if len(onDisk) == 0 || w.Body.Len() != len(onDisk) {
		t.Errorf("served %d bytes, on-disk part is %d bytes", w.Body.Len(), len(onDisk))
	}

	// Range request.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/"+hash+"/"+first, nil)
	r2.Header.Set("Range", "bytes=0-99")
	serveArchivePart(w2, r2, deps, game, first)
	if w2.Code != http.StatusPartialContent {
		t.Errorf("range status = %d, want 206", w2.Code)
	}
	if w2.Header().Get("Content-Length") != "100" {
		t.Errorf("range Content-Length = %q, want 100", w2.Header().Get("Content-Length"))
	}

	// Traversal attempt must 404.
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest("GET", "/x", nil)
	serveArchivePart(w3, r3, deps, game, "..\\..\\evil.7z.001")
	if w3.Code != http.StatusNotFound {
		t.Errorf("traversal status = %d, want 404", w3.Code)
	}
}
