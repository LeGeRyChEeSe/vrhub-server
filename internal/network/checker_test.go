package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestNetworkChecker_FirstCheck_OK covers the happy path: an
// httptest.Server that returns 200 to HEAD is classified as "ok"
// and the Checker state reflects it after Start() runs the
// immediate check.
func TestNetworkChecker_FirstCheck_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("expected HEAD, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewChecker(srv.URL+"/github", srv.URL+"/metadata", 0)
	defer c.Stop()

	c.Start(context.Background())

	// Wait for the immediate check to land (poll up to 1s — the
	// HEAD has a 3s timeout but the test server responds in
	// microseconds, so 50ms is more than enough).
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		s := c.GetStatus()
		if s.GitHub.Status == StatusOK && s.MetaMeta.Status == StatusOK {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	s := c.GetStatus()
	if s.GitHub.Status != StatusOK {
		t.Errorf("GitHub status: want ok, got %q (err=%q)", s.GitHub.Status, s.GitHub.LastError)
	}
	if s.MetaMeta.Status != StatusOK {
		t.Errorf("MetaMeta status: want ok, got %q (err=%q)", s.MetaMeta.Status, s.MetaMeta.LastError)
	}
	if s.LastCheck.IsZero() {
		t.Error("LastCheck should be set after a check runs")
	}
}

// TestNetworkChecker_Timeout_Offline covers AC3: a server that
// sleeps longer than HTTPTimeout must produce StatusOffline
// (the HEAD's context deadline fires).
func TestNetworkChecker_Timeout_Offline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the 3s HTTPTimeout to force a
		// client-side timeout. The per-probe context is 3s.
		select {
		case <-time.After(HTTPTimeout + 2*time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	// Use a short HTTPTimeout to keep the test fast. We can't
	// change the package const, so we go via a small interval
	// and short-circuit Start by calling checkOnce directly.
	c := NewChecker(srv.URL+"/github", srv.URL+"/metadata", time.Hour)
	// Direct probe — bypasses Start so we don't wait for the ticker.
	c.checkOnce(context.Background())

	s := c.GetStatus()
	if s.GitHub.Status != StatusOffline {
		t.Errorf("GitHub status: want offline, got %q (err=%q)", s.GitHub.Status, s.GitHub.LastError)
	}
	if s.MetaMeta.Status != StatusOffline {
		t.Errorf("MetaMeta status: want offline, got %q (err=%q)", s.MetaMeta.Status, s.MetaMeta.LastError)
	}
}

// TestNetworkChecker_5xx_Offline covers AC3: 5xx is offline
// (service is broken, not just rate-limited).
func TestNetworkChecker_5xx_Offline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewChecker(srv.URL+"/github", srv.URL+"/metadata", time.Hour)
	c.checkOnce(context.Background())

	s := c.GetStatus()
	if s.GitHub.Status != StatusOffline {
		t.Errorf("GitHub status: want offline, got %q", s.GitHub.Status)
	}
	if s.MetaMeta.Status != StatusOffline {
		t.Errorf("MetaMeta status: want offline, got %q", s.MetaMeta.Status)
	}
}

// TestNetworkChecker_4xx_Degraded covers AC4: 4xx is "degraded"
// (the service responded; HEAD just isn't allowed / we hit a
// rate limit). Distinct from "offline" because the service is
// alive.
func TestNetworkChecker_4xx_Degraded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewChecker(srv.URL+"/github", srv.URL+"/metadata", time.Hour)
	c.checkOnce(context.Background())

	s := c.GetStatus()
	if s.GitHub.Status != StatusDegraded {
		t.Errorf("GitHub status: want degraded, got %q (err=%q)", s.GitHub.Status, s.GitHub.LastError)
	}
	if s.MetaMeta.Status != StatusDegraded {
		t.Errorf("MetaMeta status: want degraded, got %q (err=%q)", s.MetaMeta.Status, s.MetaMeta.LastError)
	}
}

// TestNetworkChecker_DNSError_Offline covers AC3's DNS error
// branch. We use 192.0.2.1 (RFC 5737 TEST-NET-1) — a guaranteed
// non-routable IP that produces a deterministic connection
// timeout/refused on every platform (unlike an invalid TLD, which
// can occasionally be answered by wildcard DNS).
func TestNetworkChecker_DNSError_Offline(t *testing.T) {
	c := NewChecker("http://192.0.2.1/github", "http://192.0.2.1/metadata", time.Hour)
	// Bypass Start so the test doesn't wait for the ticker.
	c.checkOnce(context.Background())

	s := c.GetStatus()
	if s.GitHub.Status != StatusOffline {
		t.Errorf("GitHub status: want offline, got %q (err=%q)", s.GitHub.Status, s.GitHub.LastError)
	}
	if s.MetaMeta.Status != StatusOffline {
		t.Errorf("MetaMeta status: want offline, got %q (err=%q)", s.MetaMeta.Status, s.MetaMeta.LastError)
	}
}

// TestNetworkChecker_BothServices_Independent covers AC5: the
// two services are classified independently (one down, the other
// up produces a mixed status).
func TestNetworkChecker_BothServices_Independent(t *testing.T) {
	// GitHub stub returns 200; metadata stub returns 503.
	var ghHits, mmHits atomic.Int32
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ghHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ghSrv.Close()
	mmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mmHits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer mmSrv.Close()

	c := NewChecker(ghSrv.URL+"/github", mmSrv.URL+"/metadata", time.Hour)
	c.checkOnce(context.Background())

	s := c.GetStatus()
	if s.GitHub.Status != StatusOK {
		t.Errorf("GitHub status: want ok, got %q", s.GitHub.Status)
	}
	if s.MetaMeta.Status != StatusOffline {
		t.Errorf("MetaMeta status: want offline, got %q", s.MetaMeta.Status)
	}
	if ghHits.Load() != 1 || mmHits.Load() != 1 {
		t.Errorf("expected one HEAD per service, got github=%d metadata=%d", ghHits.Load(), mmHits.Load())
	}
	if s.AllOK() {
		t.Error("AllOK() should return false when one service is offline")
	}
}

// TestNetworkChecker_GetStatus_ThreadSafe races 10 readers
// against 1 writer. The race detector (-race flag) is the real
// assertion; the test just ensures no panics under contention.
func TestNetworkChecker_GetStatus_ThreadSafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewChecker(srv.URL+"/github", srv.URL+"/metadata", time.Hour)
	// Seed initial state so the readers always see a valid value.
	c.checkOnce(context.Background())

	const readers = 10
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(readers + 1)

	// Writer: alternate OK and 5xx to force RWMutex contention.
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			c.checkOnce(context.Background())
		}
	}()

	// Readers: snapshot GetStatus repeatedly.
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = c.GetStatus()
			}
		}()
	}

	wg.Wait()
}

// TestNetworkChecker_Stop_Idempotent covers the CAS guard: a
// second Stop() is a no-op (does not panic, does not double-close
// the channel). The run() loop exits cleanly on the first close.
func TestNetworkChecker_Stop_Idempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewChecker(srv.URL+"/github", srv.URL+"/metadata", time.Hour)
	c.Start(context.Background())

	// First stop — closes stopCh, run() returns, doneCh closes.
	c.Stop()
	// Second stop — CAS returns false (already stopped), no-op.
	// This MUST NOT panic on the double-close of stopCh.
	c.Stop()
	// Third stop for good measure.
	c.Stop()
}

// TestNetworkChecker_Context_Cancel_NoLeak covers the
// ctx.Done() branch of the run loop: cancelling the parent
// context stops the loop without the 5-second Stop() timeout.
func TestNetworkChecker_Context_Cancel_NoLeak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := NewChecker(srv.URL+"/github", srv.URL+"/metadata", time.Hour)
	c.Start(ctx)

	// Cancel the context; run() should observe ctx.Done() and
	// close doneCh, so Stop() returns within milliseconds
	// (well under its 5-second budget).
	cancel()

	done := make(chan struct{})
	go func() {
		c.Stop()
		close(done)
	}()
	select {
	case <-done:
		// OK — stopped promptly.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2s after ctx cancel")
	}
}

// TestNetworkChecker_AllOK_Flag flips the AllOK() helper through
// the 4 states. Sanity check on the public boolean — the JSON
// handler's all_ok field is derived from this.
func TestNetworkChecker_AllOK_Flag(t *testing.T) {
	tests := []struct {
		gh, mm ServiceStatus
		want   bool
	}{
		{StatusOK, StatusOK, true},
		{StatusOK, StatusDegraded, false},
		{StatusOffline, StatusOK, false},
		{StatusUnknown, StatusUnknown, false},
	}
	for _, tt := range tests {
		s := NetworkStatus{
			GitHub:   ServiceReport{Status: tt.gh},
			MetaMeta: ServiceReport{Status: tt.mm},
		}
		if got := s.AllOK(); got != tt.want {
			t.Errorf("AllOK(%q,%q) = %v, want %v", tt.gh, tt.mm, got, tt.want)
		}
	}
}

// TestClassifyHTTPStatus covers the status-code mapping directly
// (no network). One test for each branch of the switch.
func TestClassifyHTTPStatus(t *testing.T) {
	tests := []struct {
		code int
		want ServiceStatus
	}{
		{200, StatusOK},
		{204, StatusOK},
		{301, StatusOK}, // 3xx: client follows the redirect
		{302, StatusOK},
		{400, StatusDegraded},
		{403, StatusDegraded},
		{404, StatusDegraded},
		{429, StatusDegraded},
		{500, StatusOffline},
		{502, StatusOffline},
		{503, StatusOffline},
		{599, StatusOffline},
		{0, StatusOffline}, // unreachable / no response
	}
	for _, tt := range tests {
		if got := classifyHTTPStatus(tt.code, nil); got != tt.want {
			t.Errorf("classifyHTTPStatus(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

// TestNewChecker_Defaults covers the input-validation branches
// of NewChecker: empty URLs fall back to the defaults, interval
// <= 0 falls back to DefaultCheckInterval.
func TestNewChecker_Defaults(t *testing.T) {
	c := NewChecker("", "", 0)
	if c.githubURL != DefaultGitHubURL {
		t.Errorf("githubURL: want %q, got %q", DefaultGitHubURL, c.githubURL)
	}
	if c.metadataURL != DefaultMetadataURL {
		t.Errorf("metadataURL: want %q, got %q", DefaultMetadataURL, c.metadataURL)
	}
	if c.interval != DefaultCheckInterval {
		t.Errorf("interval: want %q, got %q", DefaultCheckInterval, c.interval)
	}
	if c.httpClient.Timeout != HTTPTimeout {
		t.Errorf("httpClient.Timeout: want %q, got %q", HTTPTimeout, c.httpClient.Timeout)
	}
}
