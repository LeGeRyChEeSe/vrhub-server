package api

import (
	"context"
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
	chimw "github.com/go-chi/chi/v5/middleware"
)

// e2eTestEnv is a reusable test harness for HTTP-level e2e tests of
// the public file-server routes after Story 9.10. It stands up:
//   - a temp dataDir with the v9.10 schema (via db.Open + Migrate),
//   - a real *db.DB wired as GameListProvider + FileServerDB,
//   - a real chi.Mux with all 3 production file-server routes
//     (mirrors setupFileServerHandler in this package),
//   - helpers to populate the DB with GameEntry rows and create
//     APK/OBB files on disk at any arbitrary location.
//
// The env intentionally avoids httptest.NewServer (a fully
// listening socket) — ServeHTTP on a httptest.Recorder exercises
// the same chi router code path without the socket overhead.
// HEAD support is provided via chi.middleware.GetHead, mirroring
// the production MountPublicRoutes wiring.
type e2eTestEnv struct {
	t        *testing.T
	tmpDir   string
	dataDir  string
	gameDB   *db.DB
	router   http.Handler
	modeVal  *atomic.Value
	mwRouter *chi.Mux
	cfg      *types.Config
}

// Cfg returns the *types.Config used by the production handler.
// Tests that need to call fileServerHandlerWithDeps directly
// (bypassing the router) can read this.
func (e *e2eTestEnv) Cfg() *types.Config { return e.cfg }

// newE2EEnv constructs the env. dataDir is created at
// <tmpDir>/data and the sqlite DB at <tmpDir>/data/vrhub.db.
// cfg is mutated to point at the new dataDir (so callers can read
// it back for the served-file path).
func newE2EEnv(t *testing.T) *e2eTestEnv {
	t.Helper()
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("mkdir dataDir: %v", err)
	}
	dbPath := filepath.Join(dataDir, "vrhub.db")
	gameDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = gameDB.Close() })

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	cfg := &types.Config{DataDir: dataDir, Admin: types.AdminConfig{ArchivePassword: "e2e-test-pw"}}

	mwRouter := chi.NewRouter()
	mwRouter.Use(chimw.GetHead)
	MountPublicRoutes(mwRouter, modeVal, gameDB, cfg)

	return &e2eTestEnv{
		t:        t,
		tmpDir:   tmpDir,
		dataDir:  dataDir,
		gameDB:   gameDB,
		router:   mwRouter,
		modeVal:  modeVal,
		mwRouter: mwRouter,
		cfg:      cfg,
	}
}

// insertGame inserts a game entry into the DB. pkg/hash are
// required; the rest have reasonable defaults.
func (e *e2eTestEnv) insertGame(pkg, hash, apkPath, obbPath string) {
	e.t.Helper()
	if err := e.gameDB.InsertGame(types.GameEntry{
		ReleaseName: pkg,
		GameName:    pkg,
		PackageName: pkg,
		VersionCode: 1,
		SizeBytes:   100,
		Hash:        hash,
		Exposed:     true,
		ApkPath:     apkPath,
		OBBPath:     obbPath,
	}); err != nil {
		e.t.Fatalf("insert %s: %v", pkg, err)
	}
}

// writeFile is a convenience helper to create a file at an
// arbitrary path with the given content, creating parent dirs.
func (e *e2eTestEnv) writeFile(path string, content []byte) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		e.t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		e.t.Fatalf("write %s: %v", path, err)
	}
}

// do issues an HTTP request through the production router and
// returns the recorder.
func (e *e2eTestEnv) do(method, path string, headers map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// TestE2E_PublicFileServer_FullFlow_AkiBonbon reproduces the
// full client flow on a real chi router (not a stubbed handler):
// the VRHub client does 4 sequential requests —
//  1. HEAD /{hash}/{file}.apk     → 200 + Content-Length (size probe)
//  2. GET  /{hash}/              → 200 + HTML package listing
//  3. GET  /{hash}/{pkg}/        → 200 + HTML file listing
//  4. GET  /{hash}/{pkg}/{file}  → 200 + APK body
//
// The setup uses a real *db.DB and a real disk layout matching the
// live 2026-06-12 bug: an APK at the root of "ExternalGames",
// NOT under dataDir/games/. After the fix, all 4 requests
// succeed; before the fix, #1 and #4 returned 404.
//
// Story 9.10 / AC2 + AC3 + AC7.
func TestE2E_PublicFileServer_FullFlow_AkiBonbon(t *testing.T) {
	env := newE2EEnv(t)

	// Disk layout: APK at the root of an "external" game folder
	// (mirrors the live D:\Documents\Jeux\VR\Test\ situation).
	gameFolder := filepath.Join(env.tmpDir, "ExternalGames")
	apkName := "com.gcBronze.AkiBonbon.apk"
	apkPath := filepath.Join(gameFolder, apkName)
	apkContent := []byte("AKI-BONBON-APK-BYTES-FOR-E2E-TEST")
	env.writeFile(apkPath, apkContent)

	// Hash used in the URL: matches the package-name-derived MD5
	// convention. Any 32-char hex value is fine for routing.
	const hash = "ab0c1b6edeadbeef000000000000abcd"
	env.insertGame("com.gcBronze.AkiBonbon", hash, apkPath, "")

	// 1. HEAD /{hash}/{pkg}/{apk} → 200 + Content-Length
	headRec := env.do("HEAD", "/"+hash+"/com.gcBronze.AkiBonbon/"+apkName, nil)
	if headRec.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want 200", headRec.Code)
	}
	if cl := headRec.Header().Get("Content-Length"); cl != fmt.Sprintf("%d", len(apkContent)) {
		t.Errorf("HEAD Content-Length = %q, want %d", cl, len(apkContent))
	}
	if ct := headRec.Header().Get("Content-Type"); ct != "application/vnd.android.package-archive" {
		t.Errorf("HEAD Content-Type = %q, want apk mime", ct)
	}

	// 2. GET /{hash}/ → 200 + HTML listing
	pkgRec := env.do("GET", "/"+hash+"/", nil)
	if pkgRec.Code != http.StatusOK {
		t.Errorf("GET /{hash}/ status = %d, want 200", pkgRec.Code)
	}
	if !strings.Contains(pkgRec.Body.String(), "com.gcBronze.AkiBonbon/") {
		t.Errorf("package listing missing package link; body = %q", pkgRec.Body.String())
	}

	// 3. GET /{hash}/{pkg}/ → 200 + HTML file listing
	listRec := env.do("GET", "/"+hash+"/com.gcBronze.AkiBonbon/", nil)
	if listRec.Code != http.StatusOK {
		t.Errorf("GET /{hash}/{pkg}/ status = %d, want 200", listRec.Code)
	}
	if !strings.Contains(listRec.Body.String(), apkName) {
		t.Errorf("file listing missing APK filename; body = %q", listRec.Body.String())
	}

	// 4. GET /{hash}/{pkg}/{apk} → 200 + APK body
	getRec := env.do("GET", "/"+hash+"/com.gcBronze.AkiBonbon/"+apkName, nil)
	if getRec.Code != http.StatusOK {
		t.Errorf("GET /{hash}/{pkg}/{apk} status = %d, want 200", getRec.Code)
	}
	if !strings.Contains(getRec.Body.String(), string(apkContent)) {
		t.Errorf("GET body does not contain APK bytes")
	}
	if ar := getRec.Header().Get("Accept-Ranges"); ar != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", ar)
	}
}

// TestE2E_PublicFileServer_ApkDeletedAfterScan_Returns404 covers
// the deletion path: the DB row says apk_path=X, but X is gone
// from disk. The client probes with HEAD first; the server must
// return 404 cleanly (not 500, not the legacy path). This is the
// scenario an operator hits when they delete an APK from
// game_folders between startup scans.
//
// Story 9.10 / AC6.
func TestE2E_PublicFileServer_ApkDeletedAfterScan_Returns404(t *testing.T) {
	env := newE2EEnv(t)

	gameFolder := filepath.Join(env.tmpDir, "ExternalGames")
	apkName := "com.test.deleted.apk"
	apkPath := filepath.Join(gameFolder, apkName)
	env.writeFile(apkPath, []byte("will be deleted"))

	const hash = "deadbeef000000000000000000000001"
	env.insertGame("com.test.deleted", hash, apkPath, "")

	// Delete the file BEFORE issuing the request (simulates the
	// operator removing the APK from game_folders).
	if err := os.Remove(apkPath); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// HEAD probe → must be 404 (the client uses this to detect
	// "taille error" / "file gone").
	headRec := env.do("HEAD", "/"+hash+"/com.test.deleted/"+apkName, nil)
	if headRec.Code != http.StatusNotFound {
		t.Errorf("HEAD after delete: status = %d, want 404 (AC6)", headRec.Code)
	}

	// GET also returns 404.
	getRec := env.do("GET", "/"+hash+"/com.test.deleted/"+apkName, nil)
	if getRec.Code != http.StatusNotFound {
		t.Errorf("GET after delete: status = %d, want 404", getRec.Code)
	}
}

// TestE2E_PublicFileServer_ApkFileReplacedByDirectory_Returns404
// covers an edge case the scanner can produce if the operator
// replaces an APK file with a directory of the same name
// (e.g. a partial extraction). The file-server must NOT
// recurse into the directory — it must return 404.
//
// Story 9.10 / edge case (file-stat rejection branch in
// serveFileDownload).
func TestE2E_PublicFileServer_ApkFileReplacedByDirectory_Returns404(t *testing.T) {
	env := newE2EEnv(t)

	gameFolder := filepath.Join(env.tmpDir, "ExternalGames")
	apkName := "com.test.replaced.apk"
	apkPath := filepath.Join(gameFolder, apkName)

	// Create a directory at the location where the APK should be.
	if err := os.MkdirAll(apkPath, 0755); err != nil {
		t.Fatalf("mkdir-as-apk: %v", err)
	}
	// Also drop a file in the directory so it isn't empty (otherwise
	// the directory could legitimately not exist).
	if err := os.WriteFile(filepath.Join(apkPath, "garbage"), []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	const hash = "deadbeef000000000000000000000002"
	env.insertGame("com.test.replaced", hash, apkPath, "")

	rec := env.do("GET", "/"+hash+"/com.test.replaced/"+apkName, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (directory at apk_path must not be served)", rec.Code)
	}
}

// TestE2E_PublicFileServer_RangeRequest_ApkPath is the
// resumable-download contract test on the 9.10 code path: a
// client (Android DownloadManager) requests "bytes=0-99" against
// a 9.10-stored APK. The server must return 206 + Content-Range.
//
// Story 9.10 / AC2 (Range support preserved).
func TestE2E_PublicFileServer_RangeRequest_ApkPath(t *testing.T) {
	env := newE2EEnv(t)

	gameFolder := filepath.Join(env.tmpDir, "ExternalGames")
	apkName := "com.test.range.apk"
	apkPath := filepath.Join(gameFolder, apkName)
	// 1024 bytes of predictable content (0,1,2,...,255,0,1,...)
	content := make([]byte, 1024)
	for i := range content {
		content[i] = byte(i % 256)
	}
	env.writeFile(apkPath, content)

	const hash = "deadbeef000000000000000000000003"
	env.insertGame("com.test.range", hash, apkPath, "")

	rec := env.do("GET", "/"+hash+"/com.test.range/"+apkName,
		map[string]string{"Range": "bytes=0-99"})

	if rec.Code != http.StatusPartialContent {
		t.Errorf("status = %d, want 206", rec.Code)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "100" {
		t.Errorf("Content-Length = %q, want 100", cl)
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 0-99/1024" {
		t.Errorf("Content-Range = %q, want bytes 0-99/1024", cr)
	}
	if !strings.Contains(rec.Body.String(), string(content[:100])) {
		t.Errorf("body does not contain first 100 bytes of APK")
	}
}

// TestE2E_PublicFileServer_ContentDispositionApk is a wire-format
// regression test: the file server must emit a Content-Disposition
// header that includes the actual APK filename. The Android
// DownloadManager uses this header to name the saved file; a
// missing or wrong filename causes the download to land at
// "download" with no extension, which breaks install.
//
// Story 9.10 / wire-format regression (no fallback should
// overwrite the apk_path filename).
func TestE2E_PublicFileServer_ContentDispositionApk(t *testing.T) {
	env := newE2EEnv(t)

	gameFolder := filepath.Join(env.tmpDir, "ExternalGames")
	apkName := "com.example.wire.apk"
	apkPath := filepath.Join(gameFolder, apkName)
	env.writeFile(apkPath, []byte("apk"))

	const hash = "deadbeef000000000000000000000004"
	env.insertGame("com.example.wire", hash, apkPath, "")

	rec := env.do("GET", "/"+hash+"/com.example.wire/"+apkName, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, apkName) {
		t.Errorf("Content-Disposition = %q, must include the APK filename %q", cd, apkName)
	}
	if !strings.Contains(strings.ToLower(cd), "attachment") {
		t.Errorf("Content-Disposition = %q, should be an attachment (Android DownloadManager parses this)", cd)
	}
}

// TestE2E_PublicFileServer_PathTraversalInPackageName_Returns404
// is the security regression: the wildcard route
// "/{hash}/*" exposes the package name segment to the client.
// A request with ".." in the package must NOT serve files from
// outside the game dir.
//
// Story 9.10 / security regression (fileServerHandlerWithDeps
// already guards against this; we re-test with the real router).
func TestE2E_PublicFileServer_PathTraversalInPackageName_Returns404(t *testing.T) {
	env := newE2EEnv(t)
	const hash = "deadbeef000000000000000000000005"
	env.insertGame("com.test.game", hash, "/tmp/dummy.apk", "")

	rec := env.do("GET", "/"+hash+"/..%2F..%2Fetc%2Fpasswd", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("path traversal via URL-encoded slashes: status = %d, want 404", rec.Code)
	}

	// Plain ".." in the path segment (no URL encoding) is also 404.
	rec2 := env.do("GET", "/"+hash+"/../etc/passwd", nil)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("path traversal: status = %d, want 404", rec2.Code)
	}
}

// TestE2E_PublicFileServer_ObservesModeChange is a smoke test
// that the public router honors the atomic mode value. The
// fix for 9.10 must not have accidentally removed the mode
// check on the file-server routes — otherwise setup mode
// would leak data.
//
// Story 9.10 / regression: route registration must still be
// behind the setup-mode guard.
func TestE2E_PublicFileServer_ObservesModeChange(t *testing.T) {
	env := newE2EEnv(t)

	gameFolder := filepath.Join(env.tmpDir, "ExternalGames")
	apkName := "com.test.modeswitch.apk"
	apkPath := filepath.Join(gameFolder, apkName)
	env.writeFile(apkPath, []byte("apk"))

	const hash = "deadbeef000000000000000000000006"
	env.insertGame("com.test.modeswitch", hash, apkPath, "")

	// In normal mode, the file is served.
	rec := env.do("GET", "/"+hash+"/com.test.modeswitch/"+apkName, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("normal mode: status = %d, want 200", rec.Code)
	}

	// Flip to setup mode → file listing should be 503/redirect.
	// The file-server route is gated by the setup-mode middleware
	// (same as /meta.7z). After the mode change, the request
	// must NOT serve the file.
	env.modeVal.Store(string(types.ModeSetup))
	rec2 := env.do("GET", "/"+hash+"/com.test.modeswitch/"+apkName, nil)
	if rec2.Code == http.StatusOK {
		t.Errorf("setup mode: status = 200, want != 200 (file must not be served in setup mode)")
	}
}

// TestE2E_PublicFileServer_HashNotFound_Returns404 is the "bad
// hash" branch: the client requests an unknown hash. The
// server must return 404 (not 200 with empty body, not 500).
//
// Story 9.10 / regression: this branch exercises the
// FileDB.GetGameByHash path, which is now also used by the
// apk_path code path.
func TestE2E_PublicFileServer_HashNotFound_Returns404(t *testing.T) {
	env := newE2EEnv(t)

	// No game inserted.
	rec := env.do("GET", "/00000000000000000000000000000000/com.test.unknown/file.apk", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown hash: status = %d, want 404", rec.Code)
	}
}

// ensure context import is used (compile-time hint when the
// test file is built without all the helpers wired in).
var _ = context.Background
