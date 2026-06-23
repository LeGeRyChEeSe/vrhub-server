package game

import (
	"sync"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
)

// WatcherManager owns the live *Watcher pointer and serialises
// creation, restart, and shutdown so a settings-driven restart (Story
// 3.5 AC3) cannot race the server-shutdown path.
//
// The manager is created once in cmd/server/main.go and its Restart
// method is wired as the admin "game folders changed" hook. Restart is
// safe to call with an empty folder set (it just stops the running
// watcher), with a nil manager (no-op via the nil-receiver guard in the
// caller), or repeatedly.
type WatcherManager struct {
	mu       sync.Mutex
	watcher  *Watcher
	importer GameImporter
	handler  WatchHandler
	stopped  bool
}

// NewWatcherManager creates a manager bound to a single importer and
// event handler. The handler is reused across restarts so the
// per-event import/remove logic in main.go stays in one place.
func NewWatcherManager(importer GameImporter, handler WatchHandler) *WatcherManager {
	return &WatcherManager{
		importer: importer,
		handler:  handler,
	}
}

// Start creates and starts a watcher for the given folders. It is a
// no-op (returns nil) when folders is empty, mirroring the main.go
// startup guard. Any previously running watcher is stopped first.
func (m *WatcherManager) Start(folders []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLocked(folders)
}

// Restart stops the current watcher (if any) and starts a fresh one on
// the new folder set. Called by the admin settings handler after a
// successful save when cfg.GameFolders changed. An empty folder set
// stops the watcher without starting a new one.
func (m *WatcherManager) Restart(folders []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped {
		// The server is shutting down; ignore late restart requests.
		return nil
	}

	if m.watcher != nil {
		m.watcher.Stop()
		m.watcher = nil
	}

	if len(folders) == 0 {
		vlog.Get().Info().Msg("game folders cleared; file watcher stopped, not restarted")
		return nil
	}

	vlog.Get().Info().Strs("folders", folders).Msg("restarting file watcher on updated game folders")
	return m.startLocked(folders)
}

// startLocked builds and starts a watcher. The caller must hold m.mu.
func (m *WatcherManager) startLocked(folders []string) error {
	if len(folders) == 0 {
		return nil
	}

	w, err := NewWatcher(folders, m.importer)
	if err != nil {
		return err
	}
	if !w.IsSupported() {
		// Polling watcher is supported everywhere, so this branch only
		// triggers on a native watcher on an unsupported platform.
		// Keep the manager usable (watcher stays nil) without erroring.
		vlog.Get().Warn().Msg("file watcher not supported on this platform")
		return nil
	}
	if err := w.Start(m.handler); err != nil {
		return err
	}
	m.watcher = w
	return nil
}

// Stop permanently shuts down the manager and any running watcher.
// After Stop, Restart is a no-op. Safe to call on a nil-watcher manager.
func (m *WatcherManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
	if m.watcher != nil {
		m.watcher.Stop()
		m.watcher = nil
	}
}
