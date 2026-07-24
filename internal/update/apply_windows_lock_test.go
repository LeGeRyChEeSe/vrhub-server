//go:build windows

package update

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// openExclusiveNoDelete opens path without FILE_SHARE_DELETE, reproducing
// the exact condition an antivirus scanner (including Windows Defender)
// puts a just-written or just-vacated executable in: os.Remove and
// os.Rename against such a handle fail with "Access is denied" until it
// is closed. This is the real-world lock that removeWithRetry,
// removeStaleUpdatingFile, and renameAsideAndDefer exist to route around.
func openExclusiveNoDelete(t *testing.T, path string) syscall.Handle {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, // deliberately no FILE_SHARE_DELETE
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0)
	if err != nil {
		t.Fatalf("CreateFile(%s): %v", path, err)
	}
	return h
}

func TestRemoveWithRetry_SucceedsOnceLockClears(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "locked.txt")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h := openExclusiveNoDelete(t, path)
	go func() {
		time.Sleep(150 * time.Millisecond)
		syscall.CloseHandle(h)
	}()

	if err := removeWithRetry(path, 10, 50*time.Millisecond); err != nil {
		t.Fatalf("removeWithRetry did not succeed once the lock cleared: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("expected %s to be removed", path)
	}
}

func TestRemoveStaleUpdatingFile_ErrorsWhileLockedThenRecoversAfterUnlock(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "vrhub-server.exe.updating")
	if err := os.WriteFile(path, []byte("stale"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Without FILE_SHARE_DELETE on the open handle, Windows denies BOTH
	// os.Remove and os.Rename against this path — a rename needs delete
	// access on the source just like a delete does. So while genuinely
	// locked, removeStaleUpdatingFile's retry-then-rename-aside fallback
	// must still surface an error rather than silently pretending to
	// have freed the name slot.
	h := openExclusiveNoDelete(t, path)
	if err := removeStaleUpdatingFile(path); err == nil {
		syscall.CloseHandle(h)
		t.Fatalf("expected removeStaleUpdatingFile to fail while the file is genuinely locked")
	}
	syscall.CloseHandle(h)

	// Once the lock clears, the same call (as replaceBinary would retry
	// on its next update attempt) must succeed and actually remove the
	// file — this is the real recovery path a transient AV scan relies on.
	if err := removeStaleUpdatingFile(path); err != nil {
		t.Fatalf("removeStaleUpdatingFile should succeed once the lock clears, got: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("expected %s to be removed once unlocked", path)
	}
}

func TestSweepStaleFiles_RemovesOrphans(t *testing.T) {
	tmpDir := t.TempDir()
	exePath := filepath.Join(tmpDir, "vrhub-server.exe")
	orphan1 := exePath + ".111.stale"
	orphan2 := exePath + ".222.stale"
	if err := os.WriteFile(orphan1, []byte("a"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(orphan2, []byte("b"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Simulates the process having exited before renameAsideAndDefer's
	// 10s background removal fired — CheckPendingUpdate must sweep these
	// up at the next boot instead of leaving them to accumulate forever.
	sweepStaleFiles(exePath)

	for _, p := range []string{orphan1, orphan2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected orphan %s to be swept at startup, but it still exists", p)
		}
	}
}
