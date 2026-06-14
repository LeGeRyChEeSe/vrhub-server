package game

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanDirectory_FindsAPKFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test APK files
	for _, name := range []string{"game1.apk", "game2.apk"} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("fake apk"), 0644); err != nil {
			t.Fatalf("create test file: %v", err)
		}
	}

	files, err := ScanDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	apkCount := 0
	for _, f := range files {
		if f.IsAPK {
			apkCount++
		}
	}

	if apkCount != 2 {
		t.Errorf("found %d APKs, want 2", apkCount)
	}
}

func TestScanDirectory_FindsOBBFiles(t *testing.T) {
	tmpDir := t.TempDir()

	for _, name := range []string{"main.1.com.example.game.obb", "patch.1.com.example.game.obb"} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("fake obb"), 0644); err != nil {
			t.Fatalf("create test file: %v", err)
		}
	}

	files, err := ScanDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	obbCount := 0
	for _, f := range files {
		if !f.IsAPK && filepath.Ext(f.Name) == ".obb" {
			obbCount++
		}
	}

	if obbCount != 2 {
		t.Errorf("found %d OBBs, want 2", obbCount)
	}
}

func TestScanDirectory_NestedDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	subDir := filepath.Join(tmpDir, "subdir1", "subdir2")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("create subdir: %v", err)
	}

	testFiles := []string{
		filepath.Join(tmpDir, "root.apk"),
		filepath.Join(subDir, "nested.apk"),
		filepath.Join(filepath.Join(tmpDir, "subdir1"), "mid.obb"),
	}

	for _, f := range testFiles {
		if err := os.WriteFile(f, []byte("data"), 0644); err != nil {
			t.Fatalf("create test file %s: %v", f, err)
		}
	}

	files, err := ScanDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 3 {
		t.Errorf("found %d files, want 3", len(files))
	}
}

func TestScanDirectory_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	files, err := ScanDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 0 {
		t.Errorf("found %d files in empty dir, want 0", len(files))
	}
}

func TestScanDirectory_NonExistentDir(t *testing.T) {
	_, err := ScanDirectory("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for non-existent directory, got nil")
	}
}

func TestScanDirectory_FileSizes(t *testing.T) {
	tmpDir := t.TempDir()

	expectedSize := int64(1024)
	testFile := filepath.Join(tmpDir, "test.apk")
	if err := os.WriteFile(testFile, make([]byte, expectedSize), 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	files, err := ScanDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("found %d files, want 1", len(files))
	}

	if files[0].Size != expectedSize {
		t.Errorf("file size = %d, want %d", files[0].Size, expectedSize)
	}
}

func TestScanDirectory_SkipsNonAPKOBBCFiles(t *testing.T) {
	tmpDir := t.TempDir()

	for _, name := range []string{"readme.txt", "icon.png", "data.json"} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("data"), 0644); err != nil {
			t.Fatalf("create test file: %v", err)
		}
	}

	files, err := ScanDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 0 {
		t.Errorf("found %d files, want 0 (should skip non-APK/OBB)", len(files))
	}
}

func TestScanDirectory_MixedFiles(t *testing.T) {
	tmpDir := t.TempDir()

	filesToCreate := map[string]bool{
		"game1.apk":           true,
		"main.1.com.game.obb": false,
		"readme.txt":          false,
		"game2.apk":           true,
	}

	for name := range filesToCreate {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("data"), 0644); err != nil {
			t.Fatalf("create test file: %v", err)
		}
	}

	result, err := ScanDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	apkCount := 0
	obbCount := 0
	for _, f := range result {
		if f.IsAPK {
			apkCount++
		} else if filepath.Ext(f.Name) == ".obb" {
			obbCount++
		}
	}

	if apkCount != 2 {
		t.Errorf("APK count = %d, want 2", apkCount)
	}
	if obbCount != 1 {
		t.Errorf("OBB count = %d, want 1", obbCount)
	}
}

func TestScanDirectory_PathCorrectness(t *testing.T) {
	tmpDir := t.TempDir()

	apkPath := filepath.Join(tmpDir, "subdir", "game.apk")
	if err := os.MkdirAll(filepath.Dir(apkPath), 0755); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if err := os.WriteFile(apkPath, []byte("data"), 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	files, err := ScanDirectory(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("found %d files, want 1", len(files))
	}

	if files[0].Path != apkPath {
		t.Errorf("path = %q, want %q", files[0].Path, apkPath)
	}
}

// TestScanDirectory_AkiBonbon_Recursive reproduces the live 2026-06-12
// bug at the lowest layer of the stack: the file walker must find an
// APK sitting at the root of game_folders (no enclosing sub-folder).
// Pre-9.10 the operator assumed the scanner would do this; the
// scanner does (it uses filepath.WalkDir), but a future "optimize
// to non-recursive" change would silently miss the file.
//
// This is the AC7 regression gate that the end-to-end
// TestScanAndImport_AkiBonbon_AtRoot_StoresRealPath builds on.
//
// Story 9.10 (AC7).
func TestScanDirectory_AkiBonbon_Recursive(t *testing.T) {
	tmpDir := t.TempDir()

	// Mix: one APK at the root, one in a sub-folder (the
	// operator's pre-9.10 layout was always-sub-folder, but the
	// 9.10 layout must accept both).
	if err := os.WriteFile(filepath.Join(tmpDir, "AkiBonbon_v1.apk"), []byte("root apk"), 0644); err != nil {
		t.Fatalf("create root apk: %v", err)
	}
	subDir := filepath.Join(tmpDir, "AFGirlfriend_-NA")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "AFGirlfriend.apk"), []byte("sub apk"), 0644); err != nil {
		t.Fatalf("create sub apk: %v", err)
	}

	files, err := ScanDirectory(tmpDir)
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("len = %d, want 2 (one APK at root, one in sub-folder)", len(files))
	}

	// Verify both files were found and the Path field is the
	// absolute path the walker recorded (used by Story 9.10 to
	// populate games.apk_path).
	wantPaths := map[string]bool{
		filepath.Join(tmpDir, "AkiBonbon_v1.apk"): false,
		filepath.Join(subDir, "AFGirlfriend.apk"): false,
	}
	for _, f := range files {
		if _, ok := wantPaths[f.Path]; !ok {
			t.Errorf("unexpected file %q", f.Path)
		}
		wantPaths[f.Path] = true
	}
	for p, found := range wantPaths {
		if !found {
			t.Errorf("file %q not found by ScanDirectory", p)
		}
	}
}

// TestScanDirectory_CaseInsensitiveExtensions is a regression test for
// debt-triage-2026-06-06 C-04. It verifies that ScanDirectory correctly
// classifies files with UPPERCASE / mixed-case APK/OBB extensions. The
// current code at scanner.go:47 uses strings.ToLower(filepath.Ext(name))
// before the switch, but a future "optimization" that drops the ToLower
// would silently skip uppercase files. This test catches that.
//
// Test files use simple `.apk`/`.obb` names (no OBB naming convention
// required for the IsAPK/IsOBB routing — naming convention is enforced
// elsewhere by ValidateOBB).
func TestScanDirectory_CaseInsensitiveExtensions(t *testing.T) {
	tmpDir := t.TempDir()

	// Mix of cases: .APK, .Apk, .aPk, .apk; same for OBB
	filesToCreate := []string{
		"upper.APK",
		"mixed.Apk",
		"weird.aPk",
		"lower.apk",
		"upper.OBB",
		"mixed.Obb",
		"weird.oBB",
		"lower.obb",
		"readme.txt", // not classified
	}

	for _, name := range filesToCreate {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("data"), 0644); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	files, err := ScanDirectory(tmpDir)
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}

	var apks, obbs int
	for _, f := range files {
		ext := filepath.Ext(f.Name)
		switch ext {
		case ".apk", ".APK", ".Apk", ".aPk", ".ApK", ".aPK", ".APk":
			apks++
		case ".obb", ".OBB", ".Obb", ".oBb", ".ObB", ".oBB", ".OBb":
			obbs++
		}
	}

	if apks != 4 {
		t.Errorf("APK count = %d, want 4 (4 case variants of .apk should all be classified as APK)", apks)
	}
	if obbs != 4 {
		t.Errorf("OBB count = %d, want 4 (4 case variants of .obb should all be classified as OBB)", obbs)
	}
}
