package metadata

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestFetcher(t *testing.T, ts *httptest.Server) *Fetcher {
	t.Helper()
	dataDir := t.TempDir()
	url := ts.URL
	return NewFetcher(dataDir, url, 24*time.Hour)
}

func createTarball(t *testing.T, files map[string]string) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("failed to write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write tar content: %v", err)
		}
	}

	tw.Close()
	gw.Close()
	return &buf
}

func TestFetch_304NotModified(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == "\"test-etag\"" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	f := newTestFetcher(t, ts)

	// Pre-populate ETag file.
	cacheDir := filepath.Join(f.dataDir, cacheDirName)
	os.MkdirAll(cacheDir, 0755)
	os.WriteFile(filepath.Join(cacheDir, etagFile), []byte("\"test-etag\""), 0644)

	err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("expected no error on 304, got: %v", err)
	}
}

func TestFetch_SuccessfulDownload(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "\"new-etag\"")
		w.Header().Set("Last-Modified", "Mon, 01 Jan 2024 00:00:00 GMT")

		tarball := createTarball(t, map[string]string{
			"icons/game1.png":      "icon-content",
			"thumbnails/game1.jpg": "thumb-content",
			"notes/game1.txt":      "note content",
		})

		w.Header().Set("Content-Type", "application/gzip")
		io.Copy(w, tarball)
	}))
	defer ts.Close()

	f := newTestFetcher(t, ts)
	err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Verify extracted files.
	cacheDir := filepath.Join(f.dataDir, cacheDirName)
	expectedFiles := map[string]string{
		"icons/game1.png":      "icon-content",
		"thumbnails/game1.jpg": "thumb-content",
		"notes/game1.txt":      "note content",
	}

	for path, expectedContent := range expectedFiles {
		content, err := os.ReadFile(filepath.Join(cacheDir, path))
		if err != nil {
			t.Fatalf("expected file %s to exist: %v", path, err)
		}
		if string(content) != expectedContent {
			t.Errorf("file %s content mismatch: got %q, want %q", path, string(content), expectedContent)
		}
	}

	// Verify ETag and Last-Modified saved.
	etag, err := os.ReadFile(filepath.Join(cacheDir, etagFile))
	if err != nil {
		t.Fatalf("expected ETag file to exist: %v", err)
	}
	if strings.TrimSpace(string(etag)) != "\"new-etag\"" {
		t.Errorf("ETag mismatch: got %q, want %q", string(etag), "\"new-etag\"")
	}

	lm, err := os.ReadFile(filepath.Join(cacheDir, lastModifiedFile))
	if err != nil {
		t.Fatalf("expected Last-Modified file to exist: %v", err)
	}
	if strings.TrimSpace(string(lm)) != "Mon, 01 Jan 2024 00:00:00 GMT" {
		t.Errorf("Last-Modified mismatch: got %q, want %q", string(lm), "Mon, 01 Jan 2024 00:00:00 GMT")
	}
}

func TestFetch_NetworkFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "connection refused", http.StatusBadGateway)
	}))
	defer ts.Close()

	f := newTestFetcher(t, ts)
	err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error on network failure, got nil")
	}

	// Verify the error is wrapped properly.
	if !strings.Contains(err.Error(), "all 3 retries exhausted") {
		t.Errorf("expected retry exhaustion error, got: %v", err)
	}
}

func TestFetch_PathTraversalProtection(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Create a tarball with a path traversal attempt.
		tarballContent := createTarball(t, map[string]string{
			"../../etc/evil.txt": "malicious content",
			"safe/file.txt":      "safe content",
		})

		w.Header().Set("ETag", "\"test-etag\"")
		io.Copy(w, tarballContent)
	}))
	defer ts.Close()

	f := newTestFetcher(t, ts)
	err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("expected no error (path traversal should be skipped), got: %v", err)
	}

	// Verify the malicious file was NOT extracted.
	evilPath := filepath.Join(f.dataDir, cacheDirName, "..", "..", "etc", "evil.txt")
	if _, err := os.Stat(evilPath); !os.IsNotExist(err) {
		t.Errorf("path traversal file should not exist: %s", evilPath)
	}

	// Verify the safe file WAS extracted.
	safeContent, err := os.ReadFile(filepath.Join(f.dataDir, cacheDirName, "safe/file.txt"))
	if err != nil {
		t.Fatalf("expected safe file to be extracted: %v", err)
	}
	if string(safeContent) != "safe content" {
		t.Errorf("safe file content mismatch: got %q, want %q", string(safeContent), "safe content")
	}
}

func TestFetch_CorruptTarball(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "\"test-etag\"")
		io.Copy(w, bytes.NewReader([]byte("not-a-valid-gzip-tarball")))
	}))
	defer ts.Close()

	f := newTestFetcher(t, ts)
	err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error on corrupt tarball, got nil")
	}

	if !strings.Contains(err.Error(), "extract") {
		t.Errorf("expected extract error, got: %v", err)
	}
}

func TestFetch_ContextCancellation(t *testing.T) {
	requestCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	f := newTestFetcher(t, ts)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := f.Fetch(ctx)
	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
}

func TestNewFetcher_DefaultURL(t *testing.T) {
	f := NewFetcher("/tmp/test", "", 24*time.Hour)
	if f.url != defaultMetadataURL {
		t.Errorf("expected default URL %q, got %q", defaultMetadataURL, f.url)
	}
}

func TestNewFetcher_CustomURL(t *testing.T) {
	customURL := "http://localhost:9999/metadata.tar.gz"
	f := NewFetcher("/tmp/test", customURL, 24*time.Hour)
	if f.url != customURL {
		t.Errorf("expected custom URL %q, got %q", customURL, f.url)
	}
}

func TestFetch_ConditionalRequestWithLastModified(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Modified-Since") == "Mon, 01 Jan 2024 00:00:00 GMT" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	f := newTestFetcher(t, ts)

	// Pre-populate Last-Modified file with RFC1123 format.
	cacheDir := filepath.Join(f.dataDir, cacheDirName)
	os.MkdirAll(cacheDir, 0755)
	os.WriteFile(filepath.Join(cacheDir, lastModifiedFile), []byte("Mon, 01 Jan 2024 00:00:00 GMT"), 0644)

	err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("expected no error on 304 via Last-Modified, got: %v", err)
	}
}

func TestFetch_429RateLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limited"))
	}))
	defer ts.Close()

	f := newTestFetcher(t, ts)
	err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error on 429 rate limit, got nil")
	}

	if !strings.Contains(err.Error(), "all 3 retries exhausted") {
		t.Errorf("expected retry exhaustion error on 429, got: %v", err)
	}
}

func TestFetch_UnexpectedStatusCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	f := newTestFetcher(t, ts)
	err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}

	if !strings.Contains(err.Error(), "retries exhausted") {
		t.Errorf("expected retry exhaustion error, got: %v", err)
	}
}

// TestStartScheduledFetch_TickerFires tests that the ticker fires after the configured interval.
func TestStartScheduledFetch_TickerFires(t *testing.T) {
	fetchCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount++
		w.Header().Set("ETag", fmt.Sprintf("\"etag-%d\"", fetchCount))
		tarball := createTarball(t, map[string]string{
			"icons/game.png": "icon-content",
		})
		w.Header().Set("Content-Type", "application/gzip")
		io.Copy(w, tarball)
	}))
	defer ts.Close()

	f := NewFetcher(t.TempDir(), ts.URL, 50*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	f.StartScheduledFetch(ctx)

	if fetchCount < 2 {
		t.Errorf("expected at least 2 fetches within 200ms with 50ms interval, got %d", fetchCount)
	}
}

// TestStartScheduledFetch_GracefulShutdown tests that Stop() gracefully shuts down the goroutine.
func TestStartScheduledFetch_GracefulShutdown(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response.
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	f := NewFetcher(t.TempDir(), ts.URL, 50*time.Millisecond)

	done := make(chan struct{})

	go func() {
		f.StartScheduledFetch(context.Background())
		close(done)
	}()

	// Give the goroutine time to start and begin a fetch.
	time.Sleep(100 * time.Millisecond)

	// Stop should cause StartScheduledFetch to return.
	f.Stop()

	select {
	case <-done:
		// Expected: goroutine returned after Stop().
	case <-time.After(2 * time.Second):
		t.Fatal("StartScheduledFetch did not return within 2 seconds after Stop()")
	}
}

// TestGetLastRefreshTime_NoFile tests that getLastRefreshTime returns 0 when file doesn't exist.
func TestGetLastRefreshTime_NoFile(t *testing.T) {
	f := NewFetcher(t.TempDir(), "", 24*time.Hour)
	ts, err := f.GetLastRefreshTime()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if ts != 0 {
		t.Errorf("expected 0 when no file exists, got: %d", ts)
	}
}

// TestSaveLastRefreshTime then getLastRefreshTime returns the saved timestamp.
func TestSaveAndGetLastRefreshTime(t *testing.T) {
	f := NewFetcher(t.TempDir(), "", 24*time.Hour)
	err := f.saveLastRefreshTime()
	if err != nil {
		t.Fatalf("saveLastRefreshTime failed: %v", err)
	}
	ts, err := f.GetLastRefreshTime()
	if err != nil {
		t.Fatalf("getLastRefreshTime failed: %v", err)
	}
	if ts <= 0 {
		t.Errorf("expected positive timestamp, got: %d", ts)
	}
}

// TestStop_Idempotent tests that calling Stop() multiple times does not panic.
func TestStop_Idempotent(t *testing.T) {
	f := NewFetcher(t.TempDir(), "", 24*time.Hour)

	f.Stop()
	f.Stop()
	f.Stop()

	if !f.IsShutdown() {
		t.Error("expected fetcher to be marked as shutdown")
	}
}

// TestWait_ReturnsTrue tests that Wait() returns true when the goroutine exits.
func TestWait_ReturnsTrue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	f := NewFetcher(t.TempDir(), ts.URL, 50*time.Millisecond)

	done := make(chan struct{})
	go func() {
		f.StartScheduledFetch(context.Background())
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	f.Stop()

	if !f.Wait(2 * time.Second) {
		t.Error("expected Wait() to return true within 2 seconds")
	}

	select {
	case <-done:
	default:
		t.Fatal("StartScheduledFetch goroutine did not exit")
	}
}

// TestWait_ShutdownCancelsInFlight tests that Stop() cancels in-flight fetches via shutdownCtx.
func TestWait_ShutdownCancelsInFlight(t *testing.T) {
	requestDone := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(requestDone)
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	f := NewFetcher(t.TempDir(), ts.URL, 100*time.Millisecond)

	done := make(chan struct{})
	go func() {
		f.StartScheduledFetch(context.Background())
		close(done)
	}()

	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	f.Stop()

	// Wait() should return true because shutdownCtx cancellation interrupts the in-flight fetch.
	if !f.Wait(2 * time.Second) {
		t.Error("expected Wait() to return true — Stop() cancels in-flight fetches via shutdownCtx")
	}

	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Errorf("Stop() took %v to cancel in-flight fetch (expected < 1s)", elapsed)
	}

	select {
	case <-done:
	default:
		t.Fatal("StartScheduledFetch goroutine did not exit after Stop()")
	}

	// Verify the HTTP request was cancelled (server should not have completed it).
	select {
	case <-requestDone:
		t.Error("HTTP request should have been cancelled by shutdownCtx")
	case <-time.After(100 * time.Millisecond):
		// Expected: request not yet done (cancelled mid-flight)
	}
}

// TestFetcher_MaxFileSize tests that files exceeding maxFileSize are rejected.
func TestFetcher_MaxFileSize(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "\"test-etag\"")
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)

		hdr := &tar.Header{
			Name: "oversized/file.bin",
			Mode: 0644,
			Size: int64(maxFileSize + 1),
		}
		largeContent := make([]byte, maxFileSize+1)
		for i := range largeContent {
			largeContent[i] = 'A'
		}

		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("failed to write tar header: %v", err)
		}
		if _, err := tw.Write(largeContent); err != nil {
			t.Fatalf("failed to write large content: %v", err)
		}

		tw.Close()
		gw.Close()

		w.Header().Set("Content-Type", "application/gzip")
		io.Copy(w, &buf)
	}))
	defer ts.Close()

	f := NewFetcher(t.TempDir(), ts.URL, 24*time.Hour)
	err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for oversized file, got nil")
	}

	if !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Errorf("expected 'exceeds maximum size' error, got: %v", err)
	}
}

// TestNewFetcher_NegativeInterval tests that negative intervals are normalized to default.
func TestNewFetcher_NegativeInterval(t *testing.T) {
	f := NewFetcher(t.TempDir(), "", -1*time.Hour)
	if f.refreshInterval != 24*time.Hour {
		t.Errorf("expected default interval for negative input, got: %v", f.refreshInterval)
	}
}

// TestNewFetcher_ZeroInterval tests that zero intervals are normalized to default.
func TestNewFetcher_ZeroInterval(t *testing.T) {
	f := NewFetcher(t.TempDir(), "", 0)
	if f.refreshInterval != 24*time.Hour {
		t.Errorf("expected default interval for zero input, got: %v", f.refreshInterval)
	}
}

// TestFetch_ConcurrentSafety tests that concurrent Fetch calls are serialized.
func TestFetch_ConcurrentSafety(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("ETag", "\"test-etag\"")
		tarball := createTarball(t, map[string]string{
			"icons/game.png": "icon-content",
		})
		w.Header().Set("Content-Type", "application/gzip")
		io.Copy(w, tarball)
	}))
	defer ts.Close()

	f := NewFetcher(t.TempDir(), ts.URL, 24*time.Hour)

	var wg sync.WaitGroup
	errChan := make(chan error, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errChan <- f.Fetch(context.Background())
		}()
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		if err != nil {
			t.Errorf("unexpected error from concurrent Fetch: %v", err)
		}
	}
}
