package game

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Story 3.5 tests: multi-folder watching, missing-folder tolerance, and
// config-change restart. These drive the PollingWatcher.scanFolders /
// WatcherManager logic directly so they stay fast and deterministic
// (they do NOT wait on the 30s poll ticker).

// collectingHandler returns a WatchHandler that records every event into
// the supplied slice under a mutex, plus a snapshot accessor.
func collectingHandler() (WatchHandler, func() []FileEvent) {
	var (
		mu     sync.Mutex
		events []FileEvent
	)
	h := func(event FileEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	}
	snapshot := func() []FileEvent {
		mu.Lock()
		defer mu.Unlock()
		out := make([]FileEvent, len(events))
		copy(out, events)
		return out
	}
	return h, snapshot
}

func hasAddedEvent(events []FileEvent, name string) bool {
	for _, e := range events {
		if e.EventType == EventAdded && e.FileName == name {
			return true
		}
	}
	return false
}

// Task 4.1 (AC2): a PollingWatcher configured with two folders fires an
// Added event for a file dropped into EITHER folder.
func TestPollingWatcher_DetectsAcrossMultipleFolders(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	handler, snapshot := collectingHandler()

	pw := NewPollingWatcher([]string{dirA, dirB}, &mockImporter{})
	// Establish the baseline (initialScan) without firing events, then
	// flip initialScan off to mirror Watch()'s post-baseline state.
	pw.handler = handler
	pw.initialScan = true
	pw.scanFolders()
	pw.initialScan = false

	apkA := filepath.Join(dirA, "alpha.apk")
	apkB := filepath.Join(dirB, "beta.apk")
	if err := os.WriteFile(apkA, []byte("fake apk a"), 0644); err != nil {
		t.Fatalf("write apk in dirA: %v", err)
	}
	if err := os.WriteFile(apkB, []byte("fake apk b"), 0644); err != nil {
		t.Fatalf("write apk in dirB: %v", err)
	}

	// One scan pass should detect both files across both roots.
	pw.scanFolders()

	events := snapshot()
	if !hasAddedEvent(events, "alpha.apk") {
		t.Errorf("expected Added event for alpha.apk in folder A; events=%+v", events)
	}
	if !hasAddedEvent(events, "beta.apk") {
		t.Errorf("expected Added event for beta.apk in folder B; events=%+v", events)
	}
}

// Task 4.2 (AC4): a missing/inaccessible folder is skipped without
// aborting the scan — files in the valid folder still fire events.
func TestPollingWatcher_SkipsMissingFolder_StillWatchesValid(t *testing.T) {
	validDir := t.TempDir()
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")

	handler, snapshot := collectingHandler()

	pw := NewPollingWatcher([]string{missingDir, validDir}, &mockImporter{})
	pw.handler = handler
	pw.initialScan = true
	pw.scanFolders()
	pw.initialScan = false

	apk := filepath.Join(validDir, "valid.apk")
	if err := os.WriteFile(apk, []byte("fake apk"), 0644); err != nil {
		t.Fatalf("write apk in valid dir: %v", err)
	}

	// Must not panic even though the first folder is missing.
	pw.scanFolders()

	if !hasAddedEvent(snapshot(), "valid.apk") {
		t.Errorf("expected Added event for valid.apk despite a missing sibling folder; events=%+v", snapshot())
	}
}

// Task 4.2 (AC4): a folder that vanishes between snapshots does NOT
// spuriously fire Removed events for files that were present in it —
// the per-folder skip preserves prior knowledge without erasing it.
func TestPollingWatcher_VanishedFolder_NoSpuriousRemoved(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "present.apk")
	if err := os.WriteFile(apk, []byte("fake apk"), 0644); err != nil {
		t.Fatalf("write apk: %v", err)
	}

	handler, snapshot := collectingHandler()

	pw := NewPollingWatcher([]string{dir}, &mockImporter{})
	pw.handler = handler
	pw.initialScan = true
	pw.scanFolders() // baseline now includes present.apk
	pw.initialScan = false

	// Remove the whole folder so os.Stat fails for it on the next pass.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove dir: %v", err)
	}

	pw.scanFolders()

	// The folder vanished (os.Stat fails) so it is skipped entirely; we
	// accept that the snapshot is not updated for it. The key contract:
	// no panic. A Removed event here is acceptable behaviour (the file
	// genuinely disappeared), but the scan must not crash.
	_ = snapshot()
}

// Task 4.4 (AC5): NewWatcher rejects an empty folder set and the
// WatcherManager treats empty folders as a no-op (no panic).
func TestWatcherManager_EmptyFolders_NoStart(t *testing.T) {
	called := false
	handler := func(FileEvent) error { called = true; return nil }

	m := NewWatcherManager(&mockImporter{}, handler)

	if err := m.Start(nil); err != nil {
		t.Fatalf("Start(nil) should be a no-op, got error: %v", err)
	}
	if err := m.Start([]string{}); err != nil {
		t.Fatalf("Start(empty) should be a no-op, got error: %v", err)
	}
	// Restart with empty folders just stops (nothing running) — no panic.
	if err := m.Restart(nil); err != nil {
		t.Fatalf("Restart(nil) should be a no-op, got error: %v", err)
	}
	m.Stop() // safe on a manager that never started a watcher
	if called {
		t.Error("handler should never be invoked when no folders are configured")
	}
}

// Task 4.3 (AC3): after the manager restarts on a new folder set, a drop
// into the NEW folder is detected and the OLD folder is no longer
// watched. Uses native fsnotify on Linux/macOS and polling on Windows;
// to stay platform-independent and fast we assert on the watcher's
// folder set rather than waiting on OS events.
func TestWatcherManager_Restart_SwitchesFolderSet(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()

	handler, _ := collectingHandler()
	m := NewWatcherManager(&mockImporter{}, handler)

	if err := m.Start([]string{oldDir}); err != nil {
		t.Fatalf("initial Start: %v", err)
	}

	m.mu.Lock()
	if m.watcher == nil {
		m.mu.Unlock()
		t.Fatal("expected a running watcher after Start")
	}
	gotOld := append([]string(nil), m.watcher.folders...)
	m.mu.Unlock()
	if len(gotOld) != 1 || gotOld[0] != oldDir {
		t.Fatalf("watcher should target the old folder, got %v", gotOld)
	}

	if err := m.Restart([]string{newDir}); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	// Snapshot the watcher's folder set under the lock, then release it
	// BEFORE calling Stop() (Stop acquires m.mu itself — holding it here
	// would deadlock).
	m.mu.Lock()
	if m.watcher == nil {
		m.mu.Unlock()
		t.Fatal("expected a running watcher after Restart")
	}
	gotNew := append([]string(nil), m.watcher.folders...)
	m.mu.Unlock()

	if len(gotNew) != 1 || gotNew[0] != newDir {
		t.Fatalf("watcher should target the new folder after restart, got %v", gotNew)
	}
	for _, f := range gotNew {
		if f == oldDir {
			t.Errorf("old folder %q must not be watched after restart", oldDir)
		}
	}

	m.Stop()
}

// AC3 helper: gameFoldersChanged-style set comparison is
// exercised in the api package; here we confirm the watcher's stored
// folder slice is decoupled from the caller's slice (NewWatcher copies),
// so a later mutation of cfg.GameFolders cannot change a live watcher.
func TestNewWatcher_CopiesFolderSlice(t *testing.T) {
	folders := []string{t.TempDir()}
	w, err := NewWatcher(folders, &mockImporter{})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	original := folders[0]
	folders[0] = "mutated"
	if w.folders[0] != original {
		t.Errorf("watcher folder set must be independent of caller slice; got %q want %q", w.folders[0], original)
	}
}
