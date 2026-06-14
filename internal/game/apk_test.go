package game

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// makeTestAPK creates a test APK file with a manifest whose content is
// the given string. Used for tests that need a malformed manifest or
// an empty APK.
func makeTestAPK(t *testing.T, manifestContent string) string {
	t.Helper()
	tmpDir := t.TempDir()
	apkPath := filepath.Join(tmpDir, "test.apk")

	f, err := os.Create(apkPath)
	if err != nil {
		t.Fatalf("create apk: %v", err)
	}

	w := zip.NewWriter(f)
	mf, err := w.Create(androidManifestPath)
	if err != nil {
		t.Fatalf("create manifest in zip: %v", err)
	}
	if _, err := mf.Write([]byte(manifestContent)); err != nil {
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

// makeTestAPKFromBinaryManifest creates a test APK file with the manifest
// loaded as-is from `manifestPath` (typically a real AXML fixture).
// Used for tests that exercise the production AXML parsing path.
func makeTestAPKFromBinaryManifest(t *testing.T, manifestPath string) string {
	t.Helper()
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest fixture %s: %v", manifestPath, err)
	}
	tmpDir := t.TempDir()
	apkPath := filepath.Join(tmpDir, "test.apk")

	f, err := os.Create(apkPath)
	if err != nil {
		t.Fatalf("create apk: %v", err)
	}

	w := zip.NewWriter(f)
	mf, err := w.Create(androidManifestPath)
	if err != nil {
		t.Fatalf("create manifest in zip: %v", err)
	}
	if _, err := mf.Write(content); err != nil {
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

// TestExtractAPKMetadata_BinaryManifest is the regression test for
// debt-triage-2026-06-06 C-16. It uses a real Android AXML (binary XML)
// manifest — the format Android uses in compiled APKs since API 1.6.
// The previous text-XML parser would fail on this fixture because
// AXML is not valid XML text (the first bytes are a binary chunk
// header, not `<?xml ...`).
//
// Fixture: internal/game/testdata/AndroidManifest.bin.axml
// (copied from github.com/shogo82148/androidbinary v1.0.5 testdata,
// package "net.sorablue.shogo.FWMeasure", versionCode=1, label=テスト版).
func TestExtractAPKMetadata_BinaryManifest(t *testing.T) {
	apkPath := makeTestAPKFromBinaryManifest(t,
		filepath.Join("testdata", "AndroidManifest.bin.axml"))

	meta, err := ExtractAPKMetadata(apkPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.PackageName != "net.sorablue.shogo.FWMeasure" {
		t.Errorf("PackageName = %q, want %q", meta.PackageName, "net.sorablue.shogo.FWMeasure")
	}
	if meta.VersionCode != 1 {
		t.Errorf("VersionCode = %d, want %d", meta.VersionCode, 1)
	}
	// The fixture's label is a resource reference (`@0x7F040000`),
	// not a literal, and the fixture contains no resources.arsc file.
	// The primary parser (apk.OpenFile) requires resources.arsc to
	// resolve references, so it falls back to the manifest-only parser.
	// That parser sees the raw reference and cannot resolve it either.
	//
	// NEW BEHAVIOUR (post-C-16 follow-up): instead of returning an
	// empty label (which breaks the VRHub client UI), we fall back to
	// the package name when the label is empty or an unresolved
	// resource reference. This ensures every game has a display name.
	if meta.Label != "net.sorablue.shogo.FWMeasure" {
		t.Errorf("Label = %q, want \"net.sorablue.shogo.FWMeasure\" (package-name fallback for unresolved resource ref)", meta.Label)
	}
}

func TestExtractAPKMetadata_NoManifest(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := filepath.Join(tmpDir, "empty.apk")

	f, err := os.Create(apkPath)
	if err != nil {
		t.Fatalf("create apk: %v", err)
	}
	w := zip.NewWriter(f)
	if err := w.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close apk file: %v", err)
	}

	_, err = ExtractAPKMetadata(apkPath)
	if err == nil {
		t.Fatal("expected error for APK without manifest, got nil")
	}
}

func TestExtractAPKMetadata_InvalidAPK(t *testing.T) {
	tmpDir := t.TempDir()
	apkPath := filepath.Join(tmpDir, "invalid.apk")
	if err := os.WriteFile(apkPath, []byte("not a zip file"), 0644); err != nil {
		t.Fatalf("create invalid apk: %v", err)
	}

	_, err := ExtractAPKMetadata(apkPath)
	if err == nil {
		t.Fatal("expected error for invalid APK, got nil")
	}
}

// TestExtractAPKMetadata_BinaryManifest_MalformedAXML verifies that a
// file claiming to be AXML but with truncated content returns a clear
// error (not a panic or zero-value result).
func TestExtractAPKMetadata_BinaryManifest_MalformedAXML(t *testing.T) {
	// 4 bytes is too short to be a valid AXML chunk header.
	apkPath := makeTestAPK(t, "\x03\x00\x00\x00")

	_, err := ExtractAPKMetadata(apkPath)
	if err == nil {
		t.Fatal("expected error for malformed AXML, got nil")
	}
}
