package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	chi "github.com/go-chi/chi/v5"
)

// TestRegression_LegacyGame_AllThreeServable simulates the
// pre-9.10 production state: the 3 games that have been
// working since 9.4 are at dataDir/games/{hash}/{pkgName}/
// with apk_path="" (the new column doesn't exist in old DBs,
// but the file server still serves them via the legacy
// fallback). After upgrading to 9.10, all 3 must continue
// to be downloadable.
//
// This is the AC2 "regression gate" — the fix must NOT break
// the 3 existing working games.
//
// Story 9.10 / AC2 + AC3.
func TestRegression_LegacyGame_AllThreeServable(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// The 3 legacy games (pre-9.10 names — fictional packages
	// matching the 9.10 session log).
	games := []struct {
		pkg      string
		hash     string // 32-char MD5
		apkBytes []byte
	}{
		{
			pkg:      "com.markspace.afvirtualgirlfriend",
			hash:     "af000000000000000000000000000a01",
			apkBytes: []byte("AF Virtual Girlfriend APK bytes for legacy e2e test"),
		},
		{
			pkg:      "com.innerspacevr.fisherman",
			hash:     "fs000000000000000000000000000a02",
			apkBytes: []byte("A Fisherman's Tale APK bytes for legacy e2e test"),
		},
		{
			pkg:      "com.superhotteam.superhotvr",
			hash:     "sh000000000000000000000000000a03",
			apkBytes: []byte("SUPERHOT VR APK bytes for legacy e2e test"),
		},
	}

	// Lay out the files at dataDir/games/{hash}/{pkg}/{pkg}.apk
	// (the canonical legacy location) and build the DB rows
	// with apk_path="" (the pre-9.10 state).
	db := newRegressionLegacyDB(t, dataDir, games)

	// Build a router with the same wiring as production.
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))
	cfg := &types.Config{DataDir: dataDir, Admin: types.AdminConfig{ArchivePassword: "reg"}}
	mwRouter := chi.NewRouter()
	MountPublicRoutes(mwRouter, modeVal, db, cfg)

	for _, g := range games {
		t.Run(g.pkg, func(t *testing.T) {
			// Verify the file is at the legacy location.
			legacyPath := filepath.Join(dataDir, "games", g.hash, g.pkg, g.pkg+".apk")
			if _, err := os.Stat(legacyPath); err != nil {
				t.Fatalf("setup: legacy file missing at %q: %v", legacyPath, err)
			}

			// HTTP serve via the legacy fallback. The handler
			// must look at dataDir/games/.../ because the DB
			// has apk_path="".
			req := httptest.NewRequest("GET",
				"/"+g.hash+"/"+g.pkg+"/"+g.pkg+".apk", nil)
			rec := httptest.NewRecorder()
			mwRouter.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (legacy game must remain servable via fallback)", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), string(g.apkBytes)) {
				t.Errorf("body does not contain expected APK bytes (legacy fallback broken?)")
			}
			if cl := rec.Header().Get("Content-Length"); cl != fmt.Sprintf("%d", len(g.apkBytes)) {
				t.Errorf("Content-Length = %q, want %d", cl, len(g.apkBytes))
			}
		})
	}
}

// TestRegression_LegacyGame_FileListingIsHTML verifies the
// HTML file listing at /{hash}/{pkgName}/ still works for
// legacy games. The listing must show the APK filename (and
// any OBB), and the path resolution must use the legacy
// fallback (dataDir/games/.../), not the apk_path column.
//
// Story 9.10 / AC3.
func TestRegression_LegacyGame_FileListingIsHTML(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const pkg = "com.legacy.listing"
	const hash = "legacy00listing0000000000000aabb"
	legacyDir := filepath.Join(dataDir, "games", hash, pkg)
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, pkg+".apk"), []byte("apk"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, pkg+".obb"), []byte("obb"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	db := newRegressionLegacyDB(t, dataDir, []struct {
		pkg      string
		hash     string
		apkBytes []byte
	}{
		{pkg: pkg, hash: hash, apkBytes: []byte("apk")},
	})

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))
	cfg := &types.Config{DataDir: dataDir, Admin: types.AdminConfig{ArchivePassword: "reg"}}
	mwRouter := chi.NewRouter()
	MountPublicRoutes(mwRouter, modeVal, db, cfg)

	req := httptest.NewRequest("GET", "/"+hash+"/"+pkg, nil)
	rec := httptest.NewRecorder()
	mwRouter.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (legacy listing must work)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, pkg+".apk") {
		t.Errorf("listing missing apk filename: %s", body)
	}
	if !strings.Contains(body, pkg+".obb") {
		t.Errorf("listing missing obb filename: %s", body)
	}
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Errorf("listing missing DOCTYPE: %s", body)
	}
}

// TestRegression_LegacyGame_RangeRequestWorks verifies that
// the Range support (resumable download) continues to work
// for legacy games. The Android DownloadManager relies on
// this for large APK downloads.
//
// Story 9.10 / regression: AC2 must preserve Range support.
func TestRegression_LegacyGame_RangeRequestWorks(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const pkg = "com.legacy.range"
	const hash = "legacy00range000000000000000aabb"
	legacyDir := filepath.Join(dataDir, "games", hash, pkg)
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 4 KB of predictable content (so we can verify the slice).
	content := make([]byte, 4096)
	for i := range content {
		content[i] = byte(i % 256)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, pkg+".apk"), content, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	db := newRegressionLegacyDB(t, dataDir, []struct {
		pkg      string
		hash     string
		apkBytes []byte
	}{
		{pkg: pkg, hash: hash, apkBytes: content},
	})

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))
	cfg := &types.Config{DataDir: dataDir, Admin: types.AdminConfig{ArchivePassword: "reg"}}
	mwRouter := chi.NewRouter()
	MountPublicRoutes(mwRouter, modeVal, db, cfg)

	req := httptest.NewRequest("GET", "/"+hash+"/"+pkg+"/"+pkg+".apk", nil)
	req.Header.Set("Range", "bytes=0-1023")
	rec := httptest.NewRecorder()
	mwRouter.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Errorf("status = %d, want 206 (Range support must work for legacy games)", rec.Code)
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 0-1023/4096" {
		t.Errorf("Content-Range = %q, want bytes 0-1023/4096", cr)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "1024" {
		t.Errorf("Content-Length = %q, want 1024", cl)
	}
}

// TestRegression_MixedLegacyAndNewGames covers a partial
// migration: 1 legacy game (apk_path="") and 1 new game
// (apk_path set). Both must be servable in the same
// router.
//
// Story 9.10 / regression: the legacy fallback and the
// new apk_path code path must coexist in the same server.
func TestRegression_MixedLegacyAndNewGames(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// 1 legacy game at the canonical location.
	const legacyPkg = "com.legacy.mixed"
	const legacyHash = "legacymixed00000000000000000aabb"
	legacyDir := filepath.Join(dataDir, "games", legacyHash, legacyPkg)
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, legacyPkg+".apk"),
		[]byte("legacy apk content"), 0644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	// 1 new game in an external folder.
	externalFolder := filepath.Join(tmpDir, "ExternalVR")
	if err := os.MkdirAll(externalFolder, 0755); err != nil {
		t.Fatalf("mkdir external: %v", err)
	}
	const newPkg = "com.new.mixed"
	const newHash = "newmixed00000000000000000000aabb"
	newAPK := filepath.Join(externalFolder, newPkg+".apk")
	if err := os.WriteFile(newAPK, []byte("new apk content"), 0644); err != nil {
		t.Fatalf("write new: %v", err)
	}

	// Build a real DB with both games.
	gameDB := newRegressionLegacyDB(t, dataDir, []struct {
		pkg      string
		hash     string
		apkBytes []byte
	}{
		{pkg: legacyPkg, hash: legacyHash, apkBytes: []byte("legacy apk content")},
	})
	if err := gameDB.InsertGame(types.GameEntry{
		ReleaseName: newPkg,
		GameName:    newPkg,
		PackageName: newPkg,
		VersionCode: 1,
		SizeBytes:   100,
		Hash:        newHash,
		Exposed:     true,
		ApkPath:     newAPK,
	}); err != nil {
		t.Fatalf("insert new: %v", err)
	}

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))
	cfg := &types.Config{DataDir: dataDir, Admin: types.AdminConfig{ArchivePassword: "reg"}}
	mwRouter := chi.NewRouter()
	MountPublicRoutes(mwRouter, modeVal, gameDB, cfg)

	// Legacy game: served via fallback.
	rec1 := testRequest(t, mwRouter, "GET",
		"/"+legacyHash+"/"+legacyPkg+"/"+legacyPkg+".apk", nil)
	if rec1.Code != http.StatusOK {
		t.Errorf("legacy mixed: status = %d, want 200", rec1.Code)
	}
	if !strings.Contains(rec1.Body.String(), "legacy apk content") {
		t.Errorf("legacy mixed: body missing legacy content")
	}

	// New game: served via apk_path.
	rec2 := testRequest(t, mwRouter, "GET",
		"/"+newHash+"/"+newPkg+"/"+newPkg+".apk", nil)
	if rec2.Code != http.StatusOK {
		t.Errorf("new mixed: status = %d, want 200", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "new apk content") {
		t.Errorf("new mixed: body missing new content")
	}
}

// newRegressionLegacyDB is a helper that creates a *db.DB
// with the given legacy games (apk_path=""). It lays out
// the files at the canonical dataDir/games/{hash}/{pkg}/
// location and inserts the rows.
//
// We use a fresh DB (not the e2e env) so this test file
// can run independently of the rest of the test suite.
func newRegressionLegacyDB(t *testing.T, dataDir string, games []struct {
	pkg      string
	hash     string // 32-char MD5
	apkBytes []byte
}) *db.DB {
	t.Helper()
	dbPath := filepath.Join(dataDir, "test.db")
	gameDB, err := openTestDB(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = gameDB.Close() })

	for _, g := range games {
		// Create the canonical file at the legacy location.
		dir := filepath.Join(dataDir, "games", g.hash, g.pkg)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, g.pkg+".apk"), g.apkBytes, 0644); err != nil {
			t.Fatalf("write %s: %v", g.pkg, err)
		}
		// Insert the row with apk_path="" (pre-9.10 state).
		if err := gameDB.InsertGame(types.GameEntry{
			ReleaseName: g.pkg,
			GameName:    g.pkg,
			PackageName: g.pkg,
			VersionCode: 1,
			SizeBytes:   int64(len(g.apkBytes)),
			Hash:        g.hash,
			Exposed:     true,
			// ApkPath deliberately empty (pre-9.10).
		}); err != nil {
			t.Fatalf("insert %s: %v", g.pkg, err)
		}
	}

	return gameDB
}

// testRequest is a tiny convenience for issuing requests to
// a chi router and returning the recorder. Centralized here
// to keep the regression test file compact.
func testRequest(t *testing.T, h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// openTestDB opens a *db.DB at the given path and returns it
// typed as the minimal interface used by the file-server
// route. We use a small interface (not *db.DB directly) so
// the helper signature is decoupled from the production type
// and can be reused by future tests with different fixtures.
func openTestDB(path string) (*db.DB, error) {
	return db.Open(path)
}
