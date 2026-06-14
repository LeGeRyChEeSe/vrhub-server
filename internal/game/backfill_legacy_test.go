package game

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestBackfillLegacyApkPaths_FillsEmptyApkPath is the AC4 happy path:
// 3 games with apk_path="" all get backfilled to point at the APK
// in dataDir/games/{hash}/{pkgName}/, regardless of the filename
// (the operator's pre-9.10 manual copy used the
// {Label}__v{VersionCode}_{pkgName}.apk convention from Story 9.4 /
// B4, not {pkgName}.apk).
//
// Story 9.10 T4 (Subtask 4.3) + 9.10-post bugfix.
func TestBackfillLegacyApkPaths_FillsEmptyApkPath(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := tmpDir
	dbPath := filepath.Join(tmpDir, "test.db")

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// Pre-9.10 state: 3 games with apk_path="" and the legacy
	// files already in dataDir/games/.../ (operator's manual
	// copy, using the Story 9.4 / B4 filename convention
	// {Label}__v{VersionCode}_{pkgName}.apk).
	games := []struct {
		pkg   string
		hash  string
		label string
		vc    int
	}{
		{"com.af.girlfriend", "afhash0000000000000000000000000a01", "AFGirlfriend", 18},
		{"com.fishermans.tale", "fshash0000000000000000000000000a02", "FishermansTale", 25},
		{"com.superhot.vr", "shhash0000000000000000000000000a03", "SUPERHOT_VR", 161},
	}
	for _, g := range games {
		dir := filepath.Join(dataDir, "games", g.hash, g.pkg)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		// Legacy filename: {Label}__v{VersionCode}_{pkgName}.apk
		apkName := g.label + "__v" + itoa(g.vc) + "_" + g.pkg + ".apk"
		apk := filepath.Join(dir, apkName)
		if err := os.WriteFile(apk, []byte("legacy apk"), 0644); err != nil {
			t.Fatalf("write %s: %v", apk, err)
		}
		if err := d.InsertGame(types.GameEntry{
			ReleaseName: g.pkg,
			GameName:    g.pkg,
			PackageName: g.pkg,
			VersionCode: int64(g.vc),
			SizeBytes:   100,
			Hash:        g.hash,
			Exposed:     true,
			// ApkPath deliberately left empty (pre-9.10 state).
		}); err != nil {
			t.Fatalf("insert %s: %v", g.pkg, err)
		}
	}

	updated, err := BackfillLegacyApkPaths(context.Background(), d, dataDir)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if updated != 3 {
		t.Errorf("updated = %d, want 3", updated)
	}

	// Verify all 3 games now have a non-empty apk_path pointing
	// at the legacy location.
	for _, g := range games {
		got, err := d.GetGameByPackage(g.pkg)
		if err != nil {
			t.Fatalf("get %s: %v", g.pkg, err)
		}
		want := filepath.Join(dataDir, "games", g.hash, g.pkg, g.label+"__v"+itoa(g.vc)+"_"+g.pkg+".apk")
		if got.ApkPath != want {
			t.Errorf("%s: ApkPath = %q, want %q", g.pkg, got.ApkPath, want)
		}
	}
}

// TestBackfillLegacyApkPaths_SkipsGamesWithApkPathSet is the
// idempotency guard: games that already have an apk_path (set by
// the scanner or a previous backfill) are NOT touched.
//
// Story 9.10 T4 (Subtask 4.3).
func TestBackfillLegacyApkPaths_SkipsGamesWithApkPathSet(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := tmpDir
	dbPath := filepath.Join(tmpDir, "test.db")

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// Pre-set apk_path to a custom (non-legacy) location. The
	// backfill must leave it alone.
	presetPath := `/home/user/games/already-imported.apk`
	if err := d.InsertGame(types.GameEntry{
		ReleaseName: "com.already.imported",
		GameName:    "Already Imported",
		PackageName: "com.already.imported",
		VersionCode: 1,
		SizeBytes:   100,
		Hash:        "ahash0000000000000000000000000ffff",
		Exposed:     true,
		ApkPath:     presetPath,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	updated, err := BackfillLegacyApkPaths(context.Background(), d, dataDir)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if updated != 0 {
		t.Errorf("updated = %d, want 0 (game already had apk_path set)", updated)
	}

	got, err := d.GetGameByPackage("com.already.imported")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ApkPath != presetPath {
		t.Errorf("ApkPath = %q, want %q (must not be overwritten)", got.ApkPath, presetPath)
	}
}

// TestBackfillLegacyApkPaths_MissingLegacyFile_NoOp verifies the
// "scan handles missing files" behavior: a pre-9.10 game with no
// file at the legacy location is left untouched (apk_path stays
// empty). The startup scan (phase 1) will eventually mark the
// game as unexposed via ScanAndImportMultiple's normal logic.
//
// Story 9.10 T4 (Subtask 4.3).
func TestBackfillLegacyApkPaths_MissingLegacyFile_NoOp(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := tmpDir
	dbPath := filepath.Join(tmpDir, "test.db")

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// Game with apk_path="" but NO file on disk.
	if err := d.InsertGame(types.GameEntry{
		ReleaseName: "com.missing.game",
		GameName:    "Missing Game",
		PackageName: "com.missing.game",
		VersionCode: 1,
		SizeBytes:   100,
		Hash:        "mhash0000000000000000000000000cafe",
		Exposed:     true,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	updated, err := BackfillLegacyApkPaths(context.Background(), d, dataDir)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if updated != 0 {
		t.Errorf("updated = %d, want 0 (no file on disk)", updated)
	}

	got, err := d.GetGameByPackage("com.missing.game")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ApkPath != "" {
		t.Errorf("ApkPath = %q, want \"\" (file missing, must not invent a path)", got.ApkPath)
	}
}

// TestBackfillLegacyApkPaths_Idempotent_RunTwice verifies the
// one-shot migration contract: a second backfill pass returns 0
// updated (all games now have apk_path set), so the call is
// cheap and safe to make on every startup.
//
// Story 9.10 T4 (AC4 one-shot migration).
func TestBackfillLegacyApkPaths_Idempotent_RunTwice(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := tmpDir
	dbPath := filepath.Join(tmpDir, "test.db")

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// Set up 1 game with the legacy file (Story 9.4 / B4
	// filename convention).
	pkg := "com.idempotent.game"
	hash := "ihash0000000000000000000000000beef"
	label := "IdempotentGame"
	vc := 7
	dir := filepath.Join(dataDir, "games", hash, pkg)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	apkName := label + "__v" + itoa(vc) + "_" + pkg + ".apk"
	if err := os.WriteFile(filepath.Join(dir, apkName), []byte("apk"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := d.InsertGame(types.GameEntry{
		ReleaseName: pkg,
		GameName:    pkg,
		PackageName: pkg,
		VersionCode: int64(vc),
		SizeBytes:   100,
		Hash:        hash,
		Exposed:     true,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// First pass — populates apk_path.
	updated1, err := BackfillLegacyApkPaths(context.Background(), d, dataDir)
	if err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	if updated1 != 1 {
		t.Errorf("first pass updated = %d, want 1", updated1)
	}

	// Second pass — no-op.
	updated2, err := BackfillLegacyApkPaths(context.Background(), d, dataDir)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if updated2 != 0 {
		t.Errorf("second pass updated = %d, want 0 (idempotency)", updated2)
	}
}

// TestBackfillLegacyApkPaths_NilDB_Errors guards against the
// silent-success trap (a nil DB returning 0 updated makes the
// operator think the migration succeeded when it actually didn't
// run at all).
//
// Story 9.10 T4 (defensive — best-effort must still be loud about
// the misconfiguration that prevents it from running).
func TestBackfillLegacyApkPaths_NilDB_Errors(t *testing.T) {
	_, err := BackfillLegacyApkPaths(context.Background(), nil, t.TempDir())
	if err == nil {
		t.Error("expected error for nil db, got nil")
	}
}

// TestBackfillLegacyApkPaths_EmptyDataDir_Errors guards against
// the same silent-success trap when dataDir is empty.
func TestBackfillLegacyApkPaths_EmptyDataDir_Errors(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := db.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	_, err = BackfillLegacyApkPaths(context.Background(), d, "")
	if err == nil {
		t.Error("expected error for empty dataDir, got nil")
	}
}

// TestBackfillLegacyApkPaths_LabelVersionPkg_Filename pins the
// real-world legacy filename convention introduced by Story 9.4
// (B4) and the live AkiBonbon operator case. The fixture uses
// the exact AFGirlfriend__18___v1_com.NekumaSoft.AFGirlfriend.apk
// shape that was on disk in the bug report. If a future refactor
// narrows the backfill resolver to {pkgName}.apk again, this
// test will fail and the regression will be caught.
//
// Story 9.10 post-merge bugfix.
func TestBackfillLegacyApkPaths_LabelVersionPkg_Filename(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := tmpDir
	dbPath := filepath.Join(tmpDir, "test.db")

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// Real-shape fixtures matching the live operator's data:
	//   {Label}__v{VersionCode}_{pkgName}.apk
	fixtures := []struct {
		label string
		vc    int
		pkg   string
		hash  string
		obb   bool // also drop a paired OBB next to the APK
	}{
		{"AFGirlfriend", 18, "com.NekumaSoft.AFGirlfriend", "17b1bc76fde53022047520dfdeab0736", false},
		{"A_Fishermans_Tale", 25, "com.innerspacevr.afishermanstale", "684585a74ca6af4a51468aea60aa2091", true},
		{"SUPERHOT_VR", 161, "unity.SUPERHOT_Team.SUPERHOT_VR_QA", "036776226818aecfa5c98ccfe70576f8", false},
	}
	for _, f := range fixtures {
		dir := filepath.Join(dataDir, "games", f.hash, f.pkg)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		apkName := f.label + "__v" + itoa(f.vc) + "_" + f.pkg + ".apk"
		apk := filepath.Join(dir, apkName)
		if err := os.WriteFile(apk, []byte("legacy apk"), 0644); err != nil {
			t.Fatalf("write %s: %v", apk, err)
		}
		obbSize := int64(0)
		if f.obb {
			obbName := "main." + itoa(f.vc) + "." + f.pkg + ".obb"
			obb := filepath.Join(dir, obbName)
			if err := os.WriteFile(obb, []byte("legacy obb"), 0644); err != nil {
				t.Fatalf("write %s: %v", obb, err)
			}
			obbSize = 100
		}
		if err := d.InsertGame(types.GameEntry{
			ReleaseName:  f.pkg,
			GameName:     f.label,
			PackageName:  f.pkg,
			VersionCode:  int64(f.vc),
			SizeBytes:    100,
			Hash:         f.hash,
			Exposed:      true,
			OBBSizeBytes: obbSize,
		}); err != nil {
			t.Fatalf("insert %s: %v", f.pkg, err)
		}
	}

	updated, err := BackfillLegacyApkPaths(context.Background(), d, dataDir)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if updated != len(fixtures) {
		t.Errorf("updated = %d, want %d", updated, len(fixtures))
	}

	// Verify each game has apk_path pointing at the real legacy
	// file (and obb_path populated when an OBB was on disk).
	for _, f := range fixtures {
		got, err := d.GetGameByPackage(f.pkg)
		if err != nil {
			t.Fatalf("get %s: %v", f.pkg, err)
		}
		wantAPK := filepath.Join(dataDir, "games", f.hash, f.pkg, f.label+"__v"+itoa(f.vc)+"_"+f.pkg+".apk")
		if got.ApkPath != wantAPK {
			t.Errorf("%s: ApkPath = %q, want %q", f.pkg, got.ApkPath, wantAPK)
		}
		if f.obb {
			wantOBB := filepath.Join(dataDir, "games", f.hash, f.pkg, "main."+itoa(f.vc)+"."+f.pkg+".obb")
			if got.OBBPath != wantOBB {
				t.Errorf("%s: OBBPath = %q, want %q", f.pkg, got.OBBPath, wantOBB)
			}
		} else {
			if got.OBBPath != "" {
				t.Errorf("%s: OBBPath = %q, want \"\" (no OBB on disk)", f.pkg, got.OBBPath)
			}
		}
	}
}

// itoa is a tiny strconv.Itoa alias to keep test imports lean.
func itoa(n int) string {
	return strconv.Itoa(n)
}
