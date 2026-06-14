package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestServeFileDownload_UsesApkPath_FromDB is the AC2 happy path:
// when the DB stores a non-canonical apk_path (anywhere in
// game_folders), the file server serves the file from that path —
// not from the legacy dataDir/games/{hash}/{pkgName}/ layout.
//
// We use realFileReader + real disk files: the APK is created at
// gameFolder1/.../apk (the "scanner found it here" location), and
// the DB record points at that exact path. The legacy path does NOT
// exist; if the serving logic accidentally falls through, the test
// will return 404 and fail.
//
// Story 9.10 T3 / Subtask 3.3.
func TestServeFileDownload_UsesApkPath_FromDB(t *testing.T) {
	tmpDir := t.TempDir()

	// "Game folder" the operator configured in game_folders
	gameFolder := filepath.Join(tmpDir, "ExternalGames", "AkiBonbon")
	if err := os.MkdirAll(gameFolder, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	apkFileName := "com.gcBronze.AkiBonbon.apk"
	apkPath := filepath.Join(gameFolder, apkFileName)
	apkContent := []byte("fake apk content for testing")
	if err := os.WriteFile(apkPath, apkContent, 0644); err != nil {
		t.Fatalf("write apk: %v", err)
	}

	// DB record: the scanner found the APK at gameFolder/apk. Note
	// that hash is the package-name-derived MD5, NOT the file content
	// hash — the file server route looks up by hash.
	const hash = "abcdef0123456789abcdef0123456789"
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "AkiBonbon",
			PackageName: "com.gcBronze.AkiBonbon",
			Hash:        hash,
			ApkPath:     apkPath, // <-- the key field
		},
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/"+hash+"/com.gcBronze.AkiBonbon/"+apkFileName, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (apk_path must be honored, not the legacy layout)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), string(apkContent)) {
		t.Errorf("body does not contain the APK bytes (file at %q was not served)", apkPath)
	}
	if rec.Header().Get("Content-Length") == "" {
		t.Error("missing Content-Length header")
	}
	if rec.Header().Get("Content-Type") != "application/vnd.android.package-archive" {
		t.Errorf("Content-Type = %q, want apk mime", rec.Header().Get("Content-Type"))
	}
}

// TestServeFileDownload_FallsBackToLegacyPath_WhenApkPathEmpty is the
// AC2 regression gate: games that pre-date the 9.10 migration and
// have apk_path="" must still be served from the legacy
// dataDir/games/{hash}/{pkgName}/{fileName} layout — otherwise an
// upgrade that didn't run the startup backfill would break the 3
// existing working games.
//
// Story 9.10 T3 / Subtask 3.3.
func TestServeFileDownload_FallsBackToLegacyPath_WhenApkPathEmpty(t *testing.T) {
	tmpDir := t.TempDir()

	const hash = "abcdef0123456789abcdef0123456789"
	pkgName := "com.legacy.game"
	legacyDir := filepath.Join(tmpDir, "games", hash, pkgName)
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	apkName := "com.legacy.game.apk"
	apkPath := filepath.Join(legacyDir, apkName)
	if err := os.WriteFile(apkPath, []byte("legacy apk"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// DB record: apk_path deliberately empty (legacy game, not yet
	// backfilled).
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Legacy Game",
			PackageName: pkgName,
			Hash:        hash,
			ApkPath:     "", // <-- the key condition
		},
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/"+hash+"/"+pkgName+"/"+apkName, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (legacy games must continue to be served from dataDir/games/...)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "legacy apk") {
		t.Error("body does not contain the legacy APK bytes")
	}
}

// TestServeFileListing_UsesApkPath_Dir is the AC3 happy path: when
// apk_path is set, the file listing is generated from
// filepath.Dir(game.ApkPath) — the directory containing the real
// APK. The legacy dataDir/games/{hash}/{pkgName}/ layout does NOT
// exist on disk in this test; the listing must succeed anyway.
//
// Story 9.10 T3 / Subtask 3.3.
func TestServeFileListing_UsesApkPath_Dir(t *testing.T) {
	tmpDir := t.TempDir()

	gameFolder := filepath.Join(tmpDir, "VR", "AkiBonbon")
	if err := os.MkdirAll(gameFolder, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Drop both the APK and a paired OBB in the same dir, like the
	// scanner would discover them.
	apkPath := filepath.Join(gameFolder, "com.gcBronze.AkiBonbon.apk")
	if err := os.WriteFile(apkPath, []byte("apk"), 0644); err != nil {
		t.Fatalf("write apk: %v", err)
	}
	obbPath := filepath.Join(gameFolder, "main.1.com.gcBronze.AkiBonbon.obb")
	if err := os.WriteFile(obbPath, []byte("obb"), 0644); err != nil {
		t.Fatalf("write obb: %v", err)
	}

	const hash = "abcdef0123456789abcdef0123456789"
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "AkiBonbon",
			PackageName: "com.gcBronze.AkiBonbon",
			Hash:        hash,
			ApkPath:     apkPath,
		},
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/"+hash+"/com.gcBronze.AkiBonbon", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (listing must use the apk_path dir)", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "com.gcBronze.AkiBonbon.apk") {
		t.Error("body should contain the APK filename")
	}
	if !strings.Contains(body, "main.1.com.gcBronze.AkiBonbon.obb") {
		t.Error("body should contain the OBB filename")
	}
}

// TestServeFileDownload_UsesOBBPath_ForOBBFile verifies that the
// OBB download branch (fileName extension == .obb) resolves the
// file from game.OBBPath — not from game.ApkPath. This is the
// "main + patch" multi-OBB design: apk_path points to the APK,
// obb_path points to the first valid OBB found in the same
// directory.
//
// Story 9.10 T3 / Subtask 3.3.
func TestServeFileDownload_UsesOBBPath_ForOBBFile(t *testing.T) {
	tmpDir := t.TempDir()

	gameFolder := filepath.Join(tmpDir, "VR", "MyGame")
	if err := os.MkdirAll(gameFolder, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	apkPath := filepath.Join(gameFolder, "com.example.mygame.apk")
	if err := os.WriteFile(apkPath, []byte("apk"), 0644); err != nil {
		t.Fatalf("write apk: %v", err)
	}
	obbPath := filepath.Join(gameFolder, "main.1.com.example.mygame.obb")
	obbContent := []byte("obb content for testing")
	if err := os.WriteFile(obbPath, obbContent, 0644); err != nil {
		t.Fatalf("write obb: %v", err)
	}

	const hash = "abcdef0123456789abcdef0123456789"
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "MyGame",
			PackageName: "com.example.mygame",
			Hash:        hash,
			ApkPath:     apkPath,
			OBBPath:     obbPath, // <-- the key field
		},
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	obbName := "main.1.com.example.mygame.obb"
	req := httptest.NewRequest("GET", "/"+hash+"/com.example.mygame/"+obbName, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (obb_path must be honored for .obb downloads)", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), string(obbContent)) {
		t.Error("body does not contain the OBB bytes")
	}
}
