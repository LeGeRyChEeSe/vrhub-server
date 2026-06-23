package game

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
)

// TestImportAPK_ResolvesTrailerOverride is AC1 (scanner override): when a
// "{releaseName}.trailer" sidecar sits next to the APK, ImportAPK records its
// URL (trimmed) in games.trailer_url. This is the always-wins, end-to-end
// guaranteed path of the resolution cascade.
func TestImportAPK_ResolvesTrailerOverride(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	d, err := db.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	gm := NewGameManager(d, dataDir)

	const axmlPackage = "net.sorablue.shogo.FWMeasure"
	apkDir := filepath.Join(dataDir, "Folder")
	if err := os.MkdirAll(apkDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	apkPath := filepath.Join(apkDir, "mygame.apk")
	makeValidAPKWithPackage(t, apkDir, "mygame.apk", axmlPackage)

	// Drop the operator override sidecar next to the APK. ReleaseName ==
	// packageName for imported APKs, so the sidecar is "{package}.trailer".
	const wantURL = "https://www.youtube.com/watch?v=OVERRIDE123"
	if err := os.WriteFile(filepath.Join(apkDir, axmlPackage+".trailer"), []byte("  "+wantURL+"\n"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	if err := gm.ImportAPK(apkPath); err != nil {
		t.Fatalf("ImportAPK: %v", err)
	}

	game, err := d.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if game.TrailerURL != wantURL {
		t.Errorf("TrailerURL = %q, want %q (trimmed override sidecar)", game.TrailerURL, wantURL)
	}
}

// TestImportAPK_NoTrailerOverride is the negative companion (AC5): with no
// sidecar, trailer_url stays empty.
func TestImportAPK_NoTrailerOverride(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	d, err := db.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	gm := NewGameManager(d, dataDir)

	const axmlPackage = "net.sorablue.shogo.FWMeasure"
	apkDir := filepath.Join(dataDir, "Folder")
	if err := os.MkdirAll(apkDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	apkPath := filepath.Join(apkDir, "mygame.apk")
	makeValidAPKWithPackage(t, apkDir, "mygame.apk", axmlPackage)

	if err := gm.ImportAPK(apkPath); err != nil {
		t.Fatalf("ImportAPK: %v", err)
	}

	game, err := d.GetGameByPackage(axmlPackage)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if game.TrailerURL != "" {
		t.Errorf("TrailerURL = %q, want \"\" (no override sidecar)", game.TrailerURL)
	}
}
