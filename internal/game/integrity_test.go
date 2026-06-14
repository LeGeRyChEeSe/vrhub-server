package game

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

func makeValidAPK(t *testing.T, dir string, name string) {
	t.Helper()
	makeValidAPKWithPackage(t, dir, name, "com.test.app")
}

func makeValidAPKWithPackage(t *testing.T, dir string, name, packageName string) {
	t.Helper()
	// C-16: use a real AXML (binary XML) manifest. The fixture's actual
	// package is "net.sorablue.shogo.FWMeasure" regardless of the
	// packageName argument — this helper no longer creates a manifest
	// with a specific package name. Callers that depend on a specific
	// package should use ExtractAPKMetadata directly to verify it.
	axmlContent, err := os.ReadFile(filepath.Join("testdata", "AndroidManifest.bin.axml"))
	if err != nil {
		t.Fatalf("read AXML fixture: %v", err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	w := zip.NewWriter(f)
	mf, err := w.Create(androidManifestPath)
	if err != nil {
		t.Fatalf("create manifest in zip: %v", err)
	}
	if _, err := mf.Write(axmlContent); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
}

func TestValidateAPK_ValidAPK(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := filepath.Join(tmpDir, "valid.apk")
	makeValidAPK(t, tmpDir, "valid.apk")

	result := ValidateAPK(apkPath)
	if result.Corrupted {
		t.Errorf("expected valid APK, got corrupted: %s", result.CorruptionReason)
	}
}

func TestValidateAPK_InvalidZIP(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := filepath.Join(tmpDir, "corrupted.apk")

	if err := os.WriteFile(apkPath, []byte("not a zip file at all"), 0644); err != nil {
		t.Fatalf("write corrupted apk: %v", err)
	}

	result := ValidateAPK(apkPath)
	if !result.Corrupted {
		t.Error("expected corrupted APK, got valid")
	}
	if result.CorruptionReason == "" {
		t.Error("expected corruption reason, got empty string")
	}
}

func TestValidateAPK_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := filepath.Join(tmpDir, "empty.apk")

	if err := os.WriteFile(apkPath, []byte{}, 0644); err != nil {
		t.Fatalf("write empty apk: %v", err)
	}

	result := ValidateAPK(apkPath)
	if !result.Corrupted {
		t.Error("expected corrupted APK (empty), got valid")
	}
}

func TestValidateAPK_EmptyZIP(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := filepath.Join(tmpDir, "emptyzip.apk")

	f, err := os.Create(apkPath)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	w := zip.NewWriter(f)
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	result := ValidateAPK(apkPath)
	if !result.Corrupted {
		t.Error("expected corrupted APK (empty ZIP), got valid")
	}
	if result.CorruptionReason == "" {
		t.Error("expected corruption reason for empty ZIP, got empty string")
	}
}

// TestValidateAPK_LargeEntry_NotCorrupted is a regression test for B2 (Story 1.7).
//
// Live session 2026-06-08: a user reported that all Quest VR games were
// marked as "corrupted" because the integrity validator rejected any APK
// whose UncompressedSize64 exceeded 100 MiB. This is a systemic
// false-positive: native libs (`lib/arm64-v8a/libmain.so`) and DEX files
// in Quest games routinely exceed 100 MiB when uncompressed, even though
// the compressed APK is well under that limit.
//
// The fix raises maxUncompressedSizePerEntry to 4 GiB (the practical
// filesystem limit on ext4/NTFS and the uint32 boundary of ZIP64). This
// test asserts that an APK with a 200 MiB uncompressed entry is no longer
// flagged as corrupted.
//
// Note on test design: Go's archive/zip.Writer overwrites FileHeader's
// UncompressedSize64 with the actual stream size on Close, so we cannot
// fake a large entry by writing 1 KiB of data with a 200 MiB header. We
// must write a real 110 MiB entry with zip.Store (no compression) so the
// central directory reports the true large size. The entry is zeros, which
// is the worst case for ZIP write time (no compression possible).
func TestValidateAPK_LargeEntry_NotCorrupted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 110 MiB write in -short mode")
	}
	tmpDir := t.TempDir()
	apkPath := filepath.Join(tmpDir, "large_entry.apk")

	f, err := os.Create(apkPath)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	w := zip.NewWriter(f)

	// Create a real AXML manifest (so the APK is otherwise valid).
	axmlContent, err := os.ReadFile(filepath.Join("testdata", "AndroidManifest.bin.axml"))
	if err != nil {
		t.Fatalf("read AXML fixture: %v", err)
	}
	mf, err := w.Create(androidManifestPath)
	if err != nil {
		t.Fatalf("create manifest entry: %v", err)
	}
	if _, err := mf.Write(axmlContent); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Write a 110 MiB entry with zip.Store so the central directory
	// reports a UncompressedSize64 of 110 MiB, well above the previous
	// 100 MiB limit. We stream zeros in 1 MiB chunks to avoid a single
	// huge allocation. zip.Store is required: zip.Deflate compresses
	// zeros to ~0 bytes and UncompressedSize64 in the central directory
	// would no longer match what we're trying to test.
	const largeSize = 110 * 1024 * 1024 // 110 MiB
	hdr := &zip.FileHeader{
		Name:   "lib/arm64-v8a/libmain.so",
		Method: zip.Store,
	}
	big, err := w.CreateHeader(hdr)
	if err != nil {
		t.Fatalf("create large entry: %v", err)
	}
	chunk := make([]byte, 1024*1024) // 1 MiB
	written := 0
	for written < largeSize {
		n := largeSize - written
		if n > len(chunk) {
			n = len(chunk)
		}
		if _, err := big.Write(chunk[:n]); err != nil {
			t.Fatalf("write large entry chunk at %d/%d: %v", written, largeSize, err)
		}
		written += n
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	// Sanity check: the file should be at least 110 MiB.
	info, err := os.Stat(apkPath)
	if err != nil {
		t.Fatalf("stat apk: %v", err)
	}
	if info.Size() < largeSize {
		t.Fatalf("test setup error: apk size %d < expected %d", info.Size(), largeSize)
	}

	result := ValidateAPK(apkPath)
	if result.Corrupted {
		t.Errorf("APK with 110 MiB uncompressed entry should NOT be corrupted, got reason: %q", result.CorruptionReason)
	}
}

func TestValidateOBB_ValidOBB(t *testing.T) {
	tmpDir := t.TempDir()
	obbPath := filepath.Join(tmpDir, "main.1.com.example.game.obb")

	if err := os.WriteFile(obbPath, []byte("fake obb data"), 0644); err != nil {
		t.Fatalf("write obb: %v", err)
	}

	result := ValidateOBB(obbPath)
	if result.Corrupted {
		t.Errorf("expected valid OBB, got corrupted: %s", result.CorruptionReason)
	}
}

func TestValidateOBB_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	obbPath := filepath.Join(tmpDir, "main.1.com.example.game.obb")

	if err := os.WriteFile(obbPath, []byte{}, 0644); err != nil {
		t.Fatalf("write empty obb: %v", err)
	}

	result := ValidateOBB(obbPath)
	if !result.Corrupted {
		t.Error("expected corrupted OBB (empty), got valid")
	}
	if result.CorruptionReason == "" {
		t.Error("expected corruption reason, got empty string")
	}
}

func TestValidateOBB_NonStandardNaming(t *testing.T) {
	tmpDir := t.TempDir()
	obbPath := filepath.Join(tmpDir, "weirdname.obb")

	if err := os.WriteFile(obbPath, []byte("data"), 0644); err != nil {
		t.Fatalf("write obb: %v", err)
	}

	result := ValidateOBB(obbPath)
	if result.Corrupted {
		t.Error("non-standard naming should not be corrupted, only flagged")
	}
	if result.CorruptionReason == "" {
		t.Error("expected non-standard naming reason")
	}
}

func TestValidateFile_APK(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := filepath.Join(tmpDir, "test.apk")
	makeValidAPK(t, tmpDir, "test.apk")

	result := ValidateFile(apkPath)
	if result.Corrupted {
		t.Errorf("expected valid APK via ValidateFile, got corrupted: %s", result.CorruptionReason)
	}
}

func TestValidateFile_OBB(t *testing.T) {
	tmpDir := t.TempDir()
	obbPath := filepath.Join(tmpDir, "main.1.com.game.obb")

	if err := os.WriteFile(obbPath, []byte("data"), 0644); err != nil {
		t.Fatalf("write obb: %v", err)
	}

	result := ValidateFile(obbPath)
	if result.Corrupted {
		t.Errorf("expected valid OBB via ValidateFile, got corrupted: %s", result.CorruptionReason)
	}
}

func TestValidateFile_UnsupportedExtension(t *testing.T) {
	tmpDir := t.TempDir()
	txtPath := filepath.Join(tmpDir, "readme.txt")

	if err := os.WriteFile(txtPath, []byte("data"), 0644); err != nil {
		t.Fatalf("write txt: %v", err)
	}

	result := ValidateFile(txtPath)
	if result.Corrupted {
		t.Error("unsupported extension should not be marked corrupted")
	}
}

// TestValidateFile_CaseInsensitive verifies that ValidateFile routes the file
// to the correct validator regardless of extension case. Regression for
// debt-triage-2026-06-06 C-03 (the "lowerName" variable was misnamed — it was
// NOT actually lowercased before the switch, so `.APK`/`.OBB` were silently
// dropped instead of validated).
//
// Strategy: write a file with an UPPERCASE extension but INVALID content.
// Before the fix, ValidateFile returns IntegrityResult{} (zero value, not
// corrupted) because the switch doesn't match `.APK`/`.OBB`. After the fix,
// it routes to ValidateAPK/ValidateOBB which detect the invalid content and
// mark the file as corrupted.
func TestValidateFile_CaseInsensitive(t *testing.T) {
	t.Run("APK uppercase routes to ValidateAPK", func(t *testing.T) {
		cases := []string{"INVALID.APK", "INVALID.Apk", "INVALID.aPk"}
		for _, name := range cases {
			t.Run(name, func(t *testing.T) {
				tmpDir := t.TempDir()
				upperPath := filepath.Join(tmpDir, name)
				lowerPath := filepath.Join(tmpDir, "invalid.apk")

				// Write garbage that will fail ZIP parsing
				garbage := []byte("not a real apk")
				if err := os.WriteFile(upperPath, garbage, 0644); err != nil {
					t.Fatalf("write upper: %v", err)
				}
				if err := os.WriteFile(lowerPath, garbage, 0644); err != nil {
					t.Fatalf("write lower: %v", err)
				}

				upperResult := ValidateFile(upperPath)
				lowerResult := ValidateFile(lowerPath)

				if !upperResult.Corrupted {
					t.Errorf("UPPERCASE %q should be marked corrupted (validation was attempted), got: %+v", name, upperResult)
				}
				if !lowerResult.Corrupted {
					t.Errorf("lowercase comparison should be marked corrupted, got: %+v", lowerResult)
				}
			})
		}
	})

	t.Run("OBB uppercase routes to ValidateOBB", func(t *testing.T) {
		// Use 0-byte files with VALID OBB naming — this triggers the size check
		// inside ValidateOBB and produces Corrupted=true. Before the fix,
		// ValidateFile would skip routing entirely (switch on `.OBB` doesn't
		// match `.obb`) and return IntegrityResult{} (Corrupted=false).
		cases := []string{"main.1.com.example.OBB", "main.1.com.example.Obb"}
		for _, name := range cases {
			t.Run(name, func(t *testing.T) {
				tmpDir := t.TempDir()
				upperPath := filepath.Join(tmpDir, name)
				lowerPath := filepath.Join(tmpDir, "main.1.com.example.obb")

				// 0-byte file — will fail OBB size check
				if err := os.WriteFile(upperPath, []byte{}, 0644); err != nil {
					t.Fatalf("write upper: %v", err)
				}
				if err := os.WriteFile(lowerPath, []byte{}, 0644); err != nil {
					t.Fatalf("write lower: %v", err)
				}

				upperResult := ValidateFile(upperPath)
				lowerResult := ValidateFile(lowerPath)

				if !upperResult.Corrupted {
					t.Errorf("UPPERCASE %q should be marked corrupted (size check), got: %+v", name, upperResult)
				}
				if !lowerResult.Corrupted {
					t.Errorf("lowercase comparison should be marked corrupted, got: %+v", lowerResult)
				}
			})
		}
	})

	t.Run("unsupported extension stays unsupported", func(t *testing.T) {
		tmpDir := t.TempDir()
		txtPath := filepath.Join(tmpDir, "README.TXT")
		if err := os.WriteFile(txtPath, []byte("data"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		result := ValidateFile(txtPath)
		if result.Corrupted {
			t.Error("unsupported extension (even uppercase) should not be marked corrupted")
		}
	})
}

func TestImportAPK_CorruptedAPK_StoredInDB(t *testing.T) {
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

	// Create a corrupted APK (not a valid ZIP)
	corruptedAPK := filepath.Join(dataDir, "com.corrupt.game.apk")
	if err := os.WriteFile(corruptedAPK, []byte("this is not a zip file"), 0644); err != nil {
		t.Fatalf("write corrupted apk: %v", err)
	}

	err = gm.ImportAPK(corruptedAPK)
	if err != nil {
		t.Fatalf("ImportAPK should succeed for corrupted APK (stored in DB), got error: %v", err)
	}

	// Verify game is stored with corrupted=true
	game, err := d.GetGameByPackage("com.corrupt.game")
	if err != nil {
		t.Fatalf("expected game to be found in DB, got error: %v", err)
	}

	if !game.Corrupted {
		t.Error("expected game to be marked as corrupted=true")
	}
	if game.CorruptionReason == "" {
		t.Error("expected corruption_reason to be set")
	}
	if game.Exposed {
		t.Error("expected corrupted game to have exposed=false")
	}
}

func TestImportAPK_ValidAPK_NotCorrupted(t *testing.T) {
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

	// C-16: makeValidAPKWithPackage now uses a real AXML fixture with
	// package "net.sorablue.shogo.FWMeasure". The filename and the
	// argument packageName are no longer the actual package — we look
	// up the game by the actual AXML package.
	const axmlPackage = "net.sorablue.shogo.FWMeasure"
	validAPK := filepath.Join(dataDir, "com.valid.game.apk")
	makeValidAPKWithPackage(t, dataDir, "com.valid.game.apk", axmlPackage)

	err = gm.ImportAPK(validAPK)
	if err != nil {
		t.Fatalf("ImportAPK should succeed for valid APK, got error: %v", err)
	}

	game, err := d.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("expected game to be found in DB, got error: %v", err)
	}

	if game.Corrupted {
		t.Error("expected valid game to have corrupted=false")
	}
	if !game.Exposed {
		t.Error("expected valid game to have exposed=true")
	}
}

func TestImportAPK_CorruptedOBB_StoredInDB(t *testing.T) {
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

	// Create a valid APK. C-16: package name comes from the AXML
	// fixture, not the helper argument. The OBB filename is built
	// from the actual fixture package.
	const axmlPackage = "net.sorablue.shogo.FWMeasure"
	validAPK := filepath.Join(dataDir, "com.obb.game.apk")
	makeValidAPKWithPackage(t, dataDir, "com.obb.game.apk", axmlPackage)

	// Create an empty OBB file (corrupted) — the OBB naming uses the
	// AXML fixture's package, not the helper's packageName argument.
	corruptOBB := filepath.Join(dataDir, "main.1."+axmlPackage+".obb")
	if err := os.WriteFile(corruptOBB, []byte{}, 0644); err != nil {
		t.Fatalf("write empty obb: %v", err)
	}

	err = gm.ImportAPK(validAPK)
	if err != nil {
		t.Fatalf("ImportAPK should succeed even with corrupted OBB, got error: %v", err)
	}

	game, err := d.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("expected game to be found in DB, got error: %v", err)
	}

	if !game.Corrupted {
		t.Error("expected game with corrupted OBB to have corrupted=true")
	}
	if game.CorruptionReason == "" {
		t.Error("expected corruption_reason to contain OBB reason")
	}
}

func TestListGamesForMeta7z_ExcludesCorrupted(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// Insert a valid game
	validGame := types.GameEntry{
		ReleaseName: "com.valid.meta",
		GameName:    "Valid Game",
		PackageName: "com.valid.meta",
		VersionCode: 1,
		SizeBytes:   1024,
		Hash:        db.ComputeHash("com.valid.meta"),
		Corrupted:   false,
		Exposed:     true,
	}
	if err := d.InsertGame(validGame); err != nil {
		t.Fatalf("insert valid game: %v", err)
	}

	// Insert a corrupted game
	corruptGame := types.GameEntry{
		ReleaseName:      "com.corrupt.meta",
		GameName:         "Corrupted Game",
		PackageName:      "com.corrupt.meta",
		VersionCode:      2,
		SizeBytes:        2048,
		Hash:             db.ComputeHash("com.corrupt.meta"),
		Corrupted:        true,
		CorruptionReason: "invalid ZIP archive: EOF",
		Exposed:          false,
	}
	if err := d.InsertGame(corruptGame); err != nil {
		t.Fatalf("insert corrupted game: %v", err)
	}

	games, err := d.ListGamesForMeta7z()
	if err != nil {
		t.Fatalf("list games for meta.7z: %v", err)
	}

	if len(games) != 1 {
		t.Errorf("expected 1 game in meta.7z list, got %d", len(games))
	}
	if len(games) > 0 && games[0].PackageName != "com.valid.meta" {
		t.Errorf("expected only valid game in meta.7z list, got %q", games[0].PackageName)
	}
}

func TestUpdateCorruptionStatus(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game := types.GameEntry{
		ReleaseName: "com.status.test",
		GameName:    "Status Test",
		PackageName: "com.status.test",
		VersionCode: 1,
		SizeBytes:   512,
		Hash:        db.ComputeHash("com.status.test"),
		Corrupted:   false,
	}
	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	err = d.UpdateCorruptionStatus("com.status.test", true, "test corruption reason")
	if err != nil {
		t.Fatalf("update corruption status: %v", err)
	}

	retrieved, err := d.GetGameByPackage("com.status.test")
	if err != nil {
		t.Fatalf("get game: %v", err)
	}

	if !retrieved.Corrupted {
		t.Error("expected corrupted=true after update")
	}
	if retrieved.CorruptionReason != "test corruption reason" {
		t.Errorf("corruption_reason = %q, want %q", retrieved.CorruptionReason, "test corruption reason")
	}

	err = d.UpdateCorruptionStatus("com.status.test", false, "")
	if err != nil {
		t.Fatalf("update corruption status to clear: %v", err)
	}

	retrieved2, err := d.GetGameByPackage("com.status.test")
	if err != nil {
		t.Fatalf("get game after clearing: %v", err)
	}

	if retrieved2.Corrupted {
		t.Error("expected corrupted=false after clearing")
	}
	if retrieved2.CorruptionReason != "" {
		t.Errorf("corruption_reason should be empty, got %q", retrieved2.CorruptionReason)
	}
}

func TestExtractPackageNameFromPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{"standard path", "/data/games/com.example.game.apk", "com.example.game"},
		{"nested path", "/deep/nested/path/com.test.app.apk", "com.test.app"},
		{"no apk extension", "/data/games/readme.txt", ""},
		{"empty base", "/", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractPackageNameFromPath(tt.path)
			if result != tt.expected {
				t.Errorf("ExtractPackageNameFromPath(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}

func TestRevalidateGame_CorruptedToValid(t *testing.T) {
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

	// Insert a game that was previously corrupted
	corruptedGame := types.GameEntry{
		ReleaseName:      "com.restore.test",
		GameName:         "Restore Test",
		PackageName:      "com.restore.test",
		VersionCode:      1,
		SizeBytes:        0,
		Hash:             db.ComputeHash("com.restore.test"),
		Corrupted:        true,
		CorruptionReason: "invalid ZIP archive: EOF",
		Exposed:          false,
		LastUpdated:      time.Now().Add(-1 * time.Hour),
	}
	if err := d.InsertGame(corruptedGame); err != nil {
		t.Fatalf("insert corrupted game: %v", err)
	}

	// Create a valid APK to replace the corrupted one
	validAPK := filepath.Join(dataDir, "com.restore.test.apk")
	makeValidAPK(t, dataDir, "com.restore.test.apk")

	proceed, err := gm.RevalidateGame(context.Background(), validAPK, "com.restore.test")
	if err != nil {
		t.Fatalf("RevalidateGame should succeed: %v", err)
	}

	if proceed {
		t.Error("expected proceed=false for existing game")
	}

	retrieved, err := d.GetGameByPackage("com.restore.test")
	if err != nil {
		t.Fatalf("get game after revalidation: %v", err)
	}

	if retrieved.Corrupted {
		t.Error("expected corrupted=false after revalidation of valid APK")
	}
	if retrieved.CorruptionReason != "" {
		t.Errorf("corruption_reason should be empty, got %q", retrieved.CorruptionReason)
	}
}

func TestRevalidateGame_ValidToCorrupted(t *testing.T) {
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

	// Insert a valid game
	validGame := types.GameEntry{
		ReleaseName:      "com.becomes.corrupt",
		GameName:         "Becomes Corrupt",
		PackageName:      "com.becomes.corrupt",
		VersionCode:      1,
		SizeBytes:        0,
		Hash:             db.ComputeHash("com.becomes.corrupt"),
		Corrupted:        false,
		CorruptionReason: "",
		Exposed:          true,
		LastUpdated:      time.Now().Add(-1 * time.Hour),
	}
	if err := d.InsertGame(validGame); err != nil {
		t.Fatalf("insert valid game: %v", err)
	}

	// Create a corrupted APK to replace the valid one
	corruptAPK := filepath.Join(dataDir, "com.becomes.corrupt.apk")
	if err := os.WriteFile(corruptAPK, []byte("not a zip"), 0644); err != nil {
		t.Fatalf("write corrupt apk: %v", err)
	}

	proceed, err := gm.RevalidateGame(context.Background(), corruptAPK, "com.becomes.corrupt")
	if err != nil {
		t.Fatalf("RevalidateGame should succeed: %v", err)
	}

	if proceed {
		t.Error("expected proceed=false for existing game")
	}

	retrieved, err := d.GetGameByPackage("com.becomes.corrupt")
	if err != nil {
		t.Fatalf("get game after revalidation: %v", err)
	}

	if !retrieved.Corrupted {
		t.Error("expected corrupted=true after revalidation of corrupted APK")
	}
	if retrieved.CorruptionReason == "" {
		t.Error("expected corruption_reason to be set")
	}
}
