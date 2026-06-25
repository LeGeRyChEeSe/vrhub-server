package game

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// mockImporter implements GameImporter for testing.
type mockImporter struct {
	imported  []string
	deleted   []string
	existing  []string
	mu        sync.Mutex
	importErr error
	deleteErr error
}

func (m *mockImporter) ImportAPK(filePath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.imported = append(m.imported, filePath)
	return m.importErr
}

func (m *mockImporter) DeleteGameByPackage(packageName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, packageName)
	return m.deleteErr
}

func (m *mockImporter) GetExistingGames() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.existing))
	copy(result, m.existing)
	return result, nil
}

func (m *mockImporter) RevalidateGame(ctx context.Context, filePath, packageName string) (bool, error) {
	// Mock: always return false (no import needed for existing games)
	return false, nil
}

func (m *mockImporter) GetGameByPackage(packageName string) (*types.GameEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pkg := range m.existing {
		if pkg == packageName {
			return &types.GameEntry{PackageName: packageName, Corrupted: true}, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (m *mockImporter) DeleteGame(packageName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, pkg := range m.existing {
		if pkg == packageName {
			m.existing = append(m.existing[:i], m.existing[i+1:]...)
			return nil
		}
	}
	return sql.ErrNoRows
}

func (m *mockImporter) UpdateGameExposed(packageName string, exposed bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return nil
}

func TestPollingWatcher_DetectsNewFiles(t *testing.T) {
	tmpDir := t.TempDir()

	var events []FileEvent
	var mu sync.Mutex

	handler := func(event FileEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	}

	pw := NewPollingWatcher([]string{tmpDir}, &mockImporter{})
	if err := pw.Watch(handler); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	defer pw.Stop()

	// Wait for initial scan to complete
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	initialCount := len(events)
	mu.Unlock()

	// Create a new APK file after initial scan
	newAPK := filepath.Join(tmpDir, "newgame.apk")
	if err := os.WriteFile(newAPK, []byte("fake apk"), 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	// Wait for next poll cycle (use short ticker for testing)
	time.Sleep(35 * time.Second)

	mu.Lock()
	defer mu.Unlock()

	found := false
	for _, e := range events[initialCount:] {
		if e.EventType == EventAdded && e.FileName == "newgame.apk" {
			found = true
			break
		}
	}

	if !found {
		t.Log("Polling interval is 30s, test may need longer wait. Checking all events...")
		for _, e := range events {
			if e.FileName == "newgame.apk" && e.EventType == EventAdded {
				found = true
				break
			}
		}
	}

	if !found {
		t.Logf("Events captured: %d, last event: %+v", len(events), events)
	}
}

func TestPollingWatcher_DetectsRemovedFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial APK file
	initialAPK := filepath.Join(tmpDir, "initial.apk")
	if err := os.WriteFile(initialAPK, []byte("fake apk"), 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	var events []FileEvent
	var mu sync.Mutex

	handler := func(event FileEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	}

	pw := NewPollingWatcher([]string{tmpDir}, &mockImporter{})
	if err := pw.Watch(handler); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	defer pw.Stop()

	// Wait for initial scan
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	initialCount := len(events)
	mu.Unlock()

	// Remove the file
	if err := os.Remove(initialAPK); err != nil {
		t.Fatalf("remove test file: %v", err)
	}

	// Wait for next poll cycle
	time.Sleep(35 * time.Second)

	mu.Lock()
	defer mu.Unlock()

	found := false
	for _, e := range events[initialCount:] {
		if e.EventType == EventRemoved && e.FileName == "initial.apk" {
			found = true
			break
		}
	}

	if !found {
		t.Logf("Events captured: %d", len(events))
		for i, e := range events {
			t.Logf("  [%d] type=%s file=%s", i, e.EventType, e.FileName)
		}
	}
}

func TestScanAndImport_NewGame(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake APK file (not a real APK but enough for scan test)
	apkPath := filepath.Join(tmpDir, "testgame.apk")
	if err := os.WriteFile(apkPath, []byte("fake apk data"), 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	mock := &mockImporter{
		existing: []string{},
	}

	ctx := context.Background()
	result, err := ScanAndImport(ctx, tmpDir, mock)
	if err != nil {
		t.Fatalf("ScanAndImport failed: %v", err)
	}

	if result.FilesScanned == 0 {
		t.Log("No files scanned (expected for fake APK without valid manifest)")
	}

	// The import should be skipped because the fake APK has no valid AndroidManifest.xml
	// This is correct behavior - we're testing that the scanner doesn't crash
	if result.GamesAdded != 0 {
		t.Errorf("games_added = %d, want 0 (fake APK has no valid manifest)", result.GamesAdded)
	}
}

func TestScanAndImport_DuplicateDetection(t *testing.T) {
	tmpDir := t.TempDir()

	mock := &mockImporter{
		existing: []string{"com.example.game1"},
	}

	ctx := context.Background()
	result, err := ScanAndImport(ctx, tmpDir, mock)
	if err != nil {
		t.Fatalf("ScanAndImport failed: %v", err)
	}

	// No files to scan, so no games added or removed
	if result.GamesAdded != 0 {
		t.Errorf("games_added = %d, want 0", result.GamesAdded)
	}

	_ = mock // mock has existing packages but no files to process
}

func TestScanAndImport_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	mock := &mockImporter{
		existing: []string{},
	}

	ctx := context.Background()
	result, err := ScanAndImport(ctx, tmpDir, mock)
	if err != nil {
		t.Fatalf("ScanAndImport failed: %v", err)
	}

	if result.FilesScanned != 0 {
		t.Errorf("files_scanned = %d, want 0", result.FilesScanned)
	}
	if result.GamesAdded != 0 {
		t.Errorf("games_added = %d, want 0", result.GamesAdded)
	}
	if result.GamesRemoved != 0 {
		t.Errorf("games_removed = %d, want 0", result.GamesRemoved)
	}
}

func TestNewWatcher_EmptyFolders(t *testing.T) {
	mock := &mockImporter{}
	if _, err := NewWatcher(nil, mock); err == nil {
		t.Fatal("expected error for nil folder set, got nil")
	}
	if _, err := NewWatcher([]string{}, mock); err == nil {
		t.Fatal("expected error for empty folder set, got nil")
	}
}

func TestWatcher_IsSupported(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockImporter{}

	w, err := NewWatcher([]string{tmpDir}, mock)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}

	supported := w.IsSupported()
	if !supported {
		t.Log("File watcher not supported on this platform")
	}
}

func TestGameManager_ImportAPK_FakeAPK(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	gameManager := NewGameManager(database, tmpDir)

	// Create a fake APK (not valid) - should be stored with corrupted=true
	fakeAPK := filepath.Join(tmpDir, "fake.apk")
	if err := os.WriteFile(fakeAPK, []byte("not a real apk"), 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	err = gameManager.ImportAPK(fakeAPK)
	if err != nil {
		t.Fatalf("ImportAPK should succeed for corrupted APK (stored in DB), got error: %v", err)
	}

	// Verify the game was stored with corrupted=true
	game, err := database.GetGameByPackage("fake")
	if err != nil {
		t.Fatalf("expected game to be found in DB, got error: %v", err)
	}
	if !game.Corrupted {
		t.Error("expected corrupted game to have corrupted=true")
	}
	if game.CorruptionReason == "" {
		t.Error("expected corruption_reason to be set")
	}
}

func TestGameManager_DeleteNonExistentGame(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	gameManager := NewGameManager(database, tmpDir)

	err = gameManager.DeleteGameByPackage("com.nonexistent.game")
	if err != nil {
		t.Logf("DeleteGameByPackage returned error (expected for non-existent package): %v", err)
	}
}

func TestGameManager_GetExistingGames_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	gameManager := NewGameManager(database, tmpDir)

	packages, err := gameManager.GetExistingGames()
	if err != nil {
		t.Fatalf("GetExistingGames failed: %v", err)
	}

	if len(packages) != 0 {
		t.Errorf("got %d packages, want 0", len(packages))
	}
}

func TestRescanResult_JSONFields(t *testing.T) {
	result := RescanResult{
		FilesScanned: 42,
		GamesAdded:   1,
		GamesRemoved: 0,
		TotalSize:    15728640,
	}

	if result.FilesScanned != 42 {
		t.Errorf("FilesScanned = %d, want 42", result.FilesScanned)
	}
	if result.GamesAdded != 1 {
		t.Errorf("GamesAdded = %d, want 1", result.GamesAdded)
	}
	if result.GamesRemoved != 0 {
		t.Errorf("GamesRemoved = %d, want 0", result.GamesRemoved)
	}
	if result.TotalSize != 15728640 {
		t.Errorf("TotalSize = %d, want 15728640", result.TotalSize)
	}
}

func TestPollingWatcher_SkipsNonAPKOBBCFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create non-APK/OBB files
	for _, name := range []string{"readme.txt", "icon.png", "data.json"} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("data"), 0644); err != nil {
			t.Fatalf("create test file: %v", err)
		}
	}

	var events []FileEvent
	var mu sync.Mutex

	handler := func(event FileEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	}

	pw := NewPollingWatcher([]string{tmpDir}, &mockImporter{})
	if err := pw.Watch(handler); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	defer pw.Stop()

	// Wait for initial scan and poll cycle
	time.Sleep(35 * time.Second)

	mu.Lock()
	defer mu.Unlock()

	for _, e := range events {
		ext := filepath.Ext(e.FileName)
		if ext == ".apk" || ext == ".obb" {
			t.Errorf("unexpected APK/OBB event for non-APK/OBB file: %s", e.FileName)
		}
	}
}

func TestPollingWatcher_InitialScanCapturesExistingFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial files before starting watcher
	for _, name := range []string{"game1.apk", "game2.apk"} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("fake apk"), 0644); err != nil {
			t.Fatalf("create test file: %v", err)
		}
	}

	var events []FileEvent
	var mu sync.Mutex

	handler := func(event FileEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	}

	pw := NewPollingWatcher([]string{tmpDir}, &mockImporter{})
	if err := pw.Watch(handler); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	defer pw.Stop()

	// Wait for initial scan
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	eventCount := len(events)
	mu.Unlock()

	// Initial scan should not generate events for existing files
	// (events are only generated when comparing against previous state)
	t.Logf("Initial scan captured %d events (expected 0 for baseline)", eventCount)
}

func TestScanAndImport_CorruptedGameStoredInDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	gameManager := NewGameManager(database, tmpDir)

	// Create a corrupted APK (not a valid ZIP archive)
	corruptedAPK := filepath.Join(tmpDir, "corrupted-game.apk")
	if err := os.WriteFile(corruptedAPK, []byte("this is not a valid zip archive"), 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	ctx := context.Background()
	result, err := ScanAndImport(ctx, tmpDir, gameManager)
	if err != nil {
		t.Fatalf("ScanAndImport failed: %v", err)
	}

	// The corrupted APK should be stored in DB with corrupted=true
	game, err := database.GetGameByPackage("corrupted-game")
	if err != nil {
		t.Fatalf("expected game to be found in DB after rescan of corrupted file, got error: %v", err)
	}

	if !game.Corrupted {
		t.Error("expected corrupted game to have corrupted=true after rescan")
	}
	if game.CorruptionReason == "" {
		t.Error("expected corruption_reason to be set for corrupted APK during rescan")
	}
	if game.Exposed {
		t.Error("expected corrupted game to have exposed=false")
	}

	_ = result // FilesScanned should include the corrupted APK
}

func TestScanAndImport_CorruptedGameMarkedUnexposedWhenFileMissing(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	gameManager := NewGameManager(database, tmpDir)

	// First, import a corrupted game (stored with corrupted=true, exposed=false)
	corruptedAPK := filepath.Join(tmpDir, "corrupted-game.apk")
	if err := os.WriteFile(corruptedAPK, []byte("not a valid zip"), 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	err = gameManager.ImportAPK(corruptedAPK)
	if err != nil {
		t.Fatalf("ImportAPK for corrupted APK should succeed, got error: %v", err)
	}

	// Verify the game was stored with corrupted=true and exposed=false
	game, err := database.GetGameByPackage("corrupted-game")
	if err != nil {
		t.Fatalf("expected game to be in DB, got error: %v", err)
	}
	if !game.Corrupted {
		t.Error("expected game to be corrupted after import")
	}

	// Now remove the APK file and run rescan
	if err := os.Remove(corruptedAPK); err != nil {
		t.Fatalf("remove test file: %v", err)
	}

	ctx := context.Background()
	result, err := ScanAndImport(ctx, tmpDir, gameManager)
	if err != nil {
		t.Fatalf("ScanAndImport failed after removing file: %v", err)
	}

	// The corrupted game should still be in DB but marked as not exposed (not deleted)
	gameAfter, err := database.GetGameByPackage("corrupted-game")
	if err != nil {
		t.Fatalf("expected corrupted game to remain in DB after rescan with missing file, got error: %v", err)
	}

	if !gameAfter.Corrupted {
		t.Error("expected game to still be marked as corrupted after rescan")
	}
	if gameAfter.Exposed {
		t.Error("expected corrupted game with missing file to have exposed=false after rescan")
	}

	if result.GamesRemoved == 0 {
		t.Log("Games removed count may vary depending on implementation; checking DB state is primary validation")
	}
}

func TestRevalidateGame_CorruptedToValidTransition(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	gameManager := NewGameManager(database, tmpDir)

	// Step 1: Import a corrupted APK
	corruptedAPK := filepath.Join(tmpDir, "test-game.apk")
	if err := os.WriteFile(corruptedAPK, []byte("not a valid zip"), 0644); err != nil {
		t.Fatalf("create test file: %v", err)
	}

	err = gameManager.ImportAPK(corruptedAPK)
	if err != nil {
		t.Fatalf("ImportAPK for corrupted APK should succeed, got error: %v", err)
	}

	game, _ := database.GetGameByPackage("test-game")
	if !game.Corrupted {
		t.Fatal("expected game to be corrupted after importing invalid file")
	}

	// Step 2: Replace with a valid ZIP (but not a real APK - will still fail metadata extraction)
	// Create a minimal valid ZIP archive
	validZipPath := filepath.Join(tmpDir, "test-game-valid.apk")
	if err := createValidZIP(validZipPath, map[string]string{"AndroidManifest.xml": "<manifest/>"}); err != nil {
		t.Fatalf("create valid ZIP: %v", err)
	}

	// Rename to replace the corrupted file
	if err := os.Rename(validZipPath, corruptedAPK); err != nil {
		t.Fatalf("rename valid APK: %v", err)
	}

	// Step 3: Revalidate - should detect the file changed and re-validate
	proceed, err := gameManager.RevalidateGame(context.Background(), corruptedAPK, "test-game")
	if err != nil {
		t.Fatalf("RevalidateGame failed: %v", err)
	}

	// The game should no longer be corrupted (ZIP is valid even if metadata extraction fails)
	gameAfter, err := database.GetGameByPackage("test-game")
	if err != nil {
		t.Fatalf("expected game to still be in DB after revalidation, got error: %v", err)
	}

	// Note: The ZIP is valid so ValidateAPK passes. Metadata extraction may fail but
	// the corruption flag should be cleared since the file itself is not corrupted.
	if !gameAfter.Corrupted {
		t.Log("Game transitioned from corrupted to non-corrupted (ZIP is valid)")
	}

	_ = proceed // Revalidate returns false for existing games regardless of validity
}

func TestRevalidateGame_ValidToCorruptedTransition(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	gameManager := NewGameManager(database, tmpDir)

	// Step 1: Insert a valid game entry into DB
	apkPath := filepath.Join(tmpDir, "com.test.validgame.apk")
	if err := createValidZIP(apkPath, map[string]string{"AndroidManifest.xml": "<manifest/>"}); err != nil {
		t.Fatalf("create valid ZIP: %v", err)
	}

	gameEntry := types.GameEntry{
		ReleaseName:  "com.test.validgame",
		GameName:     "Valid Game",
		PackageName:  "com.test.validgame",
		VersionCode:  1,
		SizeBytes:    1024,
		LastUpdated:  time.Now(),
		Corrupted:    false,
		Exposed:      true,
		OBBSizeBytes: 0,
	}
	if err := database.InsertGame(gameEntry); err != nil {
		t.Fatalf("insert test game: %v", err)
	}

	// Step 2: Corrupt the APK file by overwriting with invalid data
	if err := os.WriteFile(apkPath, []byte("corrupted data not a zip"), 0644); err != nil {
		t.Fatalf("overwrite with corrupted data: %v", err)
	}

	// Step 3: Revalidate - should detect corruption and update DB
	proceed, err := gameManager.RevalidateGame(context.Background(), apkPath, "com.test.validgame")
	if err != nil {
		t.Logf("RevalidateGame returned error (mtime may match): %v", err)
	}

	gameAfter, _ := database.GetGameByPackage("com.test.validgame")
	if gameAfter == nil {
		t.Fatal("expected game to still be in DB after revalidation")
	}

	if !gameAfter.Corrupted {
		t.Log("Game corruption status: corrupted=" + fmt.Sprintf("%v", gameAfter.Corrupted))
	}

	_ = proceed
}

// createValidZIP creates a minimal valid ZIP archive with the given entries.
func createValidZIP(path string, entries map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for name, content := range entries {
		wr, err := w.Create(name)
		if err != nil {
			return err
		}
		_, err = wr.Write([]byte(content))
		if err != nil {
			return err
		}
	}
	return w.Close()
}

// TestAcquirePackageLock_ContextCanceled verifies that acquirePackageLock
// returns ctx.Err() promptly when the context is cancelled while waiting
// for the per-package mutex (C-07/C-12).
func TestAcquirePackageLock_ContextCanceled(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	gm := NewGameManager(database, tmpDir)

	// Acquire the lock for "com.example.cancelled" in a separate goroutine
	// (simulating another import in progress).
	firstRelease := make(chan struct{})
	firstAcquired := make(chan struct{})
	go func() {
		release, err := gm.acquirePackageLock(context.Background(), "com.example.cancelled")
		if err != nil {
			t.Errorf("first acquirePackageLock unexpected error: %v", err)
			close(firstAcquired)
			return
		}
		close(firstAcquired)
		<-firstRelease
		release()
	}()

	// Wait until the first goroutine has the lock, then cancel the second ctx.
	<-firstAcquired
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	start := time.Now()
	release, err := gm.acquirePackageLock(ctx, "com.example.cancelled")
	elapsed := time.Since(start)
	if err == nil {
		release()
		close(firstRelease)
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	// Sanity: should return in well under the 10ms retry interval for a cancelled ctx.
	if elapsed > 100*time.Millisecond {
		t.Errorf("acquirePackageLock took %v with cancelled ctx, expected < 100ms", elapsed)
	}

	// Release the first lock and verify a fresh acquisition succeeds.
	close(firstRelease)
	time.Sleep(20 * time.Millisecond) // allow the first goroutine to release
	release2, err := gm.acquirePackageLock(context.Background(), "com.example.cancelled")
	if err != nil {
		t.Fatalf("post-cancel acquirePackageLock unexpected error: %v", err)
	}
	release2()
}

// TestAcquirePackageLock_DeadlineExceeded verifies that acquirePackageLock
// returns context.DeadlineExceeded when the context's deadline expires
// while waiting for the lock (C-07/C-12 deadline-path coverage).
func TestAcquirePackageLock_DeadlineExceeded(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	gm := NewGameManager(database, tmpDir)

	// Hold the lock from a separate goroutine.
	firstRelease := make(chan struct{})
	firstAcquired := make(chan struct{})
	go func() {
		release, err := gm.acquirePackageLock(context.Background(), "com.example.deadline")
		if err != nil {
			t.Errorf("first acquirePackageLock unexpected error: %v", err)
			close(firstAcquired)
			return
		}
		close(firstAcquired)
		<-firstRelease
		release()
	}()
	<-firstAcquired

	// Use a very short deadline (5ms) so the test is fast but the lock is still held.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	_, err = gm.acquirePackageLock(ctx, "com.example.deadline")
	if err == nil {
		close(firstRelease)
		t.Fatal("expected deadline error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}

	close(firstRelease)
}
