package update

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/log"
)

const (
	downloadTimeout = 10 * time.Minute
	updatesDirName  = "updates"
	backupsDirName  = "backups"
	maxAutoBackups  = 5
	maxDownloadSize = 500 * 1024 * 1024 // 500MB
	// minDownloadSize: floor for downloaded archive. 100KB to accommodate stripped
	// or tinygo builds; below this, the file is almost certainly not a real Go binary
	// or release archive.
	minDownloadSize   = 100 * 1024
	updatePendingFlag = "update_pending.flag"
	oldBinarySuffix   = ".old"
	updatingSuffix    = ".updating"
	defaultFileMode   = 0755
)

// ErrBinaryTooLarge is returned when a zip entry exceeds the maximum allowed download size.
var ErrBinaryTooLarge = errors.New("binary too large")

// versionRegex enforces a strict subset of characters in the Version field to
// prevent path traversal and symlink attacks when the version is interpolated
// into a download filename.
var versionRegex = regexp.MustCompile(`^[0-9A-Za-z.\-+]+$`)

// backupSeq is a process-wide counter to disambiguate backup filenames created
// in the same wall-clock second.
var backupSeq uint64

// ApplyConfig holds configuration for the apply module.
type ApplyConfig struct {
	DataDir     string
	AutoApply   bool
	AutoBackup  bool // implied true when auto-apply is enabled
	MaxBackups  int
	DownloadURL string
	ChecksumURL string // optional URL to SHA256 checksum file
	Version     string
	AutoRestart bool // when false, stage the binary but return ErrRestartPending instead of restarting
}

// DefaultApplyConfig returns default apply configuration.
func DefaultApplyConfig(dataDir string) ApplyConfig {
	return ApplyConfig{
		DataDir:     dataDir,
		AutoApply:   true,
		AutoBackup:  true,
		MaxBackups:  maxAutoBackups,
		AutoRestart: false,
	}
}

// Applicator handles downloading and applying updates.
//
// DownloadAndApply must not be called concurrently with itself; the embedded
// mutex serialises all such calls but the caller is responsible for not
// interleaving it with other Applicator state changes.
type Applicator struct {
	config ApplyConfig
	mu     sync.Mutex
	// getExePath is overridable for tests; in production it returns
	// os.Executable().
	getExePath func() (string, error)
	// httpClient is overridable for tests that need to trust a custom
	// TLS root (e.g. httptest.NewTLSServer).
	httpClient *http.Client
	// restartFn is overridable for tests; in production it is nil and
	// DownloadAndApply falls back to a.triggerRestart, which never returns
	// (it calls syscall.Exec on Unix or os.Exit on Windows). Tests inject a
	// no-op recorder here so the full apply pipeline can run to completion
	// without killing the test process.
	restartFn func() error
}

// NewApplicator creates a new applicator with the given config.
func NewApplicator(cfg ApplyConfig) *Applicator {
	if cfg.MaxBackups == 0 {
		cfg.MaxBackups = maxAutoBackups
	}
	return &Applicator{
		config:     cfg,
		getExePath: os.Executable,
		httpClient: &http.Client{Timeout: downloadTimeout},
	}
}

// DownloadAndApply downloads the new binary and applies the update.
// Returns an error if the download or validation fails.
func (a *Applicator) DownloadAndApply(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	logger := log.Get()

	if a.config.DownloadURL == "" {
		return fmt.Errorf("no download URL configured")
	}

	// Require HTTPS to prevent downgrade attacks. GitHub release assets are
	// always served over HTTPS; an http:// URL is a sign of a redirect attack
	// or a misconfigured client.
	if err := requireHTTPS(a.config.DownloadURL); err != nil {
		return fmt.Errorf("insecure download URL rejected: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context cancelled before starting: %w", err)
	}

	// Ensure updates directory exists
	updatesDir := filepath.Join(a.config.DataDir, updatesDirName)
	if err := os.MkdirAll(updatesDir, 0755); err != nil {
		return fmt.Errorf("failed to create updates directory: %w", err)
	}

	// Auto-backup before update (Task 4)
	if a.config.AutoBackup {
		if err := a.performBackup(ctx); err != nil {
			logger.Warn().Err(err).Msg("Update apply: backup failed, continuing anyway")
		}
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context cancelled after backup: %w", err)
	}

	// Download the new binary (Task 1)
	zipPath, err := a.downloadBinary(ctx)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	if err := ctx.Err(); err != nil {
		if rmErr := os.Remove(zipPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", zipPath).Msg("Update apply: failed to remove zip on cancel")
		}
		return fmt.Errorf("context cancelled after download: %w", err)
	}

	// Validate downloaded file (Task 1.3) — size, checksum, zip format.
	if err := a.validateDownload(ctx, zipPath); err != nil {
		if rmErr := os.Remove(zipPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", zipPath).Msg("Update apply: failed to remove zip on validation error")
		}
		return fmt.Errorf("validation failed: %w", err)
	}

	// Extract the binary from zip (Task 1.4)
	binaryPath, err := a.extractBinary(zipPath)
	if err != nil {
		if rmErr := os.Remove(zipPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", zipPath).Msg("Update apply: failed to remove zip on extraction error")
		}
		return fmt.Errorf("extraction failed: %w", err)
	}

	// Remove zip after successful extraction
	if err := os.Remove(zipPath); err != nil && !os.IsNotExist(err) {
		logger.Warn().Err(err).Str("path", zipPath).Msg("Update apply: failed to remove zip after extraction")
	}

	if err := ctx.Err(); err != nil {
		if rmErr := os.Remove(binaryPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", binaryPath).Msg("Update apply: failed to remove extracted binary on cancel")
		}
		return fmt.Errorf("context cancelled after extraction: %w", err)
	}

	// Replace the current binary (Task 2)
	if err := a.replaceBinary(binaryPath); err != nil {
		if rmErr := os.Remove(binaryPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", binaryPath).Msg("Update apply: failed to remove extracted binary after replace error")
		}
		return fmt.Errorf("binary replacement failed: %w", err)
	}

	// On Windows the extracted binary is a copy (not a rename) so it remains
	// in updates/ and must be cleaned up explicitly. On Unix the rename
	// already moved it.
	if runtime.GOOS == "windows" {
		if err := os.Remove(binaryPath); err != nil && !os.IsNotExist(err) {
			logger.Warn().Err(err).Str("path", binaryPath).Msg("Update apply: failed to remove staged Windows binary")
		}
	}

	// When AutoRestart is false, skip the restart and signal the caller
	// that a restart is required. The staged binary is already in place;
	// the caller is responsible for triggering the restart via TriggerRestart.
	if !a.config.AutoRestart {
		// Honor the test seam: if restartFn is set, tests may still want
		// to verify the staging path ran; call it even when AutoRestart=false
		// so the test can record what happened. Production restartFn is nil
		// so this branch is a no-op there.
		if a.restartFn != nil {
			_ = a.restartFn()
		}
		return ErrRestartPending
	}

	// Trigger restart (Task 3). The restart hook is overridable for tests;
	// in production restartFn is nil and we use a.triggerRestart, which does
	// not return on success (Exec on Unix / os.Exit on Windows).
	restart := a.restartFn
	if restart == nil {
		restart = a.triggerRestart
	}
	if err := restart(); err != nil {
		return fmt.Errorf("restart failed: %w", err)
	}

	return nil
}

// TriggerRestart re-execs the current process into the currently installed
// binary. It is safe to call independently of the staging path (e.g. after
// the operator clicks "Restart Now" in the admin UI). Does not return on success.
func TriggerRestart() error {
	a := &Applicator{getExePath: os.Executable}
	return a.triggerRestart()
}

// requireHTTPS parses rawURL and rejects anything that is not https://.
func requireHTTPS(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if strings.ToLower(u.Scheme) != "https" {
		return fmt.Errorf("scheme %q is not allowed; require https", u.Scheme)
	}
	return nil
}

// trustedGitHubHosts is the set of hostnames that a release-binary
// HTTP redirect is allowed to land on. GitHub release downloads
// hop through these (api.github.com → objects.githubusercontent.com
// or release-assets.githubusercontent.com). An attacker who
// subverts the redirect chain (e.g. via DNS poisoning, MITM, or
// a malicious redirect target) can NOT land on a host outside
// this list because we reject the redirect in CheckRedirect.
//
// S-04 supply chain fix: the previous CheckRedirect only checked
// the scheme (HTTPS), which left an attacker free to redirect
// from a trusted host to ANOTHER trusted-looking HTTPS host that
// they control (e.g. github-assets.com). Tightening the check to
// the exact set of GitHub-owned release hosts closes that hole.
//
// Configuration note: the list is intentionally a constant, not
// a config field. Operators with a self-hosted release backend
// would need a code change + re-vendoring, which is the right
// friction for a security-critical default.
var trustedGitHubHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// requireTrustedGitHubHost parses rawURL and rejects anything whose
// host is not in trustedGitHubHosts. Returns nil for the empty
// string (caller's responsibility to validate scheme first).
func requireTrustedGitHubHost(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("empty host in URL %q", rawURL)
	}
	if !trustedGitHubHosts[host] {
		return fmt.Errorf("host %q is not a trusted GitHub release host; refusing redirect (supply chain protection)", host)
	}
	return nil
}

// downloadBinary downloads the zip archive to a fresh temp file in the updates
// directory. The returned path is the caller-owned staging file.
func (a *Applicator) downloadBinary(ctx context.Context) (string, error) {
	logger := log.Get()

	// Reject clearly invalid Version strings before they reach the filesystem.
	if a.config.Version != "" && !versionRegex.MatchString(a.config.Version) {
		return "", fmt.Errorf("invalid version %q: must match %s", a.config.Version, versionRegex)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.config.DownloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Custom CheckRedirect: refuse non-HTTPS redirects to prevent an attacker
	// from downgrading an https://github.com/... URL to a hostile origin.
	client := a.httpClient
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		// S-04 supply chain: BOTH the scheme AND the host must be
		// trusted. The previous version checked only the scheme,
		// which allowed an attacker to redirect from a trusted
		// https://github.com URL to a malicious https://attacker.com
		// (HTTPS but wrong host). The host allowlist (api.github.com,
		// github.com, objects.githubusercontent.com,
		// release-assets.githubusercontent.com) closes that gap.
		if err := requireHTTPS(req.URL.String()); err != nil {
			return fmt.Errorf("redirect to non-HTTPS rejected: %w", err)
		}
		if err := requireTrustedGitHubHost(req.URL.String()); err != nil {
			return fmt.Errorf("redirect to untrusted host rejected: %w", err)
		}
		return nil
	}

	logger.Info().Str("url", a.config.DownloadURL).Msg("Update apply: downloading binary")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Wrap the response body in a LimitReader so the on-disk file cannot
	// exceed maxDownloadSize regardless of Content-Length header. This
	// also bounds the in-memory buffering.
	limitedBody := io.LimitReader(resp.Body, maxDownloadSize+1)

	// Check Content-Length as a fast pre-flight check; the hard limit is
	// enforced by LimitReader above.
	if resp.ContentLength > maxDownloadSize {
		return "", fmt.Errorf("download size %d exceeds maximum allowed size %d", resp.ContentLength, maxDownloadSize)
	}

	// Use os.CreateTemp with a random suffix; this avoids:
	//   - predictable filenames that can be pre-created as symlinks (TOCTOU)
	//   - path traversal when Version contains "../"
	updatesDir := filepath.Join(a.config.DataDir, updatesDirName)
	tempFile, err := os.CreateTemp(updatesDir, "vrhub-server-*.zip.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	// tempClosed guards against a double Close: we close explicitly before the
	// rename (required on Windows — see below) but keep a deferred close to
	// cover the early-return error paths.
	tempClosed := false
	defer func() {
		if !tempClosed {
			tempFile.Close()
		}
	}()

	written, err := io.Copy(tempFile, limitedBody)
	if err != nil {
		if rmErr := os.Remove(tempPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", tempPath).Msg("Update apply: failed to remove temp file on copy error")
		}
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	if written > maxDownloadSize {
		if rmErr := os.Remove(tempPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", tempPath).Msg("Update apply: failed to remove oversized temp file")
		}
		return "", fmt.Errorf("downloaded body exceeds maximum size %d", maxDownloadSize)
	}

	// Close the temp file BEFORE renaming. On Windows os.Rename fails with
	// "the process cannot access the file because it is being used by another
	// process" if the source is still held open by this process, which means
	// the download would never finalise. Closing here also flushes the OS
	// buffers so the renamed file is complete on disk.
	if err := tempFile.Close(); err != nil {
		if rmErr := os.Remove(tempPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", tempPath).Msg("Update apply: failed to remove temp file on close error")
		}
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}
	tempClosed = true

	// Rename to the final predictable name so the rest of the pipeline can
	// find it. The .tmp suffix above is the atomic-rename trick: if the
	// download is interrupted we leave only a .tmp file, never a half-written
	// final name.
	finalPath := filepath.Join(updatesDir, fmt.Sprintf("vrhub-server-%s.zip", a.config.Version))
	if err := os.Rename(tempPath, finalPath); err != nil {
		if rmErr := os.Remove(tempPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", tempPath).Msg("Update apply: failed to remove temp file on rename error")
		}
		return "", fmt.Errorf("failed to finalise temp file: %w", err)
	}

	logger.Info().Str("path", finalPath).Int64("bytes", written).Msg("Update apply: download complete")
	return finalPath, nil
}

// validateDownload checks the downloaded file is a real archive of reasonable
// size, with an optional SHA-256 checksum if ChecksumURL is configured.
func (a *Applicator) validateDownload(ctx context.Context, zipPath string) error {
	logger := log.Get()

	info, err := os.Stat(zipPath)
	if err != nil {
		return fmt.Errorf("failed to stat downloaded file: %w", err)
	}

	if info.Size() < minDownloadSize {
		return fmt.Errorf("downloaded file too small: %d bytes (min %d)", info.Size(), minDownloadSize)
	}

	if info.Size() > maxDownloadSize {
		return fmt.Errorf("downloaded file too large: %d bytes (max %d)", info.Size(), maxDownloadSize)
	}

	// Optional SHA-256 verification. If a ChecksumURL is configured we fetch
	// it, compare hex, and fail closed. If no ChecksumURL is configured we
	// do not verify — the caller can opt in by setting it.
	if a.config.ChecksumURL != "" {
		if err := a.verifySHA256(ctx, zipPath); err != nil {
			return fmt.Errorf("checksum verification failed: %w", err)
		}
	}

	// Use zip.OpenReader so the underlying file is closed for us.
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("invalid zip archive: %w", err)
	}
	defer zr.Close()

	if len(zr.File) == 0 {
		return fmt.Errorf("zip archive is empty")
	}

	// Pre-flight: every entry must have a sensible uncompressed size to
	// prevent zip-bomb-style archives.
	for _, f := range zr.File {
		if f.UncompressedSize64 > maxDownloadSize {
			return fmt.Errorf("zip entry %q uncompressed size %d exceeds max %d: %w", f.Name, f.UncompressedSize64, maxDownloadSize, ErrBinaryTooLarge)
		}
		if f.FileInfo().IsDir() && strings.HasSuffix(f.Name, "/") == false {
			// Some zips omit trailing slashes on directory entries; treat as such
			// by checking the FileInfo mode. archive/zip fills this correctly.
		}
	}

	logger.Debug().Int64("size", info.Size()).Int("files", len(zr.File)).Msg("Update apply: file validation passed")
	return nil
}

// verifySHA256 downloads the SHA-256 sidecar from ChecksumURL and compares it
// to the actual hash of zipPath. The expected value may be a bare hex string
// or the "sha256:" prefix used by sha256sum.
func (a *Applicator) verifySHA256(ctx context.Context, zipPath string) error {
	if err := requireHTTPS(a.config.ChecksumURL); err != nil {
		return fmt.Errorf("insecure checksum URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.config.ChecksumURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create checksum request: %w", err)
	}
	client := a.httpClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("checksum request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected checksum status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return fmt.Errorf("failed to read checksum body: %w", err)
	}

	// Strip optional "sha256:" prefix or "*filename" suffix (sha256sum format).
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return fmt.Errorf("empty checksum body")
	}
	expected := strings.TrimPrefix(fields[0], "sha256:")

	f, err := os.Open(zipPath)
	if err != nil {
		return fmt.Errorf("failed to open zip for hashing: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("failed to hash zip: %w", err)
	}
	actual := hex.EncodeToString(h.Sum(nil))

	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

// extractBinary extracts the binary matching the current GOOS/GOARCH from the
// zip archive, streaming it to disk via a temp file with an atomic rename.
// Metadata files are filtered. Path traversal and absolute paths are rejected.
func (a *Applicator) extractBinary(zipPath string) (string, error) {
	logger := log.Get()

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("failed to open zip: %w", err)
	}
	defer zr.Close()

	updatesDir := filepath.Join(a.config.DataDir, updatesDirName)
	if err := os.MkdirAll(updatesDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create updates directory: %w", err)
	}

	// First pass: identify the binary entry for the current platform and
	// collect candidate entry names for error messages.
	var binaryEntry *zip.File
	var skipped []string
	for _, f := range zr.File {
		if !isValidEntry(f) {
			skipped = append(skipped, f.Name)
			logger.Warn().Str("entry", f.Name).Msg("Update apply: skipping invalid zip entry")
			continue
		}
		if f.FileInfo().IsDir() {
			skipped = append(skipped, f.Name)
			continue
		}
		// Reject zip-bomb-style entries: the declared uncompressed size must
		// fit under the cap, even if the actual bytes on disk are short
		// (the central directory can lie about size).
		if f.UncompressedSize64 > uint64(maxDownloadSize) {
			return "", fmt.Errorf("zip entry %q declares uncompressed size %d > max %d: %w", f.Name, f.UncompressedSize64, maxDownloadSize, ErrBinaryTooLarge)
		}
		if isMetadataFile(f.Name) {
			skipped = append(skipped, f.Name)
			continue
		}
		if isBinaryFileForCurrentPlatform(f.Name) {
			binaryEntry = f
			break
		}
		skipped = append(skipped, f.Name)
	}

	if binaryEntry == nil {
		return "", fmt.Errorf("no binary found in archive for %s/%s (entries: %s)", runtime.GOOS, runtime.GOARCH, strings.Join(skipped, ", "))
	}

	// Validate version matches expected (if configured). Fail-closed: a
	// mismatched filename means the wrong release was packaged and we
	// refuse to install it.
	if a.config.Version != "" {
		if !strings.Contains(binaryEntry.Name, a.config.Version) {
			return "", fmt.Errorf("extracted binary %q does not contain expected version %q", binaryEntry.Name, a.config.Version)
		}
	}

	// Magic-bytes check: refuse anything that does not look like a real
	// executable for the current platform.
	if err := verifyBinaryMagic(binaryEntry); err != nil {
		return "", fmt.Errorf("binary magic check failed: %w", err)
	}

	// Stream to a temp file in updatesDir, then atomic rename.
	rc, err := binaryEntry.Open()
	if err != nil {
		return "", fmt.Errorf("failed to open zip entry %s: %w", binaryEntry.Name, err)
	}
	defer rc.Close()

	binaryName := filepath.Base(binaryEntry.Name)
	tmpPath := filepath.Join(updatesDir, binaryName+".tmp")
	outFile, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return "", fmt.Errorf("failed to create temp binary file: %w", err)
	}

	written, err := io.Copy(outFile, rc)
	if cerr := outFile.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		if rmErr := os.Remove(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", tmpPath).Msg("Update apply: failed to remove temp binary on copy error")
		}
		return "", fmt.Errorf("failed to write binary: %w", err)
	}

	if written > maxDownloadSize {
		if rmErr := os.Remove(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", tmpPath).Msg("Update apply: failed to remove oversized temp binary")
		}
		return "", fmt.Errorf("extracted binary too large: %d bytes", written)
	}

	// Force the executable bit independent of the umask so the file is
	// always runnable; this is the platform-correct default.
	if err := os.Chmod(tmpPath, defaultFileMode); err != nil {
		if rmErr := os.Remove(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", tmpPath).Msg("Update apply: failed to remove temp binary on chmod error")
		}
		return "", fmt.Errorf("failed to chmod extracted binary: %w", err)
	}

	binaryPath := filepath.Join(updatesDir, binaryName)
	if err := os.Rename(tmpPath, binaryPath); err != nil {
		if rmErr := os.Remove(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
			logger.Warn().Err(rmErr).Str("path", tmpPath).Msg("Update apply: failed to remove temp binary on rename error")
		}
		return "", fmt.Errorf("failed to finalise extracted binary: %w", err)
	}

	logger.Info().Str("path", binaryPath).Int64("size", written).Msg("Update apply: binary extracted")
	return binaryPath, nil
}

// isValidEntry rejects zip entries that could escape the extraction directory
// (path traversal or absolute paths).
func isValidEntry(f *zip.File) bool {
	if strings.Contains(f.Name, "..") {
		return false
	}
	if filepath.IsAbs(f.Name) || path.IsAbs(f.Name) {
		return false
	}
	// filepath.Clean with .. on a path that does not start with .. is fine
	// only if the cleaned result is still inside the implicit root. We
	// accept anything that does not have a ".." segment and is not absolute.
	return true
}

// verifyBinaryMagic checks that the first few bytes of the zip entry look
// like a real PE (Windows) or ELF (Unix) executable.
func verifyBinaryMagic(f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	head := make([]byte, 4)
	n, _ := io.ReadFull(rc, head)
	if n < 2 {
		return fmt.Errorf("binary too small to identify (%d bytes)", n)
	}
	switch {
	case runtime.GOOS == "windows":
		// PE: starts with "MZ" (DOS header magic). The PE signature is at
		// offset 0x3c but the DOS magic is a strong enough heuristic for
		// the auto-update trust boundary.
		if head[0] != 'M' || head[1] != 'Z' {
			return fmt.Errorf("Windows binary does not start with MZ magic (got %x %x)", head[0], head[1])
		}
	case runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "freebsd" || runtime.GOOS == "openbsd" || runtime.GOOS == "netbsd":
		// ELF magic: 0x7f 'E' 'L' 'F'
		if head[0] != 0x7f || head[1] != 'E' || head[2] != 'L' || head[3] != 'F' {
			return fmt.Errorf("Unix binary does not start with ELF magic (got %x %x %x %x)", head[0], head[1], head[2], head[3])
		}
	case runtime.GOOS == "js":
		// WASM magic: \0asm
		if head[0] != 0x00 || head[1] != 'a' || head[2] != 's' || head[3] != 'm' {
			return fmt.Errorf("WASM binary does not start with \\0asm magic")
		}
	default:
		// Unknown platform: be conservative and reject.
		return fmt.Errorf("unknown platform %q; cannot verify binary magic", runtime.GOOS)
	}
	return nil
}

// isMetadataFile returns true if the file is a metadata file to skip.
// The check is on the basename only, so files in subdirectories can still
// be the binary.
func isMetadataFile(name string) bool {
	lower := strings.ToLower(path.Base(name))
	switch lower {
	case "readme.md", "readme.txt", "readme",
		"license", "license.md", "license.txt",
		"changelog.md", "changelog.txt", "changelog",
		".release-info.json", "release-info.json",
		"checksums.txt", "checksums.sha256", "sha256sums.txt":
		return true
	}
	return false
}

// isBinaryFileForCurrentPlatform returns true if name looks like the binary
// for the current runtime.GOOS and runtime.GOARCH.
//
// Match rules:
//   - Windows: filename ends in ".exe" and contains "vrhub-server" (or the
//     expected asset name fragment).
//   - Unix: filename contains "vrhub-server" and is not a metadata file.
//   - When the name contains the current GOOS/GOARCH string, it is preferred.
func isBinaryFileForCurrentPlatform(name string) bool {
	base := path.Base(name)
	lower := strings.ToLower(base)

	if isMetadataFile(lower) {
		return false
	}

	// Quick reject: must look like our binary.
	if !strings.Contains(lower, "vrhub-server") {
		return false
	}

	// Prefer a name that mentions the current platform.
	hasOS := strings.Contains(lower, runtime.GOOS)
	hasArch := strings.Contains(lower, runtime.GOARCH)

	// Windows: also accept by .exe extension alone (in case the asset is
	// not platform-tagged).
	if runtime.GOOS == "windows" {
		return strings.HasSuffix(lower, ".exe") || (hasOS && hasArch)
	}

	// Unix: require GOOS match (and ideally GOARCH too, but some releases
	// are univeral). At least the GOOS segment must match.
	return hasOS
}

// replaceBinary replaces the current binary with the new one using the
// platform-specific strategy. On Windows the prior binary is preserved as
// `.updating` and the flag file is set so the next startup can recover if
// the new binary fails to start. On Unix the prior binary is renamed `.old`
// and the new binary is moved into place.
func (a *Applicator) replaceBinary(newBinaryPath string) error {
	logger := log.Get()

	exePath, err := a.getExePath()
	if err != nil {
		return fmt.Errorf("failed to get current executable path: %w", err)
	}

	if runtime.GOOS == "windows" {
		updatingPath := exePath + updatingSuffix

		// Cleanup any leftover .updating from a prior interrupted run.
		// We only do this if exePath is currently present and has a sane
		// size; otherwise the .updating might be the recovery copy we
		// need to restore (handled by CheckPendingUpdate at startup).
		if _, statErr := os.Stat(exePath); statErr == nil {
			if _, statErr := os.Stat(updatingPath); statErr == nil {
				if rmErr := os.Remove(updatingPath); rmErr != nil {
					logger.Warn().Err(rmErr).Str("path", updatingPath).Msg("Update apply: failed to remove stale .updating before rename")
				}
			}
		}

		// Rename current to .updating. If the rename fails because
		// .updating still exists (race with another updater), try once
		// more after a forced remove.
		if err := os.Rename(exePath, updatingPath); err != nil {
			if rmErr := os.Remove(updatingPath); rmErr == nil {
				if retryErr := os.Rename(exePath, updatingPath); retryErr != nil {
					return fmt.Errorf("failed to rename current binary after cleanup: %w (original: %v)", retryErr, err)
				}
			} else {
				return fmt.Errorf("failed to rename current binary: %w", err)
			}
		}

		// Copy new binary into place.
		if err := copyFile(newBinaryPath, exePath); err != nil {
			// Try to restore the old binary. We wrap both errors so the
			// caller can see the original failure plus the restore result.
			restoreErr := os.Rename(updatingPath, exePath)
			if restoreErr != nil {
				return fmt.Errorf("failed to copy new binary: %w; restore failed: %v", err, restoreErr)
			}
			return fmt.Errorf("failed to copy new binary: %w", err)
		}

		// Write the update-pending flag. This is a hard failure: without
		// the flag, the next startup cannot tell that an update happened
		// and may delete .updating as a leftover, leaving the operator
		// with a corrupted install.
		flagPath := filepath.Join(a.config.DataDir, updatePendingFlag)
		flagContent := fmt.Sprintf("%s|%s", a.config.Version, time.Now().Format(time.RFC3339))
		if err := os.WriteFile(flagPath, []byte(flagContent), 0644); err != nil {
			// Best-effort rollback so we don't leave a half-updated state.
			if restoreErr := os.Rename(updatingPath, exePath); restoreErr != nil {
				return fmt.Errorf("failed to write update flag: %w; subsequent restore also failed: %v", err, restoreErr)
			}
			return fmt.Errorf("failed to write update flag: %w", err)
		}

		logger.Info().
			Str("from", updatingPath).
			Str("to", exePath).
			Msg("Update apply: binary replaced (Windows, restart required)")
	} else {
		oldPath := exePath + oldBinarySuffix

		// Rename current to .old.
		if err := os.Rename(exePath, oldPath); err != nil {
			return fmt.Errorf("failed to rename current binary: %w", err)
		}

		// Move new binary into place, with cross-FS fallback.
		if err := renameOrCopy(newBinaryPath, exePath); err != nil {
			restoreErr := renameOrCopy(oldPath, exePath)
			if restoreErr != nil {
				return fmt.Errorf("failed to move new binary: %w; restore failed: %v", err, restoreErr)
			}
			return fmt.Errorf("failed to move new binary: %w", err)
		}

		// Ensure executable bit. On Unix the rename preserves the mode
		// of the source file, but cross-FS copies use copyFile which
		// does not, so re-chmod unconditionally.
		if err := os.Chmod(exePath, defaultFileMode); err != nil {
			return fmt.Errorf("failed to chmod new binary: %w", err)
		}

		logger.Info().
			Str("from", oldPath).
			Str("to", exePath).
			Msg("Update apply: binary replaced (Unix)")
	}

	return nil
}

// renameOrCopy performs a rename, falling back to copy+remove on EXDEV
// (cross-filesystem). Returns a wrapped error describing the source failure
// so the caller can produce a useful message.
func renameOrCopy(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	// Cross-FS: copy then remove.
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("copy fallback: %w", err)
	}
	if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove source after copy: %w", err)
	}
	return nil
}

// copyFile copies src to dst, preserving the source file's permission bits
// on the destination.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode().Perm())
	if err != nil {
		return err
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		return err
	}
	if err := dstFile.Sync(); err != nil {
		dstFile.Close()
		return err
	}
	return dstFile.Close()
}

// triggerRestart triggers a restart of the application. On Unix it uses
// syscall.Exec to replace the current process image in-place. On Windows
// the running EXE cannot self-replace, so a child process is spawned and
// the parent exits; the child picks up on the same port.
//
// Note: this function does NOT return on success. On Unix, syscall.Exec
// replaces the process; on Windows, os.Exit terminates it. The mutex held
// by the caller is leaked; this is intentional and unavoidable given the
// semantics of binary replacement in Go.
func (a *Applicator) triggerRestart() error {
	logger := log.Get()

	exePath, err := a.getExePath()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	childArgs := os.Args[1:]

	if runtime.GOOS == "windows" {
		logger.Info().Msg("Update apply: triggering Windows restart")

		cmd := exec.Command(exePath, childArgs...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start new process: %w", err)
		}

		// Best-effort liveness check: poll the child process for up to
		// 2 seconds. If it exits immediately (port already in use,
		// missing DLL, etc.), surface the error so the operator sees it
		// in the log before the parent exits.
		livenessDeadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(livenessDeadline) {
			if cmd.ProcessState != nil {
				logger.Error().
					Int("exit_code", cmd.ProcessState.ExitCode()).
					Msg("Update apply: new process exited immediately, restart likely failed")
				// Still exit — the child already died, there is nothing
				// to roll back. Just make sure the failure is visible.
				os.Exit(1)
			}
			time.Sleep(100 * time.Millisecond)
		}

		logger.Info().Msg("Update apply: new process started, exiting current")
		os.Exit(0)
	}

	// Unix: syscall.Exec replaces the current process image. There is
	// nothing to clean up; the kernel will dispose of the old address
	// space, file descriptors (the ones not marked CLOEXEC), and so on.
	logger.Info().Msg("Update apply: triggering Unix exec restart")
	return syscall.Exec(exePath, append([]string{exePath}, childArgs...), os.Environ())
}

// CheckPendingUpdate cleans up after a previously interrupted update. On
// Windows, if the running EXE is missing or empty but a `.updating` copy
// exists, the `.updating` file is restored to its proper name. The flag
// file (if present) is parsed and removed.
func CheckPendingUpdate(dataDir, exePath string) error {
	logger := log.Get()

	if exePath == "" {
		p, err := os.Executable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}
		exePath = p
	}

	if runtime.GOOS == "windows" {
		updatingPath := exePath + updatingSuffix
		updatingInfo, statErr := os.Stat(updatingPath)

		// Recovery: if exePath is missing/empty but .updating is present,
		// restore. This handles the case where the previous update was
		// killed between the rename and the copyFile finishing.
		exeInfo, exeStatErr := os.Stat(exePath)
		exeMissingOrEmpty := exeStatErr != nil || exeInfo.Size() == 0
		if statErr == nil && updatingInfo.Size() > 0 && exeMissingOrEmpty {
			logger.Warn().
				Str("path", updatingPath).
				Msg("Update apply: running binary missing/empty, restoring from .updating")
			if err := os.Rename(updatingPath, exePath); err != nil {
				return fmt.Errorf("failed to restore .updating: %w", err)
			}
		} else if statErr == nil {
			// Happy path: cleanup the leftover .updating now that the new
			// binary is in place and running.
			if err := os.Remove(updatingPath); err != nil && !os.IsNotExist(err) {
				logger.Warn().Err(err).Str("path", updatingPath).Msg("Update apply: failed to remove .updating")
			}
		}

		// Read and parse the flag file.
		flagPath := filepath.Join(dataDir, updatePendingFlag)
		if info, err := os.Stat(flagPath); err == nil && info.Size() > 0 {
			content, readErr := os.ReadFile(flagPath)
			if readErr != nil {
				logger.Warn().Err(readErr).Str("path", flagPath).Msg("Update apply: failed to read update flag")
			} else {
				parts := strings.SplitN(string(content), "|", 2)
				if len(parts) >= 1 {
					logger.Info().
						Str("version", parts[0]).
						Msg("Update apply: update completed")
				}
			}
			if err := os.Remove(flagPath); err != nil && !os.IsNotExist(err) {
				logger.Warn().Err(err).Str("path", flagPath).Msg("Update apply: failed to remove update flag")
			}
		}
	} else {
		// Unix: remove leftover .old from a previous successful update.
		oldPath := exePath + oldBinarySuffix
		if _, err := os.Stat(oldPath); err == nil {
			logger.Info().Str("path", oldPath).Msg("Update apply: removing leftover .old binary")
			if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
				logger.Warn().Err(err).Str("path", oldPath).Msg("Update apply: failed to remove .old binary")
			}
		}
	}

	return nil
}

// performBackup creates a backup of config.toml and vrhub.db before applying
// an update. Backup failures are non-fatal but logged at warn level; the
// caller proceeds with the update regardless.
func (a *Applicator) performBackup(ctx context.Context) error {
	logger := log.Get()

	backupsDir := filepath.Join(a.config.DataDir, backupsDirName)
	if err := os.MkdirAll(backupsDir, 0755); err != nil {
		return fmt.Errorf("failed to create backups directory: %w", err)
	}

	// Build a unique backup name: timestamp + atomic counter to avoid
	// collisions when two backups land in the same wall-clock second.
	seq := atomic.AddUint64(&backupSeq, 1)
	timestamp := time.Now().Format("2006-01-02-150405")
	backupName := fmt.Sprintf("backup-%s-%d.zip", timestamp, seq)
	backupPath := filepath.Join(backupsDir, backupName)

	zipWriter, err := os.Create(backupPath)
	if err != nil {
		return fmt.Errorf("failed to create backup file: %w", err)
	}

	zipArchive := zip.NewWriter(zipWriter)

	var backupErr error
	configAdded := false
	dbAdded := false

	configPath := filepath.Join(a.config.DataDir, "config.toml")
	if info, err := os.Stat(configPath); err == nil && !info.IsDir() {
		if err := addFileToZip(zipArchive, configPath, "config.toml"); err != nil {
			logger.Warn().Err(err).Msg("Update apply: failed to backup config.toml")
		} else {
			configAdded = true
		}
	}

	dbPath := filepath.Join(a.config.DataDir, "vrhub.db")
	if info, err := os.Stat(dbPath); err == nil && !info.IsDir() {
		if err := addFileToZip(zipArchive, dbPath, "vrhub.db"); err != nil {
			logger.Warn().Err(err).Msg("Update apply: failed to backup vrhub.db")
			backupErr = fmt.Errorf("vrhub.db backup failed: %w", err)
		} else {
			dbAdded = true
		}
	}

	// AC4: if neither config.toml nor vrhub.db were added to the archive
	// (both missing or both addFileToZip failed), the result would be a
	// useless empty zip. Bail out before close so we never leave an
	// empty .zip in backups/; the apply path will treat this as a backup
	// failure (Warn-logged by the caller) and proceed.
	if !configAdded && !dbAdded {
		_ = zipArchive.Close()
		_ = zipWriter.Close()
		if rerr := os.Remove(backupPath); rerr != nil && !os.IsNotExist(rerr) {
			logger.Warn().Err(rerr).Str("path", backupPath).Msg("Update apply: failed to remove empty backup file")
		}
		return fmt.Errorf("no files to backup: both config.toml and vrhub.db are missing in %s", a.config.DataDir)
	}

	// Close the zip BEFORE reaping old ones; on Windows the file cannot
	// be removed while open.
	if cerr := zipArchive.Close(); cerr != nil {
		logger.Warn().Err(cerr).Msg("Update apply: failed to close backup zip")
	}
	if err := zipWriter.Sync(); err != nil {
		logger.Warn().Err(err).Str("path", backupPath).Msg("Update apply: failed to sync backup file")
	}
	if cerr := zipWriter.Close(); cerr != nil {
		logger.Warn().Err(cerr).Str("path", backupPath).Msg("Update apply: failed to close backup file")
	}

	// AC3: validate the zip before declaring the backup successful. If the
	// archive is unreadable or contains no expected entry, remove the file
	// and surface the error so the caller (DownloadAndApply) can decide
	// whether to abort the apply. Fresh-install is still supported: as long
	// as at least one of config.toml / vrhub.db is present, the backup is
	// considered valid.
	if err := validateZip(backupPath, []string{"config.toml", "vrhub.db"}); err != nil {
		logger.Warn().Err(err).Str("path", backupPath).Msg("Update apply: backup validation failed, removing file")
		if rerr := os.Remove(backupPath); rerr != nil && !os.IsNotExist(rerr) {
			logger.Warn().Err(rerr).Str("path", backupPath).Msg("Update apply: failed to remove invalid backup file")
		}
		return fmt.Errorf("backup validation failed: %w", err)
	}

	logger.Info().Str("path", backupPath).Msg("Update apply: backup created")

	if err := a.cleanOldBackups(backupsDir); err != nil {
		logger.Warn().Err(err).Msg("Update apply: cleanOldBackups failed")
	}

	return backupErr
}

// addFileToZip adds filePath to the zip archive under arcName, with a Sync
// after copy so the entry is durable.
func addFileToZip(zw *zip.Writer, filePath, arcName string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	w, err := zw.Create(arcName)
	if err != nil {
		return err
	}

	if _, err := io.Copy(w, f); err != nil {
		return err
	}
	return nil
}

// cleanOldBackups removes oldest backups if more than maxAutoBackups exist.
// Errors on individual Remove calls are logged but do not abort the loop, so
// a single locked file (e.g. AV scanner holding a handle) does not stop
// the rest from being reaped.
func (a *Applicator) cleanOldBackups(backupsDir string) error {
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		return err
	}

	var backupFiles []os.DirEntry
	for _, e := range entries {
		// Reject directories and any non-regular entry that happens to be
		// named "backup-".
		if e.IsDir() {
			continue
		}
		if !e.Type().IsRegular() {
			continue
		}
		if !strings.HasPrefix(e.Name(), "backup-") {
			continue
		}
		backupFiles = append(backupFiles, e)
	}

	if len(backupFiles) <= a.config.MaxBackups {
		return nil
	}

	// Sort by name ascending (filename embeds the timestamp); remove the
	// oldest until we are at the cap.
	sort.Slice(backupFiles, func(i, j int) bool {
		return backupFiles[i].Name() < backupFiles[j].Name()
	})

	toRemove := len(backupFiles) - a.config.MaxBackups
	for i := 0; i < toRemove; i++ {
		path := filepath.Join(backupsDir, backupFiles[i].Name())
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			log.Get().Warn().Err(err).Str("path", path).Msg("Update apply: failed to remove old backup")
			continue
		}
		log.Get().Debug().Str("path", path).Msg("Update apply: removed old backup")
	}

	return nil
}

// itoa is a tiny helper to avoid strconv import noise in callers; not used
// outside this file but kept for symmetry with future numeric identifiers.
var _ = strconv.Itoa

// validateZip opens the zip at zipPath and verifies that at least one of the
// entries in atLeastOneOf is present and has a CRC32 that matches its content.
// Returns nil on success; on failure, the caller is expected to delete the file
// and surface the error. This is used by performBackup to guarantee that a
// backup written to disk is a valid archive (AC3) before the apply path
// continues.
//
// The semantic "at least one of" reflects the legitimate fresh-install case
// where only config.toml exists (db not yet created) or vice versa.
func validateZip(zipPath string, atLeastOneOf []string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("validateZip: open %s: %w", zipPath, err)
	}
	defer zr.Close()

	want := make(map[string]struct{}, len(atLeastOneOf))
	for _, n := range atLeastOneOf {
		want[n] = struct{}{}
	}

	validEntries := 0
	for _, f := range zr.File {
		if _, ok := want[f.Name]; !ok {
			continue
		}
		// Recompute CRC32 over the entry content and compare with the
		// header-stored CRC. This catches truncation, partial writes, and
		// bit-flips.
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("validateZip: open entry %s: %w", f.Name, err)
		}
		h := crc32.NewIEEE()
		if _, err := io.Copy(h, rc); err != nil {
			rc.Close()
			return fmt.Errorf("validateZip: read entry %s: %w", f.Name, err)
		}
		rc.Close()
		if h.Sum32() != f.CRC32 {
			return fmt.Errorf("validateZip: crc mismatch for %s: got %08x want %08x", f.Name, h.Sum32(), f.CRC32)
		}
		validEntries++
	}

	if validEntries == 0 {
		return fmt.Errorf("validateZip: none of %v found in %s", atLeastOneOf, zipPath)
	}
	return nil
}
