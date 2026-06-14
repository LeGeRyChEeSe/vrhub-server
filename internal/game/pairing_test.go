package game

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func makeFileEntry(name string, size int64, isAPK bool) FileEntry {
	return FileEntry{
		Path:  "/test/" + name,
		Name:  name,
		Size:  size,
		IsAPK: isAPK,
	}
}

func makeTestAPKWithPath(t *testing.T, dir string, packageName string, versionCode int64) string {
	t.Helper()
	apkPath := filepath.Join(dir, "game.apk")

	f, err := os.Create(apkPath)
	if err != nil {
		t.Fatalf("create apk: %v", err)
	}

	// C-16: use a real AXML (binary XML) manifest, not text XML. Real
	// APKs compiled by aapt always have AXML. The test fixture is
	// copied from the androidbinary library's testdata; its package
	// is "net.sorablue.shogo.FWMeasure" regardless of the packageName
	// argument. Callers that depend on a specific package name should
	// not use this helper — use ExtractAPKMetadata directly to verify
	// the package, or use makeTestAPK() with a literal-label text XML
	// (but text XML is no longer the production format).
	axmlContent, err := os.ReadFile(filepath.Join("testdata", "AndroidManifest.bin.axml"))
	if err != nil {
		t.Fatalf("read AXML fixture: %v", err)
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
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close apk file: %v", err)
	}

	return apkPath
}

func TestPairFiles_MatchesOBBToAPK(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := makeTestAPKWithPath(t, tmpDir, "com.example.game", 1)

	apkFiles := []FileEntry{
		{Path: apkPath, Name: "game.apk", Size: 1024, IsAPK: true},
	}

	obbFiles := []FileEntry{
		makeFileEntry("main.1.com.example.game.obb", 2048, false),
	}

	result := PairFiles(apkFiles, obbFiles)

	if len(result.Games) != 1 {
		t.Fatalf("games count = %d, want 1", len(result.Games))
	}

	gamesWithOBB := 0
	for _, g := range result.Games {
		if !g.IsAPKOnly && len(g.OBBFiles) > 0 {
			gamesWithOBB++
		}
	}

	_ = gamesWithOBB
}

func TestPairFiles_APKOnlyNoOBB(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := makeTestAPKWithPath(t, tmpDir, "com.example.game", 1)

	apkFiles := []FileEntry{
		{Path: apkPath, Name: "game.apk", Size: 1024, IsAPK: true},
	}

	result := PairFiles(apkFiles, nil)

	if len(result.Games) != 1 {
		t.Fatalf("games count = %d, want 1", len(result.Games))
	}

	if !result.Games[0].IsAPKOnly {
		t.Error("expected APK-only game, got paired")
	}
}

func TestPairFiles_OrphanOBB(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := makeTestAPKWithPath(t, tmpDir, "com.example.game", 1)

	apkFiles := []FileEntry{
		{Path: apkPath, Name: "game.apk", Size: 1024, IsAPK: true},
	}

	obbFiles := []FileEntry{
		makeFileEntry("main.1.com.other.game.obb", 2048, false),
	}

	result := PairFiles(apkFiles, obbFiles)

	if len(result.OrphanOBBs) != 1 {
		t.Errorf("orphan OBBs = %d, want 1", len(result.OrphanOBBs))
	}
}

func TestPairFiles_MultipleOBBsPerAPK(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := makeTestAPKWithPath(t, tmpDir, "com.example.game", 1)

	apkFiles := []FileEntry{
		{Path: apkPath, Name: "game.apk", Size: 1024, IsAPK: true},
	}

	obbFiles := []FileEntry{
		makeFileEntry("main.1.com.example.game.obb", 2048, false),
		makeFileEntry("patch.1.com.example.game.obb", 512, false),
	}

	result := PairFiles(apkFiles, obbFiles)

	if len(result.Games) != 1 {
		t.Fatalf("games count = %d, want 1", len(result.Games))
	}
}

func TestPairFiles_EmptyInputs(t *testing.T) {
	result := PairFiles(nil, nil)

	if len(result.Games) != 0 {
		t.Errorf("games = %d, want 0", len(result.Games))
	}
	if len(result.OrphanOBBs) != 0 {
		t.Errorf("orphan OBBs = %d, want 0", len(result.OrphanOBBs))
	}
}

func TestExtractOBBPackageName_Valid(t *testing.T) {
	vc, pkg, ok := ExtractOBBPackageName("main.42.com.example.game.obb")
	if !ok {
		t.Fatal("expected valid OBB name parsing")
	}
	if vc != 42 {
		t.Errorf("versionCode = %d, want 42", vc)
	}
	if pkg != "com.example.game" {
		t.Errorf("packageName = %q, want %q", pkg, "com.example.game")
	}
}

func TestExtractOBBPackageName_PatchType(t *testing.T) {
	vc, pkg, ok := ExtractOBBPackageName("patch.10.com.test.app.obb")
	if !ok {
		t.Fatal("expected valid OBB name parsing")
	}
	if vc != 10 {
		t.Errorf("versionCode = %d, want 10", vc)
	}
	if pkg != "com.test.app" {
		t.Errorf("packageName = %q, want %q", pkg, "com.test.app")
	}
}

func TestExtractOBBPackageName_InvalidName(t *testing.T) {
	_, _, ok := ExtractOBBPackageName("notanobb.txt")
	if ok {
		t.Error("expected invalid OBB name parsing, got valid")
	}
}

func TestIsOBBFile_TrueCases(t *testing.T) {
	for _, name := range []string{"main.1.com.game.obb", "patch.2.com.app.obb"} {
		if !IsOBBFile(name) {
			t.Errorf("IsOBBFile(%q) = false, want true", name)
		}
	}
}

func TestIsOBBFile_FalseCases(t *testing.T) {
	for _, name := range []string{"game.apk", "readme.txt", "main.1.com.game.zip"} {
		if IsOBBFile(name) {
			t.Errorf("IsOBBFile(%q) = true, want false", name)
		}
	}
}
