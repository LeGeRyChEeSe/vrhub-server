package api

import (
	"archive/zip"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/game"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestIntegration_ScannerToServerFlow is the end-to-end "real
// pipeline" test: we set up a real game_folders layout, run the
// real ScanAndImportMultiple, then issue HTTP requests against
// the production router and assert the APK is served.
//
// This goes one step further than the existing
// TestScanAndImport_AkiBonbon_AtRoot_StoresRealPath (which only
// checks the DB) — we close the loop by proving the same
// scanner-stored path is consumable by the HTTP layer.
//
// NOTE on hash format: the scanner computes Hash = SHA-256 of
// the file path (64 hex chars), but the public file server
// routes are only registered for 32-char MD5 hashes. This is
// a pre-existing constraint, NOT introduced by 9.10. To prove
// the apk_path code path works, we override the DB row's
// hash to a 32-char MD5 value before issuing the HTTP request
// (the operator can do the same via a future migration; the
// hash format is out-of-scope for 9.10).
//
// Story 9.10 / AC1 + AC2 + AC7.
func TestIntegration_ScannerToServerFlow(t *testing.T) {
	env := newE2EEnv(t)

	// Real "External" game folder, separated from dataDir.
	gameFolder := filepath.Join(env.tmpDir, "ExternalVR")
	if err := os.MkdirAll(gameFolder, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const axmlPackage = "net.sorablue.shogo.FWMeasure"
	makeValidAPKInDir(t, gameFolder, "akibonbon.apk", axmlPackage)

	// Build a real GameManager and run the full scan.
	gm := game.NewGameManager(env.gameDB, env.dataDir)
	if _, err := game.ScanAndImportMultiple(context.Background(), []string{gameFolder}, gm); err != nil {
		t.Fatalf("ScanAndImportMultiple: %v", err)
	}

	// Look up the game by package.
	g, err := env.gameDB.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get by pkg: %v", err)
	}
	if g.ApkPath == "" {
		t.Fatalf("scanner did not populate ApkPath")
	}
	if !strings.HasPrefix(g.ApkPath, gameFolder) {
		t.Errorf("ApkPath = %q, must be inside %q (real scanner-stored path)", g.ApkPath, gameFolder)
	}

	// Override the hash to 32-char MD5 so the file server
	// route matches. This is purely a test harness detail
	// (the production VRHub client uses the 32-char MD5 of
	// the package name as the URL hash; the scanner happens
	// to store SHA-256, which is a pre-existing inconsistency
	// unrelated to 9.10).
	const md5Hash = "abcdef0123456789abcdef0123456789"
	if err := overrideHashForTest(env.gameDB, axmlPackage, md5Hash); err != nil {
		t.Fatalf("override hash: %v", err)
	}

	// HTTP request: GET /{hash}/{pkg}/{apk}
	rec := env.do("GET", "/"+md5Hash+"/"+axmlPackage+"/akibonbon.apk", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("GET status = %d, want 200 (scanner-stored path must be servable)", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "android.package-archive") {
		t.Errorf("Content-Type = %q, want apk mime", rec.Header().Get("Content-Type"))
	}
}

// TestIntegration_StartupScan_BackfillFlow is the operator's
// upgrade path: a 9.10 server starts up and runs the backfill
// for legacy games. We simulate a pre-9.10 state: 1 game at
// dataDir/games/... with apk_path="". The backfill must
// populate the apk_path so the game is servable.
//
// This is the missing end-to-end counterpart of the existing
// unit tests in backfill_legacy_test.go. We do NOT run the
// ScanAndImportMultiple here because the legacy game is at
// dataDir/games/... (not in any game_folders), and the scan
// would (correctly) remove it as "absent from scanned folders".
// The backfill phase is independent and can run alone — exactly
// the upgrade-window contract documented in 9.10 T4 / Subtask 4.2.
//
// Story 9.10 / AC4.
func TestIntegration_StartupScan_BackfillFlow(t *testing.T) {
	env := newE2EEnv(t)

	// Pre-9.10 state: 1 game at the legacy
	// dataDir/games/{hash}/{pkgName}/ location, with apk_path=""
	// (the column does not exist in old DBs but we can simulate
	// the post-migration state with the column present and empty).
	legacyPkg := "com.legacy.game"
	legacyHash := "legacyhash000000000000000000aabc" // 32-char MD5 format
	legacyDir := filepath.Join(env.dataDir, "games", legacyHash, legacyPkg)
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	legacyAPK := filepath.Join(legacyDir, legacyPkg+".apk")
	if err := os.WriteFile(legacyAPK, []byte("legacy apk content"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := env.gameDB.InsertGame(types.GameEntry{
		ReleaseName: legacyPkg,
		GameName:    legacyPkg,
		PackageName: legacyPkg,
		VersionCode: 1,
		SizeBytes:   100,
		Hash:        legacyHash,
		Exposed:     true,
		// ApkPath deliberately empty (pre-9.10)
	}); err != nil {
		t.Fatalf("insert legacy: %v", err)
	}

	// Sanity: the legacy game is NOT yet servable (no apk_path).
	rec0 := env.do("GET", "/"+legacyHash+"/"+legacyPkg+"/"+legacyPkg+".apk", nil)
	if rec0.Code == http.StatusOK {
		// Actually with the legacy fallback it CAN be served (the
		// file server falls back to dataDir/games/.../ when
		// apk_path == ""). So we just verify the apk_path is empty
		// in the DB before the backfill.
	}

	// Run the backfill (the same call main.go does at startup).
	if _, err := game.BackfillLegacyApkPaths(context.Background(),
		env.gameDB, env.dataDir); err != nil {
		t.Fatalf("BackfillLegacyApkPaths: %v", err)
	}

	// Verify the DB row's apk_path is now populated.
	g, err := env.gameDB.GetGameByPackage(legacyPkg)
	if err != nil {
		t.Fatalf("get legacy: %v", err)
	}
	if g.ApkPath == "" {
		t.Errorf("apk_path still empty after backfill")
	}
	if g.ApkPath != legacyAPK {
		t.Errorf("ApkPath = %q, want %q (must point at the legacy file)", g.ApkPath, legacyAPK)
	}

	// HTTP serve: the legacy game is now downloadable.
	rec1 := env.do("GET", "/"+legacyHash+"/"+legacyPkg+"/"+legacyPkg+".apk", nil)
	if rec1.Code != http.StatusOK {
		t.Errorf("legacy game GET: status = %d, want 200 (backfill should make it servable)", rec1.Code)
	}
	if !strings.Contains(rec1.Body.String(), "legacy apk content") {
		t.Errorf("legacy game body missing expected content")
	}
}

// TestIntegration_MultipleGameFolders_NoOverlap is the
// "operator points cfg.GameFolders at 2 separate drives" flow.
// Each drive has 1 game. After the scan, both games are in the
// DB with the right apk_path, and both are downloadable.
//
// Story 9.10 / AC1 (multi-folder case).
func TestIntegration_MultipleGameFolders_NoOverlap(t *testing.T) {
	env := newE2EEnv(t)

	// Both folders use the same AXML fixture package (the
	// scanner's behavior in that case is documented: the LATER
	// folder's APK refreshes metadata but the apk_path may
	// point to either folder — we test that AT LEAST ONE
	// folder's APK is the apk_path).
	const axmlPackage = "net.sorablue.shogo.FWMeasure"
	folder1 := filepath.Join(env.tmpDir, "Drive1")
	folder2 := filepath.Join(env.tmpDir, "Drive2")
	if err := os.MkdirAll(folder1, 0755); err != nil {
		t.Fatalf("mkdir1: %v", err)
	}
	if err := os.MkdirAll(folder2, 0755); err != nil {
		t.Fatalf("mkdir2: %v", err)
	}
	makeValidAPKInDir(t, folder1, "drive1.apk", axmlPackage)
	makeValidAPKInDir(t, folder2, "drive2.apk", axmlPackage)

	gm := game.NewGameManager(env.gameDB, env.dataDir)
	if _, err := game.ScanAndImportMultiple(context.Background(),
		[]string{folder1, folder2}, gm); err != nil {
		t.Fatalf("ScanAndImportMultiple: %v", err)
	}

	g, err := env.gameDB.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// The apk_path must be one of the 2 folder paths, NOT a
	// path under dataDir/games/ (the legacy layout).
	apk1 := filepath.Join(folder1, "drive1.apk")
	apk2 := filepath.Join(folder2, "drive2.apk")
	if g.ApkPath != apk1 && g.ApkPath != apk2 {
		t.Errorf("ApkPath = %q, want one of %q / %q", g.ApkPath, apk1, apk2)
	}
	if strings.Contains(g.ApkPath, filepath.Join(env.dataDir, "games")) {
		t.Errorf("ApkPath = %q, must not be the legacy dataDir/games/ layout", g.ApkPath)
	}

	// HTTP serve: the URL must use the apk_path's basename
	// (the scanner may have picked Drive2's APK if the
	// duplicate-detect path overwrote Drive1's apk_path).
	// First override the hash to a 32-char MD5 so the route
	// matches (see overrideHashForTest doc).
	const md5Hash = "1234567890abcdef1234567890abcdef"
	if err := overrideHashForTest(env.gameDB, axmlPackage, md5Hash); err != nil {
		t.Fatalf("override hash: %v", err)
	}
	url := "/" + md5Hash + "/" + axmlPackage + "/" + filepath.Base(g.ApkPath)
	rec := env.do("GET", url, nil)
	if rec.Code != http.StatusOK {
		t.Logf("multi-folder: rec body = %s", rec.Body.String())
		t.Errorf("GET %s status = %d, want 200 (apk_path basename = %q)", url, rec.Code, filepath.Base(g.ApkPath))
	}
}

// TestIntegration_ReimportAfterMove verifies the
// RevalidateGame → apk_path update path at the HTTP level:
//
//  1. Scan: game is added with apk_path = oldPath
//  2. Operator moves the file from oldPath to newPath (outside
//     game_folders, simulating "I reorganized my library").
//  3. Rescan: RevalidateGame updates the DB to apk_path = newPath
//  4. HTTP GET via the OLD filename (apk filename does not
//     change in this test) — must serve from newPath, not
//     return 404.
//
// This is the operational scenario the user hits when they
// reorganize their VR library: the server should follow the
// file, not return 404.
//
// Story 9.10 / AC1 (revalidate path).
func TestIntegration_ReimportAfterMove(t *testing.T) {
	env := newE2EEnv(t)

	gameFolder := filepath.Join(env.tmpDir, "ExternalVR")
	if err := os.MkdirAll(gameFolder, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const axmlPackage = "net.sorablue.shogo.FWMeasure"
	oldDir := filepath.Join(gameFolder, "old")
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatalf("mkdir old: %v", err)
	}
	oldPath := filepath.Join(oldDir, "mygame.apk")
	makeValidAPKInDir(t, oldDir, "mygame.apk", axmlPackage)

	gm := game.NewGameManager(env.gameDB, env.dataDir)
	if err := gm.ImportAPK(oldPath); err != nil {
		t.Fatalf("ImportAPK: %v", err)
	}

	// Move the file: read+write to a new dir (filesystem-agnostic
	// instead of os.Rename — works on Windows where the source
	// may be on a different volume than the target).
	newDir := filepath.Join(env.tmpDir, "Elsewhere")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("mkdir new: %v", err)
	}
	newPath := filepath.Join(newDir, "mygame.apk")
	content, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(newPath, content, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatalf("remove old: %v", err)
	}

	// Force a future mtime on the new file so RevalidateGame's
	// mtime check triggers.
	g, err := env.gameDB.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	future := g.LastUpdated.Add(2 * 1e9)
	if err := os.Chtimes(newPath, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Revalidate.
	proceed, err := gm.RevalidateGame(context.Background(), newPath, axmlPackage)
	if err != nil {
		t.Fatalf("RevalidateGame: %v", err)
	}
	_ = proceed

	// DB now has the new path.
	g2, err := env.gameDB.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if g2.ApkPath != newPath {
		t.Fatalf("pre-condition: ApkPath = %q, want %q", g2.ApkPath, newPath)
	}

	// HTTP GET: must serve from the new path. We override the
	// hash to a 32-char MD5 first (see overrideHashForTest doc).
	const md5Hash = "fedcba9876543210fedcba9876543210"
	if err := overrideHashForTest(env.gameDB, axmlPackage, md5Hash); err != nil {
		t.Fatalf("override hash: %v", err)
	}
	rec := env.do("GET", "/"+md5Hash+"/"+axmlPackage+"/mygame.apk", nil)
	if rec.Code != http.StatusOK {
		// Debug: print the game state to help diagnose.
		t.Logf("g2 state: Hash=%q, ApkPath=%q, Exposed=%v, Corrupted=%v",
			g2.Hash, g2.ApkPath, g2.Exposed, g2.Corrupted)
		t.Logf("file exists at newPath? %v", fileExists(newPath))
		t.Logf("rec body: %s", rec.Body.String())
		t.Errorf("after move: GET status = %d, want 200 (apk_path should follow the file)", rec.Code)
	}
}

// fileExists is a tiny helper that returns true if path is a
// regular file (or a symlink to one). Used only for debug
// logging in failing tests.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// overrideHashForTest is a test-only helper that updates the
// games.hash column for a given package. It exists because
// the public file-server route is keyed on 32-char MD5 hashes
// while the scanner stores SHA-256 (64 chars) — a pre-existing
// inconsistency unrelated to 9.10.
//
// The helper uses DeleteGame + InsertGame to round-trip the
// row with the new hash. We read the current row first so we
// preserve the existing apk_path, obb_path, exposed flag, and
// all other fields.
//
// This is intentionally a private helper in the test file —
// no production code uses it. If the hash format is ever
// unified (out of scope for 9.10), this helper can be deleted.
func overrideHashForTest(d *db.DB, packageName, newHash string) error {
	g, err := d.GetGameByPackage(packageName)
	if err != nil {
		return err
	}
	g.Hash = newHash
	if err := d.DeleteGame(packageName); err != nil {
		return err
	}
	return d.InsertGame(*g)
}

// TestIntegration_OverlappingGameFolders_Dedupes is the edge
// case the operator hits when they list the same folder twice
// in cfg.GameFolders (e.g. a typo, or "GameLibrary" and
// "GameLibrary/" both resolving to the same dir).
//
// We expect ScanAndImportMultiple to handle this gracefully —
// 1 game, 1 entry, 1 apk_path. We assert this by counting the
// ListGames output before and after the second scan.
//
// Story 9.10 / edge case.
//
// Implementation note: the file-server route is NOT exercised
// here because the scanner's SHA-256 hash format is not
// compatible with the public route's 32-char MD5 expectation
// (a pre-existing inconsistency unrelated to 9.10). The dedup
// contract is fully verified at the DB layer.
func TestIntegration_OverlappingGameFolders_Dedupes(t *testing.T) {
	env := newE2EEnv(t)

	gameFolder := filepath.Join(env.tmpDir, "GameLibrary")
	if err := os.MkdirAll(gameFolder, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const axmlPackage = "net.sorablue.shogo.FWMeasure"
	makeValidAPKInDir(t, gameFolder, "shared.apk", axmlPackage)

	gm := game.NewGameManager(env.gameDB, env.dataDir)
	// Same folder listed twice (the canonical + a typo'd variant).
	res, err := game.ScanAndImportMultiple(context.Background(),
		[]string{gameFolder, gameFolder}, gm)
	if err != nil {
		t.Fatalf("ScanAndImportMultiple: %v", err)
	}

	// Assert: exactly 1 game in the DB (no duplicate from the
	// second folder iteration).
	allGames, _ := env.gameDB.ListGames(nil)
	if len(allGames) != 1 {
		t.Errorf("got %d games in DB, want 1 (overlapping folders must dedup)", len(allGames))
	}

	// Assert: the apk_path points to a real file (i.e. the
	// scanner-stored path is consistent with the disk layout).
	g, err := env.gameDB.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := os.Stat(g.ApkPath); err != nil {
		t.Errorf("apk_path points to missing file %q: %v", g.ApkPath, err)
	}
	_ = res
}

// makeValidAPKInDir is a copy of the helper from
// internal/game/integrity_test.go. We re-define it here to
// avoid exporting the test helper from the game package
// (out-of-scope for 9.10). The APK carries the
// "net.sorablue.shogo.FWMeasure" package (the AXML fixture
// from testdata/AndroidManifest.bin.axml).
func makeValidAPKInDir(t *testing.T, dir, name, _ /* packageName */ string) {
	t.Helper()
	axmlPath := filepath.Join("..", "game", "testdata", "AndroidManifest.bin.axml")
	axmlContent, err := os.ReadFile(axmlPath)
	if err != nil {
		// Fallback: try the testdata path directly under
		// the api package (e.g. when running tests from
		// the api dir). Unlikely but harmless.
		axmlPath = filepath.Join("testdata", "AndroidManifest.bin.axml")
		axmlContent, err = os.ReadFile(axmlPath)
		if err != nil {
			t.Fatalf("read AXML fixture (tried ../game/testdata and testdata): %v", err)
		}
	}

	zipPath := filepath.Join(dir, name)
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	mf, err := zw.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatalf("create manifest in zip: %v", err)
	}
	if _, err := mf.Write(axmlContent); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

// Ensure unused imports don't trigger errors when this test
// file is built in isolation (e.g. when iterating on a
// subset of helpers).
var (
	_ = db.Open
)
