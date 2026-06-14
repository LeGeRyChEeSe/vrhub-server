package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

func TestServeFileDownload_FullDownload_Returns200(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}

	fileContent := []byte("hello world - this is a test file for download")
	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/app-release.apk", fileContent, 0644)

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: "com.test.game",
				Hash:        "abc123def456789012345678abcdef00",
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: "com.test.game",
		Hash:        "abc123def456789012345678abcdef00",
	}, "com.test.game", "app-release.apk")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	contentLength := w.Header().Get("Content-Length")
	if contentLength == "" {
		t.Error("missing Content-Length header")
	} else if contentLength != fmt.Sprintf("%d", len(fileContent)) {
		t.Errorf("Content-Length = %q, want %q", contentLength, fmt.Sprintf("%d", len(fileContent)))
	}

	if w.Header().Get("Accept-Ranges") != "bytes" {
		t.Error("missing Accept-Ranges: bytes header")
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/vnd.android.package-archive" {
		t.Errorf("Content-Type = %q, want %q", contentType, "application/vnd.android.package-archive")
	}

	body := w.Body.Bytes()
	if string(body) != string(fileContent) {
		t.Errorf("body = %q, want %q", body, fileContent)
	}
}

func TestServeFileDownload_PartialRange_Returns206(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}

	fileContent := make([]byte, 2048)
	for i := range fileContent {
		fileContent[i] = byte(i % 256)
	}
	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/app-release.apk", fileContent, 0644)

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: "com.test.game",
				Hash:        "abc123def456789012345678abcdef00",
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Range", "bytes=0-1023")

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: "com.test.game",
		Hash:        "abc123def456789012345678abcdef00",
	}, "com.test.game", "app-release.apk")

	if w.Code != http.StatusPartialContent {
		t.Errorf("status = %d, want %d (AC2)", w.Code, http.StatusPartialContent)
	}

	contentRange := w.Header().Get("Content-Range")
	if contentRange == "" {
		t.Error("missing Content-Range header")
	} else if !strings.HasPrefix(contentRange, "bytes 0-") {
		t.Errorf("Content-Range = %q, expected bytes 0-N/2048", contentRange)
	}

	contentLength := w.Header().Get("Content-Length")
	if contentLength != "1024" {
		t.Errorf("Content-Length = %q, want 1024", contentLength)
	}
}

func TestServeFileDownload_NonexistentFile_Returns404(t *testing.T) {
	tmpDir := t.TempDir()
	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: "com.test.game",
				Hash:        "abc123def456789012345678abcdef00",
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: "com.test.game",
		Hash:        "abc123def456789012345678abcdef00",
	}, "com.test.game", "nonexistent.apk")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (AC3)", w.Code, http.StatusNotFound)
	}
}

// TestServeFileDownload_RangeBeyondFileSize_Returns416 (C-11):
// A range request whose start is beyond fileSize must return 416 with
// Content-Range: */{size} per RFC 7233 §4.4. The previous behavior
// (AC5) silently "saved" the request by serving from byte 0 with
// HTTP 206, which violated the spec.
func TestServeFileDownload_RangeBeyondFileSize_Returns416(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}

	fileContent := []byte("short")
	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/app-release.apk", fileContent, 0644)

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: "com.test.game",
				Hash:        "abc123def456789012345678abcdef00",
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Range", "bytes=500-")

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: "com.test.game",
		Hash:        "abc123def456789012345678abcdef00",
	}, "com.test.game", "app-release.apk")

	if w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("status = %d, want %d (C-11: RFC 7233 §4.4)", w.Code, http.StatusRequestedRangeNotSatisfiable)
	}

	contentRange := w.Header().Get("Content-Range")
	expectedPrefix := "bytes */5"
	if contentRange != expectedPrefix {
		t.Errorf("Content-Range = %q, want %q", contentRange, expectedPrefix)
	}
}

// TestServeFileDownload_InvalidRange_Returns400: a malformed Range
// header (e.g. "bytes=invalid") is an HTTP 400 Bad Request, not 416.
// 416 is for well-formed ranges that are out-of-bounds (RFC 7233 §4.4).
func TestServeFileDownload_InvalidRange_Returns400(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}

	fileContent := []byte("test file content")
	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/app-release.apk", fileContent, 0644)

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: "com.test.game",
				Hash:        "abc123def456789012345678abcdef00",
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Range", "bytes=invalid")

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: "com.test.game",
		Hash:        "abc123def456789012345678abcdef00",
	}, "com.test.game", "app-release.apk")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (malformed Range is 400, not 416)", w.Code, http.StatusBadRequest)
	}
}

func TestServeFileDownload_PathTraversalInFilename_Returns404(t *testing.T) {
	tmpDir := t.TempDir()
	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: "com.test.game",
				Hash:        "abc123def456789012345678abcdef00",
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: "com.test.game",
		Hash:        "abc123def456789012345678abcdef00",
	}, "com.test.game", "../../etc/passwd")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d for path traversal attempt", w.Code, http.StatusNotFound)
	}
}

func TestServeFileDownload_ZeroByteFile_Returns416(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}

	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/app-release.apk", []byte{}, 0644)

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: "com.test.game",
				Hash:        "abc123def456789012345678abcdef00",
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Range", "bytes=0-")

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: "com.test.game",
		Hash:        "abc123def456789012345678abcdef00",
	}, "com.test.game", "app-release.apk")

	if w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("status = %d, want %d for zero-byte file with range request", w.Code, http.StatusRequestedRangeNotSatisfiable)
	}

	contentRange := w.Header().Get("Content-Range")
	if !strings.HasPrefix(contentRange, "bytes */") {
		t.Errorf("Content-Range = %q, expected bytes */0", contentRange)
	}
}

func TestServeFileDownload_ZeroByteFile_NoRange_Returns200Empty(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}

	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/app-release.apk", []byte{}, 0644)

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: "com.test.game",
				Hash:        "abc123def456789012345678abcdef00",
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: "com.test.game",
		Hash:        "abc123def456789012345678abcdef00",
	}, "com.test.game", "app-release.apk")

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d for zero-byte file without range request", w.Code, http.StatusOK)
	}

	contentLength := w.Header().Get("Content-Length")
	if contentLength != "0" {
		t.Errorf("Content-Length = %q, want 0", contentLength)
	}

	body := w.Body.String()
	if body != "" {
		t.Errorf("body = %q, want empty string for zero-byte file", body)
	}
}

func TestServeFileDownload_RangeError_ClosesFile(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}

	fileContent := make([]byte, 1024)
	for i := range fileContent {
		fileContent[i] = byte(i % 256)
	}
	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/app-release.apk", fileContent, 0644)

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: "com.test.game",
				Hash:        "abc123def456789012345678abcdef00",
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Range", "bytes=0-511")

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: "com.test.game",
		Hash:        "abc123def456789012345678abcdef00",
	}, "com.test.game", "app-release.apk")

	if w.Code != http.StatusPartialContent {
		t.Errorf("status = %d, want %d", w.Code, http.StatusPartialContent)
	}

	contentLength := w.Header().Get("Content-Length")
	if contentLength != "512" {
		t.Errorf("Content-Length = %q, want 512", contentLength)
	}
}

// TestParseRangeHeader_ZeroByteFile: a range request against a 0-byte
// file is well-formed (valid=true) but unsatisfiable (RFC 7233 §4.4).
// The caller returns 416, not 400.
func TestParseRangeHeader_ZeroByteFile(t *testing.T) {
	fileSize := int64(0)
	_, _, valid, unsatisfiable := parseRangeHeader("bytes=0-", fileSize)
	if !valid {
		t.Error("expected valid=true (well-formed range, even against empty file)")
	}
	if !unsatisfiable {
		t.Error("expected unsatisfiable=true for 0-byte file (C-11 / RFC 7233 §4.4)")
	}
}
