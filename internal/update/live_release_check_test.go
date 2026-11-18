package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestLiveRelease_DiscoveryAndApplyAgainstPublishedRelease exercises the whole
// self-update path against the release this repository actually publishes, not a
// mock: discovery through api.github.com/repos/LeGeRyChEeSe/vrhub-server/releases/latest,
// then DownloadAndApply on the real published asset, with the release's own
// checksums.txt as the SHA-256 source.
//
// Gated by VRHUB_LIVE_RELEASE_CHECK=1 so it never runs in CI (CI must not depend
// on network or on a release existing). Run manually to validate an install/update:
//
//	VRHUB_LIVE_RELEASE_CHECK=1 go test -run TestLiveRelease ./internal/update/ -v
//
// Nothing outside t.TempDir() is touched: getExePath is pointed at a throwaway
// directory, so the installed vrhub-server binary and its data dir are untouched.
func TestLiveRelease_DiscoveryAndApplyAgainstPublishedRelease(t *testing.T) {
	if os.Getenv("VRHUB_LIVE_RELEASE_CHECK") != "1" {
		t.Skip("set VRHUB_LIVE_RELEASE_CHECK=1 to run the live release check")
	}

	// --- 1. Discovery against the real GitHub API -----------------------
	// current is pinned one release behind so the checker must offer an update.
	c := NewChecker(Config{Enabled: true, Owner: "LeGeRyChEeSe", Repo: "vrhub-server"},
		Version{Major: 0, Minor: 1, Patch: 1})
	c.check(context.Background())
	res := c.GetResult()
	if res == nil {
		t.Fatal("REAL_RELEASE_DISCOVERY_FAILED: checker returned no result")
	}
	if !res.VersionAvailable || res.DownloadURL == "" {
		t.Fatalf("REAL_RELEASE_DISCOVERY_FAILED: available=%v url=%q", res.VersionAvailable, res.DownloadURL)
	}
	if !strings.HasPrefix(res.DownloadURL, "https://") {
		t.Fatalf("download URL is not https: %q", res.DownloadURL)
	}
	t.Logf("REAL_RELEASE_DISCOVERY_OK latest=%s url=%s", res.LatestVersion, res.DownloadURL)

	dir := t.TempDir()
	exeName := "vrhub-server"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	exePath := filepath.Join(dir, exeName)
	const oldBytes = "OLD-INSTALLED-BINARY"
	if err := os.WriteFile(exePath, []byte(oldBytes), 0755); err != nil {
		t.Fatalf("write stand-in exe: %v", err)
	}

	// --- 2. Apply with the release's own checksums.txt ------------------
	a := NewApplicator(ApplyConfig{
		DataDir:     dir,
		DownloadURL: res.DownloadURL,
		ChecksumURL: "https://github.com/LeGeRyChEeSe/vrhub-server/releases/download/" +
			"v" + res.LatestVersion + "/checksums.txt",
		Version:     res.LatestVersion,
		AutoApply:   true,
		AutoBackup:  true,
		MaxBackups:  3,
		AutoRestart: false,
	})
	a.getExePath = func() (string, error) { return exePath, nil }
	restarted := false
	a.restartFn = func() error { restarted = true; return nil }

	err := a.DownloadAndApply(context.Background())
	// AutoRestart is false, which is the staged-install contract: the new binary
	// is in place and ErrRestartPending says an explicit restart finalises it.
	// Anything else is a real failure of the published-artifact apply.
	if err != nil && !errors.Is(err, ErrRestartPending) {
		t.Fatalf("REAL_APPLY_FAILED: %v", err)
	}
	if errors.Is(err, ErrRestartPending) {
		t.Log("STAGED_INSTALL_CONTRACT_OK (ErrRestartPending as AutoRestart=false requires)")
	}

	data, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatalf("read applied exe: %v", err)
	}
	if string(data) == oldBytes {
		t.Fatal("REAL_APPLY_FAILED: binary bytes unchanged, nothing was staged")
	}
	if len(data) < 1_000_000 {
		t.Fatalf("applied binary looks too small to be the published release binary: %d bytes", len(data))
	}
	t.Logf("REAL_APPLY_OK exe replaced with %d bytes (was %d)", len(data), len(oldBytes))
	if !restarted {
		t.Fatal("restart hook was not invoked")
	}
	t.Log("RESTART_HOOK_INVOKED")

	entries, _ := os.ReadDir(filepath.Join(dir, "updates"))
	var zips []string
	for _, e := range entries {
		zips = append(zips, e.Name())
	}
	t.Logf("updates/ after apply: %v", zips)
	if _, err := os.Stat(filepath.Join(dir, "backups")); err != nil {
		t.Errorf("no backup directory created: %v", err)
	}
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(filepath.Join(dir, "update_pending.flag")); err != nil {
			t.Errorf("Windows: expected update_pending.flag so the next boot finalises the swap: %v", err)
		} else {
			t.Log("WINDOWS_STAGING_FLAG_OK")
		}
	}

	// --- 3. Fail closed on a checksum that does not match --------------
	dir2 := t.TempDir()
	exe2 := filepath.Join(dir2, exeName)
	if err := os.WriteFile(exe2, []byte(oldBytes), 0755); err != nil {
		t.Fatalf("write stand-in exe: %v", err)
	}
	bad := NewApplicator(ApplyConfig{
		DataDir:     dir2,
		DownloadURL: res.DownloadURL,
		// An asset that exists but is not the checksum file: every line of this
		// response fails to match the downloaded zip's SHA-256.
		ChecksumURL: "https://github.com/LeGeRyChEeSe/vrhub-server/releases/download/" +
			"v" + res.LatestVersion + "/vrhub-server-windows-amd64.zip",
		Version:     res.LatestVersion,
		AutoApply:   true,
		AutoBackup:  true,
		AutoRestart: false,
	})
	bad.getExePath = func() (string, error) { return exe2, nil }
	badRestarted := false
	bad.restartFn = func() error { badRestarted = true; return nil }
	err = bad.DownloadAndApply(context.Background())
	if err == nil {
		t.Fatal("CHECKSUM_FAILCLOSED_FAILED: apply succeeded despite an unusable checksum source")
	}
	after, _ := os.ReadFile(exe2)
	if string(after) != oldBytes {
		t.Fatalf("checksum mismatch must leave the binary untouched, got %d bytes", len(after))
	}
	if badRestarted {
		t.Fatal("a checksum mismatch must never reach the restart step")
	}
	t.Logf("CHECKSUM_FAILCLOSED_OK err=%v", err)
}
