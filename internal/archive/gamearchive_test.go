package archive

import (
	"context"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeRandom writes n pseudo-random (incompressible) bytes to path so the 7z
// archive actually splits at a small -v size instead of compressing away.
func writeRandom(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	buf := make([]byte, n)
	rng := rand.New(rand.NewSource(int64(n) + 1))
	rng.Read(buf)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestValidateSplitSize(t *testing.T) {
	valid := []string{"2g", "500m", "64k", "4096b", "1", "100M", "2G"}
	for _, s := range valid {
		if !ValidateSplitSize(s) {
			t.Errorf("ValidateSplitSize(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "0", "2gb", "-1", "2 g", "abc", "g2", "1.5g"}
	for _, s := range invalid {
		if ValidateSplitSize(s) {
			t.Errorf("ValidateSplitSize(%q) = true, want false", s)
		}
	}
	if NormalizeSplitSize("garbage") != DefaultSplitSize {
		t.Errorf("NormalizeSplitSize(garbage) = %q, want %q", NormalizeSplitSize("garbage"), DefaultSplitSize)
	}
	if NormalizeSplitSize("500m") != "500m" {
		t.Errorf("NormalizeSplitSize(500m) should pass through")
	}
}

func TestIsArchivePartName(t *testing.T) {
	yes := []string{"com.example.game.7z.001", "Game Name.7z.002", "x.7z.123"}
	for _, n := range yes {
		if !IsArchivePartName(n) {
			t.Errorf("IsArchivePartName(%q) = false, want true", n)
		}
	}
	no := []string{"game.apk", "main.123.obb", "notes.txt", "game.7z", "game.7z.abc", "thumbnail.jpg"}
	for _, n := range no {
		if IsArchivePartName(n) {
			t.Errorf("IsArchivePartName(%q) = true, want false", n)
		}
	}
}

func TestGenerateGameArchive_RoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	gameSrc := t.TempDir() // simulate games living on a different tree than dataDir

	release := "com.example.game"
	pkg := "com.example.game"
	hash := "deadbeefdeadbeefdeadbeefdeadbeef"
	password := "s3cr3tPassw0rd"

	apkPath := filepath.Join(gameSrc, "Game.apk")
	obbPath := filepath.Join(gameSrc, "main.1.com.example.game.obb")
	writeRandom(t, apkPath, 250*1024) // 250 KiB
	writeRandom(t, obbPath, 200*1024) // 200 KiB

	spec := GameArchiveSpec{
		DataDir:     dataDir,
		Hash:        hash,
		ReleaseName: release,
		PackageName: pkg,
		APKPath:     apkPath,
		OBBPaths:    []string{obbPath},
		VersionCode: 1,
		Password:    password,
		SplitSize:   "100k", // force multiple volumes
	}

	ctx := context.Background()
	if err := GenerateGameArchive(ctx, spec); err != nil {
		t.Fatalf("GenerateGameArchive: %v", err)
	}

	parts, err := GameArchiveParts(dataDir, hash, release)
	if err != nil {
		t.Fatalf("GameArchiveParts: %v", err)
	}
	if len(parts) < 2 {
		t.Fatalf("expected the ~450 KiB payload split at 100k into >=2 parts, got %d: %v", len(parts), parts)
	}
	// Parts must be named {release}.7z.001, .002, … in order.
	for i, p := range parts {
		want := release + ".7z." + pad3(i+1)
		if p != want {
			t.Errorf("part[%d] = %q, want %q", i, p, want)
		}
	}

	if !GameArchiveUpToDate(spec) {
		t.Error("GameArchiveUpToDate = false right after generation, want true")
	}
	// A version bump must invalidate the archive.
	bumped := spec
	bumped.VersionCode = 2
	if GameArchiveUpToDate(bumped) {
		t.Error("GameArchiveUpToDate = true after version bump, want false")
	}
	// A password change must invalidate the archive.
	repw := spec
	repw.Password = "different"
	if GameArchiveUpToDate(repw) {
		t.Error("GameArchiveUpToDate = true after password change, want false")
	}

	// Extract the archive with the real 7zz binary and verify the VRP-internal
	// structure ({release}/{apk} and {release}/{pkg}/{obb}) round-trips.
	sevenZip, err := sevenZipBinaryPath(ctx, dataDir)
	if err != nil {
		t.Skipf("no 7z binary available to verify extraction: %v", err)
	}
	outDir := t.TempDir()
	firstPart := filepath.Join(GameArchiveDir(dataDir, hash), parts[0])
	cmd := exec.CommandContext(ctx, sevenZip, "x", firstPart, "-p"+password, "-o"+outDir, "-y")
	if isAndroid() {
		if libDir := GetAndroidLibDir(); libDir != "" {
			cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+libDir)
		}
	}
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("7z extract failed: %v (%s)", runErr, string(out))
	}

	extractedAPK := filepath.Join(outDir, release, "Game.apk")
	if _, statErr := os.Stat(extractedAPK); statErr != nil {
		t.Errorf("extracted APK not found at %s: %v", extractedAPK, statErr)
	}
	extractedOBB := filepath.Join(outDir, release, pkg, "main.1.com.example.game.obb")
	if _, statErr := os.Stat(extractedOBB); statErr != nil {
		t.Errorf("extracted OBB not found at %s: %v", extractedOBB, statErr)
	}

	// Regeneration must replace the parts and keep the archive up to date.
	if err := GenerateGameArchive(ctx, bumped); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if !GameArchiveUpToDate(bumped) {
		t.Error("GameArchiveUpToDate = false after regeneration with bumped version")
	}

	// Removal must clear parts and marker.
	if err := RemoveGameArchive(dataDir, hash, release); err != nil {
		t.Fatalf("RemoveGameArchive: %v", err)
	}
	if HasGameArchive(dataDir, hash, release) {
		t.Error("HasGameArchive = true after RemoveGameArchive")
	}
}

func TestGenerateGameArchive_RejectsBadSpec(t *testing.T) {
	ctx := context.Background()
	base := GameArchiveSpec{
		DataDir: t.TempDir(), Hash: "h", ReleaseName: "r", PackageName: "p",
		APKPath: "x.apk", Password: "pw",
	}
	// Missing password.
	noPw := base
	noPw.Password = ""
	if err := GenerateGameArchive(ctx, noPw); err == nil {
		t.Error("expected error for empty password")
	}
	// Path traversal in release name.
	bad := base
	bad.ReleaseName = "../evil"
	if err := GenerateGameArchive(ctx, bad); err == nil {
		t.Error("expected error for unsafe release name")
	}
}

func pad3(n int) string {
	switch {
	case n < 10:
		return "00" + itoa(n)
	case n < 100:
		return "0" + itoa(n)
	default:
		return itoa(n)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
