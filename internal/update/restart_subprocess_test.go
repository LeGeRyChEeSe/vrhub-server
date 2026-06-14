package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestTriggerRestart_RealRestart is the AC3 black-box test: it proves that the
// REAL triggerRestart actually restarts the process into a freshly built target
// binary — exercising syscall.Exec on Unix and the spawn + os.Exit path on
// Windows. Every other test stubs the restart (restartFn seam) because
// triggerRestart never returns on success (it replaces or exits the process),
// which would kill the test runner.
//
// Pattern: the test re-execs its own test binary as a gated harness subprocess
// (BE_RESTART_HARNESS=1). The harness builds an Applicator pointing getExePath
// at the target binary and calls the real triggerRestart. The target binary
// writes a marker file (proving it actually ran), then stays alive past the
// Windows 2s liveness window before exiting 0. The parent asserts the marker
// was written with the expected content.
//
// Skips cleanly when the Go toolchain is unavailable (e.g. WSL without Go per
// HANDOFF-2026-06-14) so CI stays green on toolchain-less hosts.
func TestTriggerRestart_RealRestart(t *testing.T) {
	// --- Harness subprocess branch --------------------------------------
	if os.Getenv("BE_RESTART_HARNESS") == "1" {
		runRestartHarness()
		return // unreachable on success (triggerRestart execs/exits)
	}

	// --- Parent branch --------------------------------------------------
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping real-restart black-box test")
	}

	tmpDir := t.TempDir()
	marker := filepath.Join(tmpDir, "restarted.marker")
	target := buildMarkerBinary(t, tmpDir)

	// Re-exec THIS test binary as the harness, running only this test, gated
	// by the env var so it takes the harness branch above.
	cmd := exec.Command(os.Args[0], "-test.run=^TestTriggerRestart_RealRestart$", "-test.v")
	cmd.Env = append(os.Environ(),
		"BE_RESTART_HARNESS=1",
		"VRHUB_TARGET_BIN="+target,
		"VRHUB_DATA_DIR="+tmpDir,
		"VRHUB_MARKER="+marker,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("harness subprocess failed: %v\noutput:\n%s", err, out)
	}

	// The target writes the marker near-instantly; poll briefly to absorb the
	// async spawn on Windows.
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, rerr := os.ReadFile(marker)
		if rerr == nil {
			if string(data) != "restarted" {
				t.Fatalf("marker content = %q, want %q", string(data), "restarted")
			}
			return // success: the real triggerRestart launched the target binary
		}
		if time.Now().After(deadline) {
			t.Fatalf("marker file never written by restarted target binary\nharness output:\n%s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// runRestartHarness is the body of the gated subprocess: it calls the real
// triggerRestart with getExePath pointing at the target binary. triggerRestart
// does not return on success, so any code after it indicates failure.
func runRestartHarness() {
	target := os.Getenv("VRHUB_TARGET_BIN")
	a := &Applicator{
		config:     ApplyConfig{DataDir: os.Getenv("VRHUB_DATA_DIR")},
		getExePath: func() (string, error) { return target, nil },
	}
	if err := a.triggerRestart(); err != nil {
		// Surface the error to the parent via a non-zero exit.
		os.Stderr.WriteString("triggerRestart returned error: " + err.Error() + "\n")
		os.Exit(2)
	}
	// On Unix syscall.Exec replaced the image; on Windows os.Exit was called.
	// Reaching here means the restart silently failed.
	os.Stderr.WriteString("triggerRestart returned without restarting\n")
	os.Exit(3)
}

// buildMarkerBinary writes a tiny stdlib-only Go program and compiles it into
// tmpDir. The program writes the marker file named by VRHUB_MARKER, then sleeps
// past the Windows 2s liveness window before exiting 0 — a real server child
// stays alive, so an instant exit would be (correctly) treated as a failed
// restart by triggerRestart on Windows.
func buildMarkerBinary(t *testing.T, tmpDir string) string {
	t.Helper()

	const src = `package main

import (
	"os"
	"time"
)

func main() {
	if m := os.Getenv("VRHUB_MARKER"); m != "" {
		_ = os.WriteFile(m, []byte("restarted"), 0644)
	}
	// Stay alive past the Windows liveness check (2s) so the spawn is not
	// mistaken for an immediate-exit failure.
	time.Sleep(2500 * time.Millisecond)
	os.Exit(0)
}
`
	srcPath := filepath.Join(tmpDir, "marker_main.go")
	if err := os.WriteFile(srcPath, []byte(src), 0644); err != nil {
		t.Fatalf("write marker source: %v", err)
	}

	out := filepath.Join(tmpDir, "marker_target")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, srcPath)
	cmd.Dir = tmpDir
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build marker binary failed: %v\n%s", err, combined)
	}
	return out
}
