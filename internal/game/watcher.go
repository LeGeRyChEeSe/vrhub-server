package game

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	fsnotify "github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"
)

// EventType represents the type of file event.
type EventType string

const (
	EventAdded    EventType = "added"
	EventModified EventType = "modified"
	EventRemoved  EventType = "removed"
)

// FileEvent represents a file system change event.
type FileEvent struct {
	EventType   EventType
	FilePath    string
	FileName    string
	PackageName string
}

// WatchHandler is the callback function type for file watcher events.
type WatchHandler func(event FileEvent) error

// FileWatcher defines the interface for file system watchers.
type FileWatcher interface {
	Watch(dir string, handler WatchHandler) error
	Stop()
	IsSupported() bool
}

// Watcher manages file watching across platforms.
type Watcher struct {
	dataDir  string
	importer GameImporter
	logger   *zerolog.Logger
	watcher  FileWatcher
	done     chan struct{}
	wg       sync.WaitGroup
}

// GameImporter defines the interface for game import operations.
type GameImporter interface {
	ImportAPK(filePath string) error
	DeleteGameByPackage(packageName string) error
	GetExistingGames() ([]string, error)
	RevalidateGame(ctx context.Context, filePath, packageName string) (proceed bool, err error)
}

// GameDeleter defines the interface for checking game corruption status.
type GameDeleter interface {
	GetGameByPackage(packageName string) (*types.GameEntry, error)
	DeleteGame(packageName string) error
	UpdateGameExposed(packageName string, exposed bool) error
}

// NewWatcher creates a new Watcher instance with platform-appropriate watcher.
func NewWatcher(dataDir string, importer GameImporter) (*Watcher, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data dir is required for file watcher")
	}

	w := &Watcher{
		dataDir:  dataDir,
		importer: importer,
		logger:   vlog.Get(),
		done:     make(chan struct{}),
	}

	// Choose platform-specific watcher
	if runtime.GOOS == "windows" {
		w.watcher = NewPollingWatcher(dataDir, importer)
	} else {
		w.watcher = NewNativeWatcher(dataDir, importer)
	}

	return w, nil
}

// Start begins watching the configured directory.
func (w *Watcher) Start(handler WatchHandler) error {
	if _, err := os.Stat(w.dataDir); os.IsNotExist(err) {
		w.logger.Warn().Str("dir", w.dataDir).Msg("data directory does not exist, skipping file watcher")
		return nil
	}

	return w.watcher.Watch(w.dataDir, handler)
}

// Stop gracefully shuts down the watcher.
func (w *Watcher) Stop() {
	close(w.done)
	w.watcher.Stop()
	w.wg.Wait()
}

// IsSupported returns true if file watching is supported on this platform.
func (w *Watcher) IsSupported() bool {
	return w.watcher.IsSupported()
}

// --- Native Watcher (Linux/macOS) using fsnotify ---

// NativeWatcher wraps fsnotify for native OS-level file system events.
type NativeWatcher struct {
	dataDir string
	fs      *fsnotify.Watcher
	handler WatchHandler
	done    chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
}

// NewNativeWatcher creates a new NativeWatcher instance.
func NewNativeWatcher(dataDir string, importer GameImporter) *NativeWatcher {
	return &NativeWatcher{
		dataDir: dataDir,
		handler: nil, // set via Watch()
		done:    make(chan struct{}),
	}
}

// Watch starts watching the given directory using fsnotify.
func (nw *NativeWatcher) Watch(dir string, handler WatchHandler) error {
	nw.handler = handler

	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create fsnotify watcher: %w", err)
	}
	nw.fs = fs

	// Recursively add all subdirectories under dir to watch
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible dirs
		}
		if !d.IsDir() || path == dir {
			return nil
		}
		if err := fs.Add(path); err != nil {
			vlog.Get().Warn().Str("dir", path).Msg("failed to add directory to watcher")
		}
		return nil
	})
	if err != nil {
		fs.Close()
		return fmt.Errorf("walk dir for watching: %w", err)
	}

	nw.wg.Add(1)
	go nw.watchEvents()
	return nil
}

// Stop stops the native watcher.
func (nw *NativeWatcher) Stop() {
	close(nw.done)
	nw.fs.Close()
	nw.wg.Wait()
}

// IsSupported returns true (fsnotify is supported on Linux/macOS).
func (nw *NativeWatcher) IsSupported() bool {
	return runtime.GOOS != "windows"
}

func (nw *NativeWatcher) watchEvents() {
	defer nw.wg.Done()

	// Debounce map for rapid file changes (copy in progress)
	debounce := make(map[string]time.Time)
	debounceInterval := 500 * time.Millisecond
	cleanupInterval := 5 * time.Second

	// Periodically clean up stale debounce entries to prevent memory leak
	nw.wg.Add(1)
	go func() {
		defer nw.wg.Done()
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				nw.mu.Lock()
				cutoff := time.Now().Add(-debounceInterval * 2)
				for key := range debounce {
					if debounce[key].Before(cutoff) {
						delete(debounce, key)
					}
				}
				nw.mu.Unlock()
			case <-nw.done:
				return
			}
		}
	}()

	for {
		select {
		case event, ok := <-nw.fs.Events:
			if !ok {
				return
			}
			ext := strings.ToLower(filepath.Ext(event.Name))
			if ext != ".apk" && ext != ".obb" {
				continue
			}

			// Debounce rapid changes (e.g., Create then Write during file copy)
			now := time.Now()
			nw.mu.Lock()
			if lastTime, exists := debounce[event.Name]; exists && now.Sub(lastTime) < debounceInterval {
				debounce[event.Name] = now
				nw.mu.Unlock()
				continue
			}
			debounce[event.Name] = now
			nw.mu.Unlock()

			var eventType EventType
			if event.Op&fsnotify.Remove == fsnotify.Remove || event.Op&fsnotify.Rename == fsnotify.Rename {
				eventType = EventRemoved
			} else if event.Op&fsnotify.Write == fsnotify.Write {
				eventType = EventModified
			} else {
				eventType = EventAdded
			}

			fileEvent := FileEvent{
				EventType: eventType,
				FilePath:  event.Name,
				FileName:  filepath.Base(event.Name),
			}

			if nw.handler != nil {
				if err := nw.handler(fileEvent); err != nil {
					vlog.Get().Error().Err(err).Str("file", event.Name).Str("event", string(eventType)).Msg("file watcher handler error")
				}
			}

		case err, ok := <-nw.fs.Errors:
			if !ok {
				return
			}
			vlog.Get().Error().Err(err).Msg("file watcher error")

		case <-nw.done:
			return
		}
	}
}

// --- Polling Watcher (Windows) using time.Ticker ---

// PollingWatcher uses periodic directory scanning for Windows.
type PollingWatcher struct {
	dataDir       string
	importer      GameImporter
	handler       WatchHandler
	done          chan struct{}
	wg            sync.WaitGroup
	lastScan      map[string]fileSnapshot
	mu            sync.RWMutex
	initialScan   bool
	lastEventTime map[string]time.Time
}

type fileSnapshot struct {
	modTime time.Time
	size    int64
}

// NewPollingWatcher creates a new PollingWatcher instance.
func NewPollingWatcher(dataDir string, importer GameImporter) *PollingWatcher {
	return &PollingWatcher{
		dataDir:       dataDir,
		importer:      importer,
		handler:       nil, // set via Watch()
		done:          make(chan struct{}),
		lastScan:      make(map[string]fileSnapshot),
		lastEventTime: make(map[string]time.Time),
	}
}

// Watch starts the polling loop for the given directory.
func (pw *PollingWatcher) Watch(dir string, handler WatchHandler) error {
	pw.handler = handler
	pw.dataDir = dir
	pw.initialScan = true

	// Initial scan to establish baseline
	pw.scanDirectory(dir)
	pw.initialScan = false

	pw.wg.Add(1)
	go pw.pollLoop()
	return nil
}

// Stop stops the polling watcher.
func (pw *PollingWatcher) Stop() {
	close(pw.done)
	pw.wg.Wait()
}

// IsSupported returns true (polling works on all platforms including Windows).
func (pw *PollingWatcher) IsSupported() bool {
	return true
}

func (pw *PollingWatcher) pollLoop() {
	defer pw.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			pw.scanDirectory(pw.dataDir)
		case <-pw.done:
			return
		}
	}
}

func (pw *PollingWatcher) scanDirectory(dir string) {
	currentFiles := make(map[string]fileSnapshot)

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible dirs
		}
		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".apk" && ext != ".obb" {
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}

		currentFiles[path] = fileSnapshot{
			modTime: info.ModTime(),
			size:    info.Size(),
		}

		// Check for new files (Added event)
		pw.mu.RLock()
		snapshot, exists := pw.lastScan[path]
		pw.mu.RUnlock()

		if !exists && !pw.initialScan {
			pw.mu.RLock()
			lastEvent, hasLast := pw.lastEventTime[path]
			pw.mu.RUnlock()
			if hasLast && time.Since(lastEvent) < 2*time.Second {
				return nil
			}

			fileEvent := FileEvent{
				EventType: EventAdded,
				FilePath:  path,
				FileName:  d.Name(),
			}
			if pw.handler != nil {
				if err := pw.handler(fileEvent); err != nil {
					vlog.Get().Error().Err(err).Str("file", path).Msg("polling watcher handler error")
				}
			}
			pw.mu.Lock()
			pw.lastEventTime[path] = time.Now()
			pw.mu.Unlock()
		} else if !pw.initialScan && (snapshot.modTime.Before(info.ModTime()) || snapshot.size != info.Size()) {
			// File modified (Modified event)
			pw.mu.RLock()
			lastEvent, hasLast := pw.lastEventTime[path]
			pw.mu.RUnlock()
			if hasLast && time.Since(lastEvent) < 2*time.Second {
				return nil
			}

			fileEvent := FileEvent{
				EventType: EventModified,
				FilePath:  path,
				FileName:  d.Name(),
			}
			if pw.handler != nil {
				if err := pw.handler(fileEvent); err != nil {
					vlog.Get().Error().Err(err).Str("file", path).Msg("polling watcher handler error")
				}
			}
			pw.mu.Lock()
			pw.lastEventTime[path] = time.Now()
			pw.mu.Unlock()
		}

		return nil
	})

	if err != nil {
		vlog.Get().Warn().Err(err).Str("dir", dir).Msg("polling scan error, continuing")
	}

	// Check for removed files (Removed event)
	pw.mu.RLock()
	for path := range pw.lastScan {
		if _, exists := currentFiles[path]; !exists {
			fileEvent := FileEvent{
				EventType: EventRemoved,
				FilePath:  path,
				FileName:  filepath.Base(path),
			}
			if pw.handler != nil {
				if err := pw.handler(fileEvent); err != nil {
					vlog.Get().Error().Err(err).Str("file", path).Msg("polling watcher handler error")
				}
			}
		}
	}
	pw.mu.RUnlock()

	// Update snapshot
	pw.mu.Lock()
	pw.lastScan = currentFiles
	pw.mu.Unlock()
}

// RescanResult holds the summary of a rescan operation.
type RescanResult struct {
	FilesScanned int   `json:"files_scanned"`
	GamesAdded   int   `json:"games_added"`
	GamesRemoved int   `json:"games_removed"`
	TotalSize    int64 `json:"total_size_bytes"`
}

// ScanAndImport performs a full directory scan and imports new games.
// Backward-compatible wrapper: scans a single directory.
func ScanAndImport(ctx context.Context, dir string, importer GameImporter) (RescanResult, error) {
	return ScanAndImportMultiple(ctx, []string{dir}, importer)
}

// ScanAndImportMultiple scans multiple game folders and imports new games.
// It aggregates results across all folders and only removes games from the
// DB when they are absent from *all* scanned folders.
func ScanAndImportMultiple(ctx context.Context, dirs []string, importer GameImporter) (RescanResult, error) {
	var result RescanResult

	// Collect files from all configured game folders.
	var allFiles []FileEntry
	for _, dir := range dirs {
		files, err := ScanDirectory(dir)
		if err != nil {
			vlog.Get().Warn().Err(err).Str("dir", dir).Msg("scan directory failed, skipping")
			continue
		}
		allFiles = append(allFiles, files...)
	}

	result.FilesScanned = len(allFiles)

	apkFiles := make([]FileEntry, 0)
	for _, f := range allFiles {
		if f.IsAPK {
			result.TotalSize += f.Size
			apkFiles = append(apkFiles, f)
		} else {
			result.TotalSize += f.Size
		}
	}

	// Get existing games from DB to check for duplicates and removals
	existingPackages, err := importer.GetExistingGames()
	if err != nil {
		return result, fmt.Errorf("get existing games: %w", err)
	}
	existingSet := make(map[string]bool)
	for _, pkg := range existingPackages {
		existingSet[pkg] = true
	}

	// Track packages found in current scan
	foundPackages := make(map[string]bool)

	for _, apk := range apkFiles {
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("rescan cancelled: %w", ctx.Err())
		default:
		}

		// Fix #1 (Round 10): Validate APK integrity before metadata extraction.
		// If corrupted, use ImportAPK which stores with corrupted=true internally.
		apkResult := ValidateAPK(apk.Path)
		if apkResult.Corrupted {
			vlog.Get().Warn().Str("file", apk.Path).Str("reason", apkResult.CorruptionReason).Msg("corrupted APK detected during rescan, storing with corruption flag")

			if err := importer.ImportAPK(apk.Path); err != nil {
				vlog.Get().Warn().Str("file", apk.Path).Err(err).Msg("failed to store corrupted game during rescan")
			}
			// Fix #10 (Round 11): Add to foundPackages so corrupted game is not flagged as deleted
			// We need to extract package name from path since metadata extraction is skipped
			if fallbackName := ExtractPackageNameFromPath(apk.Path); fallbackName != "" {
				foundPackages[fallbackName] = true
			}
			continue
		}

		meta, err := ExtractAPKMetadata(apk.Path)
		if err != nil {
			vlog.Get().Warn().Str("file", apk.Path).Err(err).Msg("failed to extract APK metadata, skipping")
			continue
		}

		if meta.PackageName == "" {
			vlog.Get().Warn().Str("file", apk.Path).Msg("APK has no package name, skipping")
			continue
		}

		foundPackages[meta.PackageName] = true

		// Check for duplicates (AC4)
		if existingSet[meta.PackageName] {
			// Task 3: Re-validate existing games if file has changed (AC #4)
			// Use RevalidateGame to handle mtime comparison and corruption status update internally
			_, err := importer.RevalidateGame(ctx, apk.Path, meta.PackageName)
			if err != nil {
				vlog.Get().Error().Err(err).Str("package", meta.PackageName).Msg("failed to revalidate game during rescan")
			}

			continue
		}

		// Import the new game
		if err := importer.ImportAPK(apk.Path); err != nil {
			vlog.Get().Error().Err(err).Str("file", apk.Path).Msg("failed to import APK")
			continue
		}

		result.GamesAdded++
		vlog.Get().Info().Str("game", meta.Label).Str("package", meta.PackageName).Str("file", apk.Path).Msg("imported new game from file watcher")
	}

	// Check for removed games (AC3) — Fix #5 (Round 10): mark corrupted games as unexposed instead of deleting
	for pkg := range existingSet {
		if !foundPackages[pkg] {
			deleter, ok := importer.(GameDeleter)
			if ok {
				gameEntry, getErr := deleter.GetGameByPackage(pkg)
				if getErr == nil && gameEntry != nil {
					if gameEntry.Corrupted {
						// Fix #5 (Round 10): Mark corrupted games as not exposed instead of deleting them
						// This keeps them visible in admin UI even when files are missing
						if updateErr := deleter.UpdateGameExposed(pkg, false); updateErr != nil {
							vlog.Get().Error().Err(updateErr).Str("package", pkg).Msg("failed to mark corrupted game as unexposed")
						} else {
							result.GamesRemoved++
							vlog.Get().Info().Str("package", pkg).Msg("marked corrupted game as not exposed (file deleted)")
						}
						continue
					}
				}
			}

			if err := importer.DeleteGameByPackage(pkg); err != nil {
				vlog.Get().Error().Err(err).Str("package", pkg).Msg("failed to delete game from DB")
				continue
			}
			result.GamesRemoved++
			vlog.Get().Info().Str("package", pkg).Msg("removed game from DB (file deleted)")
		}
	}

	return result, nil
}
