package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// mockStatsRecorder captures IncrementDownloadStats calls for testing.
// The download hook is async fire-and-forget, so the test sleeps a
// short moment after the request returns before asserting on counts.
type mockStatsRecorder struct {
	mu    sync.Mutex
	calls []statsCall
	err   error // optional: returned from IncrementDownloadStats
	delay time.Duration
}

type statsCall struct {
	hash        string
	bytesServed int64
}

func (m *mockStatsRecorder) IncrementDownloadStats(hash string, bytesServed int64) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, statsCall{hash: hash, bytesServed: bytesServed})
	return m.err
}

func (m *mockStatsRecorder) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// waitForCalls polls the recorder until at least n calls have been
// observed, or fails the test after the deadline. The async hook can
// complete after the HTTP response is written, so we need a small
// grace window.
func (m *mockStatsRecorder) waitForCalls(t *testing.T, n int, deadline time.Duration) {
	t.Helper()
	timeout := time.After(deadline)
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		if m.callCount() >= n {
			return
		}
		select {
		case <-timeout:
			t.Fatalf("waitForCalls: got %d calls, want %d (deadline %s)", m.callCount(), n, deadline)
		case <-tick.C:
		}
	}
}

// makeDownloadFixture writes a small file under tmpDir/games/{hash}/{pkg}/
// and returns the paths so tests can construct fileServerDeps.
func makeDownloadFixture(t *testing.T, tmpDir, hash, pkg, filename string, content []byte) string {
	t.Helper()
	dir := filepath.Join(tmpDir, "games", hash, pkg)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	full := filepath.Join(dir, filename)
	if err := os.WriteFile(full, content, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return full
}

// TestServeFileDownload_200OK_IncrementsStats is the AC5 happy path:
// a 200 download triggers exactly one IncrementDownloadStats call
// with the file's byte size and the game's hash.
func TestServeFileDownload_200OK_IncrementsStats(t *testing.T) {
	tmpDir := t.TempDir()
	hash := "abc123def456789012345678abcdef00"
	pkg := "com.test.game"
	fileContent := []byte("hello world - this is a test file for download")
	makeDownloadFixture(t, tmpDir, hash, pkg, "app-release.apk", fileContent)

	stats := &mockStatsRecorder{}

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: pkg,
				Hash:        hash,
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
		StatsDB:    stats,
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: pkg,
		Hash:        hash,
	}, pkg, "app-release.apk")

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	stats.waitForCalls(t, 1, 500*time.Millisecond)

	stats.mu.Lock()
	defer stats.mu.Unlock()
	if len(stats.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(stats.calls))
	}
	call := stats.calls[0]
	if call.hash != hash {
		t.Errorf("call.hash = %q, want %q", call.hash, hash)
	}
	if call.bytesServed != int64(len(fileContent)) {
		t.Errorf("call.bytesServed = %d, want %d", call.bytesServed, len(fileContent))
	}
}

// TestServeFileDownload_206Partial_NoIncrement is the AC6 contract:
// a 206 Partial Content response must NOT trigger an increment.
// Range requests are one logical download split across many TCP
// reads, so we only count the full 200.
func TestServeFileDownload_206Partial_NoIncrement(t *testing.T) {
	tmpDir := t.TempDir()
	hash := "abc123def456789012345678abcdef00"
	pkg := "com.test.game"
	fileContent := make([]byte, 2048)
	for i := range fileContent {
		fileContent[i] = byte(i % 256)
	}
	makeDownloadFixture(t, tmpDir, hash, pkg, "app-release.apk", fileContent)

	stats := &mockStatsRecorder{}

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: pkg,
				Hash:        hash,
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
		StatsDB:    stats,
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Range", "bytes=0-1023")

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: pkg,
		Hash:        hash,
	}, pkg, "app-release.apk")

	if w.Code != 206 {
		t.Fatalf("status = %d, want 206", w.Code)
	}

	// Give the (non-)scheduled goroutine a moment to NOT run.
	time.Sleep(50 * time.Millisecond)

	if stats.callCount() != 0 {
		t.Errorf("calls = %d, want 0 for 206 partial", stats.callCount())
	}
}

// TestServeFileDownload_404_NoIncrement: nonexistent file → 404 → no
// increment. The 404 path returns BEFORE the increment hook.
func TestServeFileDownload_404_NoIncrement(t *testing.T) {
	tmpDir := t.TempDir()
	hash := "abc123def456789012345678abcdef00"
	pkg := "com.test.game"

	stats := &mockStatsRecorder{}

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: pkg,
				Hash:        hash,
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
		StatsDB:    stats,
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: pkg,
		Hash:        hash,
	}, pkg, "nonexistent.apk")

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}

	time.Sleep(50 * time.Millisecond)
	if stats.callCount() != 0 {
		t.Errorf("calls = %d, want 0 for 404", stats.callCount())
	}
}

// TestServeFileDownload_416_NoIncrement: out-of-bounds range → 416 →
// no increment.
func TestServeFileDownload_416_NoIncrement(t *testing.T) {
	tmpDir := t.TempDir()
	hash := "abc123def456789012345678abcdef00"
	pkg := "com.test.game"
	fileContent := []byte("short")
	makeDownloadFixture(t, tmpDir, hash, pkg, "app-release.apk", fileContent)

	stats := &mockStatsRecorder{}

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: pkg,
				Hash:        hash,
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
		StatsDB:    stats,
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Range", "bytes=500-")

	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: pkg,
		Hash:        hash,
	}, pkg, "app-release.apk")

	if w.Code != 416 {
		t.Fatalf("status = %d, want 416", w.Code)
	}

	time.Sleep(50 * time.Millisecond)
	if stats.callCount() != 0 {
		t.Errorf("calls = %d, want 0 for 416", stats.callCount())
	}
}

// TestServeFileDownload_StatsDBNil_NoPanic: when StatsDB is nil,
// the increment is silently skipped (no panic). Defensive: the
// download must still serve normally.
func TestServeFileDownload_StatsDBNil_NoPanic(t *testing.T) {
	tmpDir := t.TempDir()
	hash := "abc123def456789012345678abcdef00"
	pkg := "com.test.game"
	fileContent := []byte("hello world")
	makeDownloadFixture(t, tmpDir, hash, pkg, "app-release.apk", fileContent)

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Test Game",
				PackageName: pkg,
				Hash:        hash,
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
		StatsDB:    nil, // explicit
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/test", nil)

	// Must not panic.
	serveFileDownload(w, r, deps, &types.GameEntry{
		GameName:    "Test Game",
		PackageName: pkg,
		Hash:        hash,
	}, pkg, "app-release.apk")

	if w.Code != 200 {
		t.Errorf("status = %d, want 200 (StatsDB=nil must not break downloads)", w.Code)
	}
}

// TestServeFileDownload_Concurrent_AccurateCount is the AC7 contract:
// N parallel 200 downloads for the same game → exactly N increments.
// Uses a real *db.DB (not the mock) so the test exercises the
// UPDATE column = column + ? atomicity.
func TestServeFileDownload_Concurrent_AccurateCount(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := db.Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	hash := "concurrent00000000000000000000000abc"
	pkg := "com.concurrent.game"
	fileContent := []byte("12345678")
	makeDownloadFixture(t, tmpDir, hash, pkg, "app-release.apk", fileContent)

	if err := d.InsertGame(types.GameEntry{
		ReleaseName: pkg, GameName: pkg, PackageName: pkg,
		VersionCode: 1, SizeBytes: int64(len(fileContent)),
		Hash: hash, Exposed: true,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	deps := fileServerDeps{
		FileDB: &mockFileServerDB{
			game: &types.GameEntry{
				GameName:    "Concurrent Game",
				PackageName: pkg,
				Hash:        hash,
			},
		},
		FileReader: &realFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
		StatsDB:    d, // real *db.DB satisfies StatsRecorder
	}

	const N = 10
	var wg sync.WaitGroup
	var done int32
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/test", nil)
			serveFileDownload(w, r, deps, &types.GameEntry{
				GameName:    "Concurrent Game",
				PackageName: pkg,
				Hash:        hash,
			}, pkg, "app-release.apk")
			atomic.AddInt32(&done, 1)
		}()
	}
	wg.Wait()

	// All HTTP responses should be 200.
	if got := atomic.LoadInt32(&done); got != N {
		t.Fatalf("done = %d, want %d", got, N)
	}

	// Wait for all async goroutines to commit the UPDATE.
	// Poll the DB until DownloadCount == N, or fail after a deadline.
	deadline := time.Now().Add(2 * time.Second)
	for {
		stats, err := d.GetStatsForHash(hash)
		if err != nil {
			t.Fatalf("get stats: %v", err)
		}
		if stats.DownloadCount == N {
			if stats.TotalBandwidthBytes != int64(N)*int64(len(fileContent)) {
				t.Errorf("TotalBandwidthBytes = %d, want %d", stats.TotalBandwidthBytes, int64(N)*int64(len(fileContent)))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("DownloadCount = %d after deadline, want %d (concurrent UPDATE lost writes?)", stats.DownloadCount, N)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
