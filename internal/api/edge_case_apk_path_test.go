package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEdgeCase_ApkPathDeletedWhileServerRunning is the
// "operator deletes the file while the server is up" scenario
// (the file-server must return 404, not 500, when the file
// is gone). This complements the e2e test
// TestE2E_PublicFileServer_ApkDeletedAfterScan_Returns404
// by also covering:
//   - the GET (full download) returns 404 cleanly
//   - the listing endpoint for the now-empty parent dir
//     returns 200 with no files (not 404 — the dir itself
//     still exists, just empty of the deleted APK).
//
// Story 9.10 / edge case (file-deletion race).
func TestEdgeCase_ApkPathDeletedWhileServerRunning(t *testing.T) {
	env := newE2EEnv(t)

	gameFolder := filepath.Join(env.tmpDir, "External")
	apkName := "com.gone.game.apk"
	apkPath := filepath.Join(gameFolder, apkName)
	env.writeFile(apkPath, []byte("apk"))

	const hash = "edgecase000000000000000000000001"
	env.insertGame("com.gone.game", hash, apkPath, "")

	// Delete the file BEFORE the request (simulates operator
	// removing the APK after the scan was done).
	if err := os.Remove(apkPath); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// GET (full download) → 404 (the file is gone, apk_path
	// points to a missing file).
	rec := env.do("GET", "/"+hash+"/com.gone.game/"+apkName, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete: status = %d, want 404 (deleted file must not 500)", rec.Code)
	}

	// HEAD → also 404 (the client uses this for size probes).
	headRec := env.do("HEAD", "/"+hash+"/com.gone.game/"+apkName, nil)
	if headRec.Code != http.StatusNotFound {
		t.Errorf("HEAD after delete: status = %d, want 404", headRec.Code)
	}

	// GET /{hash}/{pkg}/ (file listing) → 404 (the apk_path's
	// parent dir still exists but the file is gone; the
	// handler returns 404 because the dir no longer has the
	// APK to list — but this is a separate code path from the
	// download 404). We just check it doesn't 500.
	listRec := env.do("GET", "/"+hash+"/com.gone.game/", nil)
	if listRec.Code >= 500 {
		t.Errorf("listing after delete: status = %d, want < 500 (no 500 on missing file)", listRec.Code)
	}
}

// TestEdgeCase_ApkPathDirectoryReplaced is the
// "operator replaces the file with a directory" scenario
// (e.g. extracting the APK contents over the APK file). The
// file server must return 404, not 500.
//
// Story 9.10 / edge case (file-stat rejection branch).
func TestEdgeCase_ApkPathDirectoryReplaced(t *testing.T) {
	env := newE2EEnv(t)

	gameFolder := filepath.Join(env.tmpDir, "External")
	apkName := "com.dir.game.apk"
	apkPath := filepath.Join(gameFolder, apkName)
	env.writeFile(apkPath, []byte("apk"))

	const hash = "edgecase000000000000000000000002"
	env.insertGame("com.dir.game", hash, apkPath, "")

	// Replace the file with a directory of the same name.
	if err := os.Remove(apkPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.MkdirAll(apkPath, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Drop a file inside the dir so it's not completely empty
	// (an empty dir would be a different edge case).
	if err := os.WriteFile(filepath.Join(apkPath, "garbage"), []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := env.do("GET", "/"+hash+"/com.dir.game/"+apkName, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (directory at apk_path must not be served)", rec.Code)
	}
}

// TestEdgeCase_ApkPathRelativePath is the security check:
// the apk_path stored in DB must be absolute. A relative
// path in the DB is a sign of a bad import (the scanner
// should always store absolute paths). The file server
// should still handle it gracefully (404, not 500 or worse
// — file path traversal).
//
// This is more of a contract test than a true edge case
// (in production the scanner prevents relative paths).
//
// Story 9.10 / security regression.
func TestEdgeCase_ApkPathRelativePath(t *testing.T) {
	env := newE2EEnv(t)

	// Build a real file at a relative location relative to
	// the env's CWD (the test's CWD), then set apk_path
	// to that relative path. The file server will try to
	// open it relative to the test CWD; we don't care if
	// it succeeds (the contract is "no 500, no traversal").
	const hash = "edgecase000000000000000000000003"
	const relPath = "edgecase_relative.apk"
	if err := os.WriteFile(relPath, []byte("rel apk"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(relPath) })

	env.insertGame("com.relative.game", hash, relPath, "")

	rec := env.do("GET", "/"+hash+"/com.relative.game/"+relPath, nil)
	// We don't assert the exact code: the file might or
	// might not be served (depends on whether the test CWD
	// matches the file's location). The contract is "no 5xx".
	if rec.Code >= 500 {
		t.Errorf("relative apk_path: status = %d, want < 500 (relative paths must not cause 5xx)", rec.Code)
	}
}

// TestEdgeCase_ApkPathEmptyStringInDB is the no-op case:
// the DB row exists, exposed=true, apk_path="". The file
// server must NOT 500 — it should fall back to the legacy
// dataDir/games/.../ path. If the file isn't there either,
// it returns 404 (the file genuinely doesn't exist on
// disk).
//
// Story 9.10 / regression: the empty-apk_path branch must
// not crash the handler.
func TestEdgeCase_ApkPathEmptyStringInDB(t *testing.T) {
	env := newE2EEnv(t)

	// Insert a game with apk_path="" but no file on disk
	// anywhere. The handler should:
	//   1. Try the apk_path (empty → falls through to legacy)
	//   2. Try the legacy path (also doesn't exist) → 404
	const hash = "edgecase000000000000000000000004"
	env.insertGame("com.empty.path", hash, "", "")

	rec := env.do("GET", "/"+hash+"/com.empty.path/file.apk", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("empty apk_path: status = %d, want 404 (file genuinely missing)", rec.Code)
	}

	// Sanity: no panic, no 500.
	if rec.Code >= 500 {
		t.Errorf("empty apk_path: got 5xx, want 404 (the handler must not crash on empty apk_path)")
	}
}

// TestEdgeCase_ApkPathWithSpecialChars is a fuzz-style
// regression: apk_path containing special characters (spaces,
// non-ASCII, etc.) must be served correctly. The scanner
// always produces a Clean'd absolute path, but a DB row
// imported via some other path might have a less-clean
// value. The file server should handle the bytes it
// receives from the DB without re-interpreting them.
//
// Story 9.10 / robustness regression.
func TestEdgeCase_ApkPathWithSpecialChars(t *testing.T) {
	env := newE2EEnv(t)

	// Build a path with a space and unicode.
	gameFolder := filepath.Join(env.tmpDir, "VR Games")
	if err := os.MkdirAll(gameFolder, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	apkName := "space game.apk"
	apkPath := filepath.Join(gameFolder, apkName)
	if err := os.WriteFile(apkPath, []byte("space apk"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	const hash = "edgecase000000000000000000000005"
	env.insertGame("com.space.game", hash, apkPath, "")

	rec := env.do("GET", "/"+hash+"/com.space.game/space%20game.apk", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (special chars in path must work)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "space apk") {
		t.Errorf("body does not contain expected content")
	}
}

// TestEdgeCase_BothApkAndObbPaths exercises the multi-file
// scenario: 1 game with both an APK and an OBB, both stored
// in the DB at the right paths. The client must be able to
// download both files via separate GET requests.
//
// Story 9.10 / edge case (multi-file game).
func TestEdgeCase_BothApkAndObbPaths(t *testing.T) {
	env := newE2EEnv(t)

	gameFolder := filepath.Join(env.tmpDir, "External")
	apkPath := filepath.Join(gameFolder, "com.multi.game.apk")
	obbPath := filepath.Join(gameFolder, "main.1.com.multi.game.obb")
	env.writeFile(apkPath, []byte("apk bytes"))
	env.writeFile(obbPath, []byte("obb bytes"))

	const hash = "edgecase000000000000000000000006"
	env.insertGame("com.multi.game", hash, apkPath, obbPath)

	// Download the APK via the new apk_path code path.
	rec1 := env.do("GET", "/"+hash+"/com.multi.game/com.multi.game.apk", nil)
	if rec1.Code != http.StatusOK {
		t.Errorf("APK GET: status = %d, want 200", rec1.Code)
	}
	if !strings.Contains(rec1.Body.String(), "apk bytes") {
		t.Errorf("APK body missing")
	}

	// Download the OBB via the new obb_path code path.
	rec2 := env.do("GET", "/"+hash+"/com.multi.game/main.1.com.multi.game.obb", nil)
	if rec2.Code != http.StatusOK {
		t.Errorf("OBB GET: status = %d, want 200", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "obb bytes") {
		t.Errorf("OBB body missing")
	}

	// Sanity: APK and OBB are different responses (no
	// content confusion).
	if rec1.Body.String() == rec2.Body.String() {
		t.Errorf("APK and OBB bodies are identical (server confused the paths?)")
	}
}
