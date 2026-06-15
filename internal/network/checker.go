// Package network implements a passive reachability checker for
// external services the server depends on (GitHub for update checks,
// MetaMetadata for game metadata enrichment).
//
// Story 7.6: a single background goroutine polls each service at a
// configurable interval (default 60s) using a HEAD request with a
// 3-second timeout. The latest status per service is stored in a
// mutex-protected struct; the admin API exposes it via
// GET /admin/api/network-status (read-only, no mode-gate).
//
// Design notes (from story 7.6 dev-notes):
//   - HEAD only, no GET retry. 4xx is "degraded" (service responded,
//     the request was just not HEAD-able), 5xx + timeouts + DNS
//     errors are "offline".
//   - Goroutine count is exactly 1 (the periodic loop). The per-check
//     fan-out is via 2 short-lived goroutines + a WaitGroup.
//   - Stop() is idempotent (CAS via atomic.Bool) and bounded by a
//     5-second wait — the same shape as update.Checker.Stop.
//   - Zero new dependencies; net/http + sync + sync/atomic + time.
package network

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
)

// ServiceStatus is the public enum for the state of a single
// external service endpoint. Stored as a string so the JSON
// response is self-describing ("ok" / "degraded" / "offline" /
// "unknown") without an extra layer of mapping.
type ServiceStatus string

const (
	// StatusOK is 2xx / 3xx — the service responded.
	StatusOK ServiceStatus = "ok"
	// StatusDegraded is 4xx — the service responded but the
	// request was rejected (rate-limit, not-found, HEAD-not-
	// allowed). Distinct from "offline" because the service is
	// still alive.
	StatusDegraded ServiceStatus = "degraded"
	// StatusOffline is 5xx, timeout, DNS error, connection
	// refused — the service is unreachable.
	StatusOffline ServiceStatus = "offline"
	// StatusUnknown is the pre-first-check state (and the state
	// after Stop). Displayed as a muted badge in the UI.
	StatusUnknown ServiceStatus = "unknown"
)

// ServiceReport captures the latest known state of a single service
// plus a short error string (truncated by the checker) for debugging.
type ServiceReport struct {
	Status      ServiceStatus `json:"status"`
	LastChecked time.Time     `json:"last_checked"`
	LastError   string        `json:"last_error,omitempty"`
}

// NetworkStatus is the snapshot returned by Checker.GetStatus.
// The zero value is the initial state: all services "unknown".
type NetworkStatus struct {
	GitHub    ServiceReport `json:"github"`
	MetaMeta  ServiceReport `json:"metadata"`
	LastCheck time.Time     `json:"last_check"`
}

// AllOK returns true when both services are StatusOK.
func (s NetworkStatus) AllOK() bool {
	return s.GitHub.Status == StatusOK && s.MetaMeta.Status == StatusOK
}

// Default URLs and timings. Exposed as constants for tests (so a
// test can pass a custom URL pointing at httptest.Server).
const (
	// DefaultGitHubURL is the HEAD-able endpoint for the project's
	// GitHub releases. Using api.github.com avoids the 302 redirect
	// from github.com/.../releases/latest.
	DefaultGitHubURL = "https://api.github.com/repos/LeGeRyChEeSe/vrhub-server/releases/latest"
	// DefaultMetadataURL is the HEAD-able endpoint for the
	// MetaMetadata tarball on GitHub releases.
	DefaultMetadataURL = "https://github.com/threethan/MetaMetadata/archive/refs/heads/main.tar.gz"

	// DefaultCheckInterval is the gap between consecutive checks
	// (60s — anonymous GitHub rate limit is 60 req/h, so 1 req/60s
	// stays well under the limit even with the 2 services).
	DefaultCheckInterval = 60 * time.Second

	// HTTPTimeout caps a single check at 3s; the headroom above
	// the canonical 2xx response is intentional (some CDN paths
	// add 1s of TLS handshake on cold cache).
	HTTPTimeout = 3 * time.Second

	// userAgent is required by GitHub (rejects empty UA with 403).
	userAgent = "vrhub-server/0.1.0"
)

// Checker is the long-lived background reachability checker.
// One instance per process; constructed by main.go and shared
// with the AdminHandler.
type Checker struct {
	githubURL   string
	metadataURL string
	interval    time.Duration
	httpClient  *http.Client

	// status holds the latest NetworkStatus. mu protects the
	// embedded struct fields; readers (GetStatus, the API
	// handler) take RLock, the per-check writer takes Lock.
	mu     sync.RWMutex
	status NetworkStatus

	// started is a CAS guard for Start (mirrors update.Checker's
	// pattern): only the first Start() spins up the goroutine.
	started atomic.Bool
	// shutdown is the same CAS guard for Stop().
	shutdown atomic.Bool
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// NewChecker constructs a Checker. interval <= 0 falls back to
// DefaultCheckInterval; empty URLs fall back to the defaults. The
// http.Client timeout is always HTTPTimeout (3s) so the dev-notes'
// "per-check timeout strict" contract is enforced.
func NewChecker(githubURL, metadataURL string, interval time.Duration) *Checker {
	if githubURL == "" {
		githubURL = DefaultGitHubURL
	}
	if metadataURL == "" {
		metadataURL = DefaultMetadataURL
	}
	if interval <= 0 {
		interval = DefaultCheckInterval
	}
	return &Checker{
		githubURL:   githubURL,
		metadataURL: metadataURL,
		interval:    interval,
		httpClient:  &http.Client{Timeout: HTTPTimeout},
		// Seed the initial state to "unknown" so the JSON
		// response (and the UI badge) never has to special-case
		// the empty-string zero value. The first checkOnce()
		// overwrites this within microseconds in production;
		// in tests that call NewChecker() without Start(), the
		// state is still a valid "unknown" snapshot.
		status: NetworkStatus{
			GitHub:   ServiceReport{Status: StatusUnknown},
			MetaMeta: ServiceReport{Status: StatusUnknown},
		},
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start launches the background loop: one immediate check (so the
// UI sees a real status on first poll, not "unknown"), then a
// ticker at Checker.interval. Idempotent: a second Start() is a
// no-op (CAS).
//
// The loop exits on stopCh or ctx.Done(); doneCh is closed on exit
// so Stop() can wait with a bounded timeout.
func (c *Checker) Start(ctx context.Context) {
	if !c.started.CompareAndSwap(false, true) {
		// Already started — no-op (matches update.Checker pattern).
		return
	}

	logger := vlog.Get()
	logger.Info().
		Str("github_url", c.githubURL).
		Str("metadata_url", c.metadataURL).
		Dur("interval", c.interval).
		Msg("Network checker: starting")

	go c.run(ctx)
}

// run is the main loop. Lives in its own goroutine so Start() can
// return immediately to the caller.
func (c *Checker) run(ctx context.Context) {
	logger := vlog.Get()
	defer close(c.doneCh)

	// Defensive: a nil parent context (test wiring that calls
	// Start(nil) by accident) should fall back to Background so
	// the per-probe context.WithTimeout call doesn't panic with
	// "cannot create context from nil parent". The run loop then
	// observes only the stopCh (no ctx.Done() branch can fire).
	if ctx == nil {
		ctx = context.Background()
	}

	// Immediate check at startup (per AC1).
	c.checkOnce(ctx)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.checkOnce(ctx)
		case <-c.stopCh:
			logger.Info().Msg("Network checker: stopped")
			return
		case <-ctx.Done():
			logger.Info().Msg("Network checker: context cancelled")
			return
		}
	}
}

// Stop is idempotent (CAS). Bounded by a 5-second wait so a stuck
// in-flight check doesn't hang shutdown forever.
func (c *Checker) Stop() {
	if !c.shutdown.CompareAndSwap(false, true) {
		// Already stopped or stopping — no-op.
		return
	}
	close(c.stopCh)
	select {
	case <-c.doneCh:
	case <-time.After(5 * time.Second):
		vlog.Get().Warn().Msg("Network checker: stop timed out")
	}
}

// GetStatus returns a snapshot of the latest NetworkStatus. Safe
// for concurrent use (RWMutex.RLock on the read path).
func (c *Checker) GetStatus() NetworkStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// checkOnce performs a single round of HEAD requests in parallel
// (one goroutine per service) and writes the combined result under
// a single Lock. Each per-service check has its own context
// derived from the parent with HTTPTimeout so a slow GitHub never
// starves the metadata check (or vice versa).
func (c *Checker) checkOnce(parent context.Context) {
	logger := vlog.Get()

	// Defensive: nil parent context (direct call from test
	// wiring) → fall back to Background so context.WithTimeout
	// doesn't panic.
	if parent == nil {
		parent = context.Background()
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Local captures — written by the goroutines, read after wg.Wait().
	var ghReport, mmReport ServiceReport

	go func() {
		defer wg.Done()
		ghReport = c.probe(parent, "github", c.githubURL)
	}()
	go func() {
		defer wg.Done()
		mmReport = c.probe(parent, "metadata", c.metadataURL)
	}()
	wg.Wait()

	now := time.Now()
	c.mu.Lock()
	c.status = NetworkStatus{
		GitHub:    ghReport,
		MetaMeta:  mmReport,
		LastCheck: now,
	}
	c.mu.Unlock()

	logger.Debug().
		Str("github", string(ghReport.Status)).
		Str("metadata", string(mmReport.Status)).
		Time("checked_at", now).
		Msg("Network checker: check complete")
}

// probe issues a single HEAD request and classifies the result.
// Always returns a ServiceReport — even on panic (the deferred
// recover below catches any unexpected runtime error and returns
// StatusUnknown rather than crashing the loop).
func (c *Checker) probe(parent context.Context, name, url string) (report ServiceReport) {
	logger := vlog.Get()
	report.LastChecked = time.Now()

	defer func() {
		if r := recover(); r != nil {
			logger.Error().
				Interface("panic", r).
				Str("service", name).
				Msg("Network checker: probe panic recovered")
			report.Status = StatusUnknown
			report.LastError = "internal panic"
		}
	}()

	// Per-probe context so a slow link doesn't leak past the
	// per-check HTTPTimeout (and so a parent ctx cancellation
	// during shutdown still terminates the probe cleanly).
	ctx, cancel := context.WithTimeout(parent, HTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		report.Status = StatusOffline
		report.LastError = truncate(err.Error(), 200)
		return
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		report.Status = StatusOffline
		report.LastError = truncate(err.Error(), 200)
		return
	}
	// Always close the body — even on HEAD the response has a
	// (small) body that must be drained for connection reuse.
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	report.Status = classifyHTTPStatus(resp.StatusCode, nil)
	if report.Status == StatusDegraded || report.Status == StatusOffline {
		report.LastError = "HTTP " + http.StatusText(resp.StatusCode)
	}
	return
}

// classifyHTTPStatus maps an HTTP status code (or error) to a
// ServiceStatus. The err parameter is reserved for future use
// (e.g., distinguishing DNS errors from timeouts in the error
// string); today err is always nil at the call site because the
// caller handles error != nil before classifying the status.
func classifyHTTPStatus(code int, _ error) ServiceStatus {
	switch {
	case code >= 200 && code < 400:
		// 2xx success + 3xx redirect-followed (Go's http.Client
		// follows up to 10 redirects by default).
		return StatusOK
	case code >= 400 && code < 500:
		// 4xx: service responded, our request was rejected
		// (HEAD-not-allowed, rate-limit, auth missing).
		return StatusDegraded
	default:
		// 5xx or any out-of-range code: treat as offline so
		// the operator sees a single red state, not a yellow
		// warning that hides a real outage.
		return StatusOffline
	}
}

// truncate caps the error string stored in ServiceReport.LastError
// so a 1 KB DNS error doesn't bloat the status struct.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
