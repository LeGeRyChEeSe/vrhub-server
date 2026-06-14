package game

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
)

// ctxBackground returns a fresh background context for tests that
// call RevalidateGame (which takes a context.Context for cancellation).
func ctxBackground() context.Context {
	return context.Background()
}

// TestImportAPK_StoresApkPath is the AC1 happy path: when ImportAPK
// stores a new game, the games.apk_path column is populated with the
// absolute path of the APK file as the scanner found it (no staging
// copy to dataDir/games/.../).
//
// We use a real (minimal) AXML fixture (the makeValidAPKWithPackage
// helper from integrity_test.go) so ImportAPK reaches the "valid APK"
// branch and the metadata extraction succeeds.
//
// Story 9.10 T2 / Subtask 2.3.
func TestImportAPK_StoresApkPath(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.Mkdir(dataDir, 0755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	gm := NewGameManager(d, dataDir)

	// Create a valid APK in a sub-directory of the "data" dir,
	// simulating a scanner finding it at the configured path.
	const axmlPackage = "net.sorablue.shogo.FWMeasure"
	apkPath := filepath.Join(dataDir, "MySubFolder", "mygame.apk")
	if err := os.MkdirAll(filepath.Dir(apkPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	makeValidAPKWithPackage(t, filepath.Dir(apkPath), "mygame.apk", axmlPackage)

	if err := gm.ImportAPK(apkPath); err != nil {
		t.Fatalf("ImportAPK: %v", err)
	}

	game, err := d.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if game.ApkPath != apkPath {
		t.Errorf("ApkPath = %q, want %q (the scanner should store the absolute path it found)", game.ApkPath, apkPath)
	}
}

// TestImportAPK_CorruptedAPK_StoresApkPath is the AC1 negative path:
// even when the APK is corrupted, the scanner still records the
// absolute path so the file server can attempt to serve it (and
// return 404 cleanly if the file is gone).
//
// Story 9.10 T2 / Subtask 2.3.
func TestImportAPK_CorruptedAPK_StoresApkPath(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.Mkdir(dataDir, 0755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	gm := NewGameManager(d, dataDir)

	corruptedAPK := filepath.Join(dataDir, "com.corrupt.apk")
	if err := os.WriteFile(corruptedAPK, []byte("not a zip"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := gm.ImportAPK(corruptedAPK); err != nil {
		t.Fatalf("ImportAPK: %v", err)
	}

	game, err := d.GetGameByPackage("com.corrupt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if game.ApkPath != corruptedAPK {
		t.Errorf("ApkPath = %q, want %q (corrupted APK should still record the path)", game.ApkPath, corruptedAPK)
	}
	if !game.Corrupted {
		t.Error("expected game to be marked as corrupted")
	}
}

// TestImportAPK_FindsOBBInSameDir is the AC1 OBB-pairing path: when a
// valid OBB file is in the same directory as the APK, the scanner
// records the OBB's absolute path on the game entry so the file
// server can serve it directly.
//
// We use the lightweight "obfuscation blob" OBB layout (the validator
// only checks the first 4 bytes signature, see integrity_test.go for
// details). The pair is detected by package name + version code.
//
// Story 9.10 T2 / Subtask 2.3.
func TestImportAPK_FindsOBBInSameDir(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.Mkdir(dataDir, 0755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	gm := NewGameManager(d, dataDir)

	const axmlPackage = "net.sorablue.shogo.FWMeasure"
	const versionCode = 42

	// Create the APK in a sub-folder of data (the "scanner finds it
	// anywhere" scenario).
	apkSubDir := filepath.Join(dataDir, "SomeFolder")
	if err := os.MkdirAll(apkSubDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	apkPath := filepath.Join(apkSubDir, "mygame.apk")
	makeValidAPKWithPackage(t, apkSubDir, "mygame.apk", axmlPackage)
	// makeValidAPKWithPackage always sets versionCode=1. To match the
	// OBB naming convention main.<vc>.<package>.obb, use versionCode=1.
	const actualVersionCode = 1

	// Create a valid OBB with the matching package name and version code.
	obbName := "main." + strconv.Itoa(actualVersionCode) + "." + axmlPackage + ".obb"
	obbPath := filepath.Join(apkSubDir, obbName)
	// 4-byte signature "OBB\x00" is the minimum valid OBB prefix
	// per the validator; the validator only checks the first 4 bytes.
	// We write a 4 KiB blob so the file is non-trivially sized.
	obbContent := make([]byte, 4096)
	copy(obbContent, []byte("OBB\x00"))
	if err := os.WriteFile(obbPath, obbContent, 0644); err != nil {
		t.Fatalf("write obb: %v", err)
	}

	_ = versionCode // silence unused warning for clarity
	if err := gm.ImportAPK(apkPath); err != nil {
		t.Fatalf("ImportAPK: %v", err)
	}

	game, err := d.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if game.ApkPath != apkPath {
		t.Errorf("ApkPath = %q, want %q", game.ApkPath, apkPath)
	}
	if game.OBBPath == "" {
		t.Error("OBBPath should be non-empty when a paired OBB exists in the same directory")
	} else if game.OBBPath != obbPath {
		t.Errorf("OBBPath = %q, want %q", game.OBBPath, obbPath)
	}
	if game.OBBSizeBytes == 0 {
		t.Error("OBBSizeBytes should be > 0 when a paired OBB exists")
	}
}

// TestRevalidateGame_UpdatesApkPathOnPathChange verifies the AC1
// revalidation contract: when RevalidateGame is called and the file
// has been moved to a new location, the games.apk_path is updated
// accordingly so the file server picks up the new path.
//
// We simulate "path change" by:
//
//  1. Inserting a game at the OLD path with a stale mtime in the DB.
//  2. Moving the file to a NEW path.
//  3. Touching the NEW file so its mtime differs from the stored
//     last_updated, triggering the revalidate branch.
//  4. Asserting the DB now stores the NEW path.
//
// Story 9.10 T2 / Subtask 2.3.
func TestRevalidateGame_UpdatesApkPathOnPathChange(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.Mkdir(dataDir, 0755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	gm := NewGameManager(d, dataDir)

	const axmlPackage = "net.sorablue.shogo.FWMeasure"

	// 1. Create the APK at the OLD path and import it (this also
	//    triggers the valid-APK path; ApkPath is set to oldPath).
	oldDir := filepath.Join(dataDir, "old")
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatalf("mkdir old: %v", err)
	}
	oldPath := filepath.Join(oldDir, "mygame.apk")
	makeValidAPKWithPackage(t, oldDir, "mygame.apk", axmlPackage)

	if err := gm.ImportAPK(oldPath); err != nil {
		t.Fatalf("ImportAPK: %v", err)
	}

	// Verify the initial state.
	g, err := d.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if g.ApkPath != oldPath {
		t.Fatalf("pre-condition: ApkPath = %q, want %q", g.ApkPath, oldPath)
	}

	// 2. Move the file to a NEW path. We read+write instead of os.Rename
	//    so the test is filesystem-agnostic.
	newDir := filepath.Join(dataDir, "new")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("mkdir new: %v", err)
	}
	newPath := filepath.Join(newDir, "mygame.apk")
	content, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("read old: %v", err)
	}
	if err := os.WriteFile(newPath, content, 0644); err != nil {
		t.Fatalf("write new: %v", err)
	}

	// 3. Force a future mtime on the new file so the revalidate
	//    branch (fileMtime != lastUpdatedSec) triggers.
	future := g.LastUpdated.Add(2 * 1e9) // +2 seconds in nanoseconds
	if err := os.Chtimes(newPath, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// 4. Call RevalidateGame at the new path.
	proceed, rvErr := gm.RevalidateGame(ctxBackground(), newPath, axmlPackage)
	if rvErr != nil {
		t.Fatalf("RevalidateGame: %v", rvErr)
	}
	_ = proceed

	// 5. Verify the DB now stores the NEW path.
	g2, err := d.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if g2.ApkPath != newPath {
		t.Errorf("ApkPath = %q, want %q (revalidate should update to the new path)", g2.ApkPath, newPath)
	}
}

// TestRevalidateGame_UpdatesApkPathWhenMtimeUnchanged_ApkPathEmpty
// pins the 9.10-post bugfix: when the file mtime is EQUAL to the
// stored last_updated (no mtime change → the revalidate branch is
// skipped) but apk_path is empty (pre-9.10 row), the revalidate
// MUST still write apk_path so the file server can serve the file
// from the real disk location. Otherwise the AkiBonbon live case
// (file mtime == last_updated to the second) stays broken.
//
// Story 9.10-post bugfix.
func TestRevalidateGame_UpdatesApkPathWhenMtimeUnchanged_ApkPathEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.Mkdir(dataDir, 0755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	gm := NewGameManager(d, dataDir)

	const axmlPackage = "net.sorablue.shogo.FWMeasure"

	// 1. Create the APK and import it (so apk_path gets set).
	apkDir := filepath.Join(dataDir, "live")
	if err := os.MkdirAll(apkDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	apkPath := filepath.Join(apkDir, "mygame.apk")
	makeValidAPKWithPackage(t, apkDir, "mygame.apk", axmlPackage)

	if err := gm.ImportAPK(apkPath); err != nil {
		t.Fatalf("ImportAPK: %v", err)
	}

	// 2. Simulate a pre-9.10 row: blank apk_path and freeze
	//    last_updated to match the file's mtime (the operator's
	//    "I re-scanned and the file did not change" workflow).
	if _, err := d.GetGameByPackage(axmlPackage); err != nil {
		t.Fatalf("get: %v", err)
	}
	info, statErr := os.Stat(apkPath)
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	// mtime rounded to seconds to mirror RevalidateGame's
	// fileMtimeSec == lastUpdatedSec comparison.
	frozen := time.Unix(info.ModTime().Unix(), 0)
	if err := d.UpdateApkAndOBBPath(ctxBackground(), axmlPackage, "", ""); err != nil {
		t.Fatalf("clear apk_path: %v", err)
	}
	// Freeze last_updated to the file's mtime (seconds) via a
	// small transaction so the revalidate's mtime-equals-
	// last_updated branch is skipped.
	tx, txErr := d.BeginTx(ctxBackground(), nil)
	if txErr != nil {
		t.Fatalf("begin tx: %v", txErr)
	}
	if _, err := tx.ExecContext(ctxBackground(),
		`UPDATE games SET last_updated = ? WHERE package_name = ?`,
		frozen.Unix(), axmlPackage,
	); err != nil {
		_ = tx.Rollback()
		t.Fatalf("freeze last_updated: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Touch the file so its mtime matches frozen exactly (Stat
	// may return sub-second resolution that we just truncated).
	if err := os.Chtimes(apkPath, frozen, frozen); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// 3. Call RevalidateGame WITHOUT changing the mtime.
	proceed, rvErr := gm.RevalidateGame(ctxBackground(), apkPath, axmlPackage)
	if rvErr != nil {
		t.Fatalf("RevalidateGame: %v", rvErr)
	}
	_ = proceed

	// 4. Verify apk_path is now populated even though the mtime
	//    branch was skipped.
	g2, err := d.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if g2.ApkPath != apkPath {
		t.Errorf("ApkPath = %q, want %q (mtime unchanged but apk_path empty: revalidate must backfill)",
			g2.ApkPath, apkPath)
	}
}

// TestRevalidateGame_DoesNotTouchApkPath_WhenMtimeUnchanged_ApkPathSet
// is the negative companion of the bugfix test: when the mtime
// is unchanged AND apk_path is already populated, the revalidate
// must NOT touch apk_path (or any other column). This protects
// the no-churn contract: every startup revalidate should not
// trigger a row update for already-migrated rows.
//
// Story 9.10-post bugfix (regression guard).
func TestRevalidateGame_DoesNotTouchApkPath_WhenMtimeUnchanged_ApkPathSet(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.Mkdir(dataDir, 0755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	gm := NewGameManager(d, dataDir)

	const axmlPackage = "net.sorablue.shogo.FWMeasure"
	apkDir := filepath.Join(dataDir, "live")
	if err := os.MkdirAll(apkDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	apkPath := filepath.Join(apkDir, "mygame.apk")
	makeValidAPKWithPackage(t, apkDir, "mygame.apk", axmlPackage)

	if err := gm.ImportAPK(apkPath); err != nil {
		t.Fatalf("ImportAPK: %v", err)
	}

	// Force the file mtime to match the stored last_updated (no
	// mtime change → revalidate must be a no-op).
	g, err := d.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := os.Chtimes(apkPath, g.LastUpdated, g.LastUpdated); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	proceed, rvErr := gm.RevalidateGame(ctxBackground(), apkPath, axmlPackage)
	if rvErr != nil {
		t.Fatalf("RevalidateGame: %v", err)
	}
	_ = proceed

	g2, err := d.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if g2.ApkPath != apkPath {
		t.Errorf("ApkPath = %q, want %q (must not be touched when already set and mtime unchanged)",
			g2.ApkPath, apkPath)
	}
	// last_updated should also be untouched — no churn.
	if !g2.LastUpdated.Equal(g.LastUpdated) {
		t.Errorf("last_updated changed: was %v, now %v (must not churn)",
			g.LastUpdated, g2.LastUpdated)
	}
}
