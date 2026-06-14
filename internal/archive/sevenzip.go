package archive

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	embed7zz "github.com/LeGeRyChEeSe/vrhub-server/internal/archive/embed/7zz"
	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
)

const (
	sevenZipFallbackURL = "https://github.com/ip7z/7zip/releases/download/26.01/"
	sevenZipVersion     = "26.01"
)

// androidLibDir is the directory containing the bundled libc++_shared.so
// extracted alongside the Android 7zz binary. The generator points
// LD_LIBRARY_PATH at it so the dynamically-linked 7zz resolves its libc++
// on stock Android. Set by sevenZipBinaryPath; read via GetAndroidLibDir.
var androidLibDir string

// GetAndroidLibDir returns the absolute directory holding the bundled
// libc++_shared.so for Android, or "" if not extracted.
func GetAndroidLibDir() string {
	return androidLibDir
}

// ensureAndroidLibCxx extracts the bundled libc++_shared.so into
// {dataDir}/bin next to the 7zz binary (idempotent). Returns the absolute
// directory so the caller can set LD_LIBRARY_PATH.
func ensureAndroidLibCxx(dataDir string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("dataDir required for libc++ extraction")
	}
	binDir := filepath.Join(dataDir, "bin")
	if abs, err := filepath.Abs(binDir); err == nil {
		binDir = abs
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir bin: %w", err)
	}
	data, err := embed7zz.ReadAndroidLibCxx()
	if err != nil || data == nil {
		return "", fmt.Errorf("read embedded libc++: %w", err)
	}
	libPath := filepath.Join(binDir, "libc++_shared.so")
	// Idempotent: skip the write if an identically-sized copy is already
	// present (avoids rewriting ~1.3 MB on every meta.7z request).
	if fi, statErr := os.Stat(libPath); statErr == nil && fi.Size() == int64(len(data)) {
		return binDir, nil
	}
	if err := os.WriteFile(libPath, data, 0o644); err != nil {
		return "", fmt.Errorf("write libc++: %w", err)
	}
	return binDir, nil
}

// isAndroid returns true when the runtime appears to be Android (e.g. Termux).
// Detection heuristics: /proc/version contains "android", or the TERMUX_PREFIX
// environment variable is set, or we're on linux/arm64 with bionic libc markers.
func isAndroid() bool {
	// Fast path: check for Termux environment variable.
	if os.Getenv("TERMUX_PREFIX") != "" || os.Getenv("PREFIX") == "/data/data/com.termux/files/usr" {
		return true
	}

	// Medium path: read /proc/version for Android kernel marker.
	data, err := os.ReadFile("/proc/version")
	if err == nil && strings.Contains(string(data), "android") {
		return true
	}

	// Slow path: check if we're on linux/arm64 and the system libc is bionic.
	if runtime.GOOS == "linux" && (runtime.GOARCH == "arm64" || runtime.GOARCH == "amd64") {
		// Attempt to read /system/lib64/libc.so — its presence indicates Android.
		if _, err := os.Stat("/system/bin/linker64"); err == nil {
			return true
		}
	}

	return false
}

// sevenZipBinaryPath returns the absolute path to a 7z/7zz binary that
// can create AES-256 encrypted 7z archives. Resolution order:
//  1. Embedded bionic-compatible binary (Android/Termux only)
//  2. System PATH (7zz or 7z) — standard Linux/macOS/Windows
//  3. {dataDir}/bin/7zz (cached from previous extraction/download)
//  4. Embedded glibc binary extracted to {dataDir}/bin/7zz
//  5. Download from GitHub releases to {dataDir}/bin/7zz
//
// If no binary can be resolved, it returns an error. The caller
// (GenerateMeta7z) treats this as a fatal condition per Story 9.8 D4.
func sevenZipBinaryPath(ctx context.Context, dataDir string) (string, error) {
	// On Android/Termux, prefer the bionic-compatible embedded binary first.
	if isAndroid() {
		if path, err := tryEmbeddedOrCache(dataDir, "linux/android"); err == nil && path != "" {
			// Extract the bundled libc++_shared.so next to the binary and
			// remember its directory so the generator can set
			// LD_LIBRARY_PATH. The Android 7zz is a modern bionic build of
			// 7-Zip 26.01 dynamically linked against Termux's libc++; stock
			// Android ships a differently-named platform libc++, so we
			// supply our own. (F11: the previous embedded binary was an old
			// p7zip 17.05 that SIGABRT/SIGSEGV'd on modern ARMv9 + MTE/PAC
			// devices like the Galaxy S24+.)
			if libDir, lerr := ensureAndroidLibCxx(dataDir); lerr == nil {
				androidLibDir = libDir
			} else {
				vlog.Get().Warn().Err(lerr).Msg("android: failed to extract bundled libc++ (continuing; binary may resolve a system libc++)")
			}
			return path, nil
		}
		vlog.Get().Debug().Msg("android: no embedded bionic binary found, falling back to PATH")
	}

	// 2. Try PATH lookup (works on standard Linux/macOS/Windows).
	for _, name := range []string{"7zz", "7z"} {
		if path, err := exec.LookPath(name); err == nil && path != "" {
			return path, nil
		}
	}

	// If dataDir is empty, use a temp directory for one-shot extraction.
	cleanup := false
	if dataDir == "" {
		tmp, err := os.MkdirTemp("", "vrhub-7zz-*")
		if err != nil {
			return "", fmt.Errorf("create temp dir for 7zz: %w", err)
		}
		dataDir = tmp
		cleanup = true
	}

	// 3. Try cached binary in dataDir.
	if path, err := tryEmbeddedOrCache(dataDir, runtime.GOOS+"/"+runtime.GOARCH); err == nil && path != "" {
		return path, nil
	}

	// 4. Fallback download from GitHub (Windows only).
	osArch := runtime.GOOS + "/" + runtime.GOARCH
	path, err := downloadFallback(ctx, dataDir, osArch)
	if err == nil {
		return path, nil
	}

	if cleanup {
		os.RemoveAll(dataDir)
	}

	return "", fmt.Errorf(
		"FATAL: 7zz binary not available for this OS (OS=%s, arch=%s) and download failed: %w. "+
			"The meta.7z endpoint requires AES-256 encryption which depends on 7zz. "+
			"Please report this issue at https://github.com/LeGeRyChEeSe/vrhub-server/issues",
		runtime.GOOS, runtime.GOARCH, err,
	)
}

// tryEmbeddedOrCache checks for a cached binary first, then falls back to
// extracting the embedded binary for the given os/arch key. Returns "" on miss.
func tryEmbeddedOrCache(dataDir string, osArch string) (string, error) {
	if dataDir == "" {
		return "", nil
	}

	// 1. Try cached binary in dataDir.
	cached := filepath.Join(dataDir, "bin", "7zz"+exeSuffix())
	if valid, _ := verifyBinary(cached, osArch); valid {
		return cached, nil
	}

	// 2. Try embedded binary.
	embedded, err := embed7zz.ReadBinary(osArch)
	if err == nil && embedded != nil {
		path, err := extractEmbedded(dataDir, embedded, osArch)
		if err == nil {
			return path, nil
		}
	}

	return "", nil
}

// cachedBinaryPath returns the path where a previously-extracted or
// downloaded binary would live.
func cachedBinaryPath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "bin", "7zz"+exeSuffix())
}

// extractEmbedded writes the embedded bytes to disk, verifies SHA-256,
// and makes the file executable.
func extractEmbedded(dataDir string, data []byte, osArch string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("dataDir required for embedded extraction")
	}
	binDir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir bin: %w", err)
	}
	path := filepath.Join(binDir, "7zz"+exeSuffix())

	// Verify SHA-256 before writing. Only fails if a checksum is defined and mismatches.
	expected := embed7zz.ExpectedSHA256[osArch]
	if expected != "" {
		sum := fmt.Sprintf("%X", sha256.Sum256(data))
		if !strings.EqualFold(sum, expected) {
			return "", fmt.Errorf("embedded binary SHA-256 mismatch for %s (got %s)", osArch, sum)
		}
	}

	if err := os.WriteFile(path, data, 0o700); err != nil {
		return "", fmt.Errorf("write embedded binary: %w", err)
	}
	return path, nil
}

// downloadFallback downloads the appropriate 7zz binary from GitHub
// releases when the platform is not covered by embedded binaries.
func downloadFallback(ctx context.Context, dataDir string, osArch string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("dataDir required for download fallback")
	}

	url, err := fallbackURL(osArch)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024)) // 50 MiB cap
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	binDir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir bin: %w", err)
	}
	path := filepath.Join(binDir, "7zz"+exeSuffix())
	if err := os.WriteFile(path, data, 0o700); err != nil {
		return "", fmt.Errorf("write downloaded binary: %w", err)
	}
	return path, nil
}

// fallbackURL returns the GitHub release asset URL for the given os/arch.
// Only Windows is supported for download fallback because all supported
// Linux and macOS platforms are covered by embedded binaries.
func fallbackURL(osArch string) (string, error) {
	switch osArch {
	case "windows/amd64", "windows/arm64":
		// 7zr.exe is a minimal standalone console that supports 7z AES-256.
		return sevenZipFallbackURL + "7zr.exe", nil
	default:
		return "", fmt.Errorf("unsupported platform %s (no embed coverage and no download URL)", osArch)
	}
}

// verifyBinary checks that the file at path matches the expected SHA-256.
func verifyBinary(path string, osArch string) (bool, error) {
	expected := embed7zz.ExpectedSHA256[osArch]
	if expected == "" {
		return true, nil // no expectation for this platform key — trust on first use
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	sum := fmt.Sprintf("%X", sha256.Sum256(data))
	return strings.EqualFold(sum, expected), nil
}

// exeSuffix returns ".exe" on Windows, "" elsewhere.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// sevenZipExeSuffix is the old name kept for backward compatibility
// within this package. Prefer exeSuffix().
func sevenZipExeSuffix() string {
	return exeSuffix()
}
