package game

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shogo82148/androidbinary"
	"github.com/shogo82148/androidbinary/apk"
)

const androidManifestPath = "AndroidManifest.xml"

// APKMetadata holds extracted metadata from an APK file.
type APKMetadata struct {
	PackageName string
	VersionCode int64
	Label       string
}

// ExtractAPKMetadata reads an APK file and extracts package name, version code, and label.
//
// Primary path: uses github.com/shogo82148/androidbinary/apk.OpenFile which
// transparently decodes AXML AND resolves resource references (e.g.
// @string/app_name) by reading the embedded resources.arsc file.
//
// Fallback path: some APKs (especially test fixtures or stripped APKs) may
// not contain a resources.arsc file. In that case we fall back to manual
// AXML decoding without resource resolution. Literal labels are returned
// as-is; resource references remain unresolved in the fallback path.
func ExtractAPKMetadata(apkPath string) (APKMetadata, error) {
	meta, err := extractViaAPKLib(apkPath)
	if err == nil {
		return meta, nil
	}

	// If the APK library fails (typically because resources.arsc is missing),
	// fall back to the lightweight manifest-only parser.
	if strings.Contains(err.Error(), "resources.arsc") {
		return extractViaManifestOnly(apkPath)
	}

	return APKMetadata{}, err
}

// extractViaAPKLib uses the full androidbinary/apk library including
// resources.arsc resolution.
func extractViaAPKLib(apkPath string) (APKMetadata, error) {
	pkg, err := apk.OpenFile(apkPath)
	if err != nil {
		return APKMetadata{}, err
	}
	defer pkg.Close()

	meta := APKMetadata{}

	// Package
	meta.PackageName = pkg.PackageName()

	// VersionCode
	if vc, err := pkg.Manifest().VersionCode.Int32(); err == nil {
		meta.VersionCode = int64(vc)
	}

	// Label — automatically resolves @string/... references via resources.arsc.
	// Pass nil ResTableConfig to use the default locale.
	if label, err := pkg.Label(nil); err == nil {
		meta.Label = label
	}

	// Fallback: if the label is still empty, use the package name so the
	// game is at least identifiable in the VRHub client UI.
	if meta.Label == "" && meta.PackageName != "" {
		meta.Label = meta.PackageName
	}

	return meta, nil
}

// extractViaManifestOnly is the fallback parser that only reads
// AndroidManifest.xml without requiring resources.arsc.
func extractViaManifestOnly(apkPath string) (APKMetadata, error) {
	r, err := zip.OpenReader(apkPath)
	if err != nil {
		return APKMetadata{}, fmt.Errorf("open apk zip: %w", err)
	}
	defer r.Close()

	manifest, err := findManifest(r)
	if err != nil {
		return APKMetadata{}, fmt.Errorf("find manifest: %w", err)
	}

	content, err := readManifestContent(manifest)
	if err != nil {
		return APKMetadata{}, fmt.Errorf("read manifest: %w", err)
	}

	xmlFile, err := androidbinary.NewXMLFile(bytes.NewReader(content))
	if err != nil {
		return APKMetadata{}, fmt.Errorf("parse AXML manifest: %w", err)
	}

	var v apk.Manifest
	dec := xml.NewDecoder(xmlFile.Reader())
	if err := dec.Decode(&v); err != nil {
		return APKMetadata{}, fmt.Errorf("decode manifest: %w", err)
	}

	meta := APKMetadata{}

	if pkg, err := v.Package.String(); err == nil {
		meta.PackageName = pkg
	}
	if vc, err := v.VersionCode.Int32(); err == nil {
		meta.VersionCode = int64(vc)
	}
	if label, err := v.App.Label.String(); err == nil {
		meta.Label = label
	}

	// Fallback: if the label is empty or is a raw resource reference,
	// use the package name as display name.
	if meta.Label == "" || strings.HasPrefix(meta.Label, "@") {
		if meta.PackageName != "" {
			meta.Label = meta.PackageName
		}
	}

	return meta, nil
}

// findManifest locates AndroidManifest.xml in the ZIP archive.
func findManifest(r *zip.ReadCloser) (*zip.File, error) {
	for _, f := range r.File {
		if strings.EqualFold(f.Name, androidManifestPath) {
			return f, nil
		}
	}
	return nil, fmt.Errorf("%s not found in apk", androidManifestPath)
}

// readManifestContent reads the raw bytes of the manifest file from a ZIP archive.
func readManifestContent(manifest *zip.File) ([]byte, error) {
	rc, err := manifest.Open()
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer rc.Close()

	content, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	return content, nil
}

// ExtractAPKIcon extracts the application icon from an APK and saves it as a
// PNG file at the given destination path.  The icon is retrieved through the
// androidbinary/apk library which resolves resource references via
// resources.arsc, so it works for real APKs where the manifest stores
// @mipmap/ic_launcher rather than a literal path.
func ExtractAPKIcon(apkPath, destPath string) error {
	pkg, err := apk.OpenFile(apkPath)
	if err != nil {
		return fmt.Errorf("open apk for icon extraction: %w", err)
	}
	defer pkg.Close()

	iconImg, err := pkg.Icon(nil)
	if err != nil {
		return fmt.Errorf("extract icon: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("create icon dir: %w", err)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create icon file: %w", err)
	}
	defer f.Close()

	if err := png.Encode(f, iconImg); err != nil {
		return fmt.Errorf("encode icon png: %w", err)
	}

	return nil
}
