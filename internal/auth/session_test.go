package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	"golang.org/x/crypto/bcrypt"
)

// testBcryptCost is the cost factor used for ALL test bcrypt hashes in this package.
// It MUST match dummyBcryptHash's cost (resolved via resolveDummyBcrypt) so the
// timing-oracle defense test can validate real-vs-dummy timing symmetry.
const testBcryptCost = bcrypt.DefaultCost // cost 10 — matches resolveDummyBcrypt seeding.

// testPasswordHash is a precomputed bcrypt hash for "hunter2" using testBcryptCost
// (NOT MinCost). MinCost (cost 4) is ~250x faster than DefaultCost (cost 10), which
// silently breaks the timing-oracle test by making the real path orders of magnitude
// faster than the dummy path. Tests pay a one-time ~50ms cost at package init.
var testPasswordHash = func() string {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), testBcryptCost)
	if err != nil {
		panic("session_test: failed to generate test password hash: " + err.Error())
	}
	return string(hash)
}()

// failingReader implements io.Reader and always returns an error.
// Used by TestGenerateSessionID_Error to exercise the error path.
type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) {
	return 0, errors.New("simulated crypto/rand failure")
}

func TestSessionStore_CreateAndGet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := NewSessionStore(ctx)
	defer store.Stop()

	session := store.Create("testuser")
	if session == nil {
		t.Fatal("Create returned nil")
	}
	if session.Username != "testuser" {
		t.Errorf("Username = %q, want %q", session.Username, "testuser")
	}
	if len(session.ID) != 64 {
		t.Errorf("Session ID length = %d, want 64 (32 bytes hex)", len(session.ID))
	}
	if session.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
	if session.ExpiresAt.Before(session.CreatedAt) {
		t.Error("ExpiresAt should be after CreatedAt")
	}

	got := store.Get(session.ID)
	if got == nil {
		t.Fatal("Get returned nil for existing session")
	}
	if got.Username != "testuser" {
		t.Errorf("Got username = %q, want %q", got.Username, "testuser")
	}

	// Non-existent ID should return nil.
	if store.Get("nonexistent-id") != nil {
		t.Error("Get should return nil for non-existent session")
	}
}

func TestSessionStore_Touch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := NewSessionStore(ctx)
	defer store.Stop()

	session := store.Create("testuser")
	oldLastSeen := session.LastSeenAt

	time.Sleep(10 * time.Millisecond)

	ok := store.Touch(session.ID)
	if !ok {
		t.Error("Touch should return true for existing session")
	}

	got := store.Get(session.ID)
	if got == nil {
		t.Fatal("Get returned nil after Touch")
	}
	if got.LastSeenAt.Equal(oldLastSeen) {
		t.Error("LastSeenAt should have been updated by Touch")
	}
	if got.ExpiresAt.Before(got.LastSeenAt) {
		t.Error("ExpiresAt should be after LastSeenAt after Touch")
	}

	// Touch non-existent session.
	if store.Touch("nonexistent") {
		t.Error("Touch should return false for non-existent session")
	}
}

func TestSessionStore_Delete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := NewSessionStore(ctx)
	defer store.Stop()

	session := store.Create("testuser")

	ok := store.Delete(session.ID)
	if !ok {
		t.Error("Delete should return true for existing session")
	}

	if store.Get(session.ID) != nil {
		t.Error("Get should return nil after Delete")
	}

	// Delete non-existent session.
	if store.Delete("nonexistent") {
		t.Error("Delete should return false for non-existent session")
	}
}

func TestSessionStore_JanitorEvictsExpired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := NewSessionStore(ctx)
	defer store.Stop()

	// Create a session with a very short lifetime by directly manipulating the map.
	id, _ := generateSessionID()
	now := time.Now()
	store.mu.Lock()
	store.sessions[id] = &Session{
		ID:         id,
		Username:   "expired-user",
		CreatedAt:  now.Add(-2 * time.Hour),
		LastSeenAt: now.Add(-2 * time.Hour),
		ExpiresAt:  now.Add(-1 * time.Second), // expired 1 second ago
	}
	store.mu.Unlock()

	// Force eviction directly (the real ticker runs hourly).
	store.evictExpired()

	if store.Get(id) != nil {
		t.Error("Janitor should have evicted the expired session")
	}
}

// TestSessionStore_ConcurrentAccess exercises the store under concurrent
// Create/Get/Touch/Delete pressure. Run with `go test -race` for full coverage —
// the test itself does not require -race to pass, but lock-ordering bugs are
// only visible to the race detector.
//
// R10-CONCURRENT-TEST-LENIENT: the previous assertion `count < createdCount-1`
// allowed data loss (a wiggle of -1). The new assertion tracks every created
// ID and asserts each one is either still in the map OR was explicitly deleted
// by the deleter goroutines. No silent data loss.
func TestSessionStore_ConcurrentAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := NewSessionStore(ctx)
	defer store.Stop()

	const goroutines = 50
	const opsPerGoroutine = 20

	// createdIDs records every session ID returned by Create (success only).
	// After the test we assert each one is still present in the map (since
	// the deleter goroutines use random IDs that won't collide with the
	// 64-hex-char generated IDs — collision probability is ~2^-256).
	createdIDs := make(chan string, goroutines*opsPerGoroutine)

	// deletedIDs records every ID passed to Delete. We use a separate channel
	// for symmetry with createdIDs, then assert (createdIDs ∩ deletedIDs) = ∅
	// (or, in the worst case of a collision, the test still passes if the
	// session was correctly removed by the deleter).
	deletedIDs := make(chan string, goroutines*opsPerGoroutine)

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				sess := store.Create("user" + string(rune('a'+n%26)))
				if sess != nil {
					createdIDs <- sess.ID
				}
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				id, _ := generateSessionID()
				deletedIDs <- id
				store.Get(id)
				store.Touch(id)
				store.Delete(id)
			}
		}()
	}
	wg.Wait()
	close(createdIDs)
	close(deletedIDs)

	// Build a set of deleted IDs (random hex collisions with created IDs
	// have probability ~2^-256, so the set is effectively disjoint).
	deletedSet := make(map[string]struct{}, goroutines*opsPerGoroutine)
	for id := range deletedIDs {
		deletedSet[id] = struct{}{}
	}

	// Read the final map state.
	store.mu.RLock()
	finalMap := make(map[string]struct{}, len(store.sessions))
	for id := range store.sessions {
		finalMap[id] = struct{}{}
	}
	store.mu.RUnlock()

	// For every created ID, assert it is present in the final map (since the
	// deleter goroutines used random IDs that cannot collide). If any created
	// ID is missing, that's a data-loss bug (R10-CONCURRENT-TEST-LENIENT).
	missing := 0
	for id := range createdIDs {
		if _, isDeleted := deletedSet[id]; isDeleted {
			// The deleter goroutine happened to use the same ID. The Create
			// happened-before, so the Delete may have legitimately removed
			// the entry. Skip — the ordering is racy by design.
			continue
		}
		if _, present := finalMap[id]; !present {
			missing++
		}
	}

	if missing > 0 {
		t.Errorf("concurrent test: %d created sessions are missing from the final map (data loss / race)", missing)
	}
}

func TestGenerateSessionID_Uniqueness(t *testing.T) {
	ids := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id, err := generateSessionID()
		if err != nil {
			t.Fatalf("generateSessionID failed: %v", err)
		}
		if len(id) != 64 {
			t.Errorf("ID length = %d, want 64", len(id))
		}
		if ids[id] {
			t.Errorf("Duplicate session ID generated: %s", id)
		}
		ids[id] = true
	}
}

// TestGenerateSessionID_Error verifies that a failing random source surfaces as
// an error (not a panic). The withRandReader helper injects a failingReader for
// the duration of the call.
func TestGenerateSessionID_Error(t *testing.T) {
	var (
		gotID  string
		gotErr error
	)
	withRandReader(failingReader{}, func() {
		gotID, gotErr = generateSessionID()
	})
	if gotErr == nil {
		t.Fatal("expected error when randReader fails, got nil")
	}
	if gotID != "" {
		t.Errorf("expected empty id on error, got %q", gotID)
	}
	if !strings.Contains(gotErr.Error(), "random source failed") {
		t.Errorf("error message = %q, expected to mention random source failure", gotErr.Error())
	}
}

func TestSessionStore_Create_AfterStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := NewSessionStore(ctx)
	store.Stop()

	session := store.Create("testuser")
	if session != nil {
		t.Error("Create should return nil after Stop()")
	}
}

// TestSessionStore_SessionCap_FIFOEviction is the S-03 regression
// gate. The session count cap (10000 by default) prevents an
// attacker with valid credentials from filling the map with
// millions of sessions over the 7-day session lifetime. When the
// cap is reached, the oldest non-target session is evicted to
// make room for the new one.
//
// Test design: directly mutate the internal cap via a fresh
// SessionStore and inject 100 sessions + 1. Verify the oldest
// session (by CreatedAt) is gone and the count is at 100.
func TestSessionStore_SessionCap_FIFOEviction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := NewSessionStore(ctx)
	defer store.Stop()

	// We can't easily set the cap from outside the package, so
	// fill the store up to the documented cap (10000) — too
	// slow for a unit test. Instead, exercise the eviction logic
	// by stuffing the map up to (cap - 1) then adding one more.
	// We use a helper that bypasses the cap (for test only).
	const cap = 10000

	// Pre-populate the store to (cap - 1) via the cap-aware path:
	// directly inject (cap-2) "old" sessions, then verify one
	// more Create triggers eviction of the oldest.
	oldestID := ""
	oldestTime := time.Now().Add(-24 * time.Hour) // very old
	store.mu.Lock()
	store.sessions["old-session-1"] = &Session{
		ID: "old-session-1", Username: "u", CreatedAt: oldestTime, ExpiresAt: time.Now().Add(time.Hour),
	}
	store.sessions["old-session-2"] = &Session{
		ID: "old-session-2", Username: "u", CreatedAt: oldestTime.Add(time.Second), ExpiresAt: time.Now().Add(time.Hour),
	}
	oldestID = "old-session-1"
	store.mu.Unlock()

	// Fill up to the cap (minus 1, so the next Create triggers the path).
	for i := 0; i < cap-2-len(store.sessions); i++ {
		// This loop is huge. To keep the test fast, directly
		// inject fake sessions to the cap.
		_ = i
	}
	// Direct-inject the rest to avoid creating bcrypt-costly sessions.
	store.mu.Lock()
	for i := 0; i < cap-2; i++ {
		id := generateFakeSessionIDForTest()
		store.sessions[id] = &Session{
			ID: id, Username: "u", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		}
	}
	store.mu.Unlock()

	// Now the store has cap+ entries (counting the 2 "old" ones + cap-2 fakes = cap).
	// The next Create should evict the oldest.
	beforeCount := len(store.sessions)
	if beforeCount < cap {
		t.Fatalf("test setup error: store has %d sessions, expected >= %d", beforeCount, cap)
	}

	// Create one more — this should evict "old-session-1" (the oldest).
	newSession := store.Create("attacker")
	if newSession == nil {
		t.Fatal("Create returned nil after cap reached; expected eviction to make room")
	}
	_ = oldestID

	// Verify the oldest session is gone.
	store.mu.Lock()
	_, oldestStillThere := store.sessions["old-session-1"]
	count := len(store.sessions)
	store.mu.Unlock()

	if oldestStillThere {
		t.Error("oldest session should have been evicted (FIFO)")
	}
	if count != cap {
		t.Errorf("session count after Create = %d, want %d (cap, after eviction)", count, cap)
	}
}

// generateFakeSessionIDForTest returns a random 64-hex ID for
// test injection into the session map. Exported only within the
// package (lowercase). Not used in production.
func generateFakeSessionIDForTest() string {
	id, _ := generateSessionID()
	return id
}

func TestSessionStore_Touch_ExpiresExpired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := NewSessionStore(ctx)
	defer store.Stop()

	id, _ := generateSessionID()
	now := time.Now()
	store.mu.Lock()
	store.sessions[id] = &Session{
		ID:         id,
		Username:   "expired-user",
		CreatedAt:  now.Add(-8 * time.Hour),
		LastSeenAt: now.Add(-8 * time.Hour),
		ExpiresAt:  now.Add(-1 * time.Hour), // expired 1 hour ago
	}
	store.mu.Unlock()

	ok := store.Touch(id)
	if ok {
		t.Error("Touch should return false for expired session")
	}

	// Verify the expired session was removed from the map.
	store.mu.RLock()
	_, exists := store.sessions[id]
	store.mu.RUnlock()
	if exists {
		t.Error("Expired session should have been removed by Touch")
	}
}

// TestSessionStore_Stop_WaitsForJanitor exercises the WaitGroup contract in
// the R12-P1 design: the parent janitor goroutine holds the single +1, and
// Stop's wg.Wait() must block until the loop has returned.
//
// R12-P1: the previous design had a separate runJanitorTick with body-level
// wg.Add(1)/Done() so external callers' ticks would be tracked. The new
// design removes that body-level Add/Done to eliminate the check-then-act
// race; the WaitGroup is only +1'd by the parent janitor goroutine. The
// observable contract for the new design:
//  1. runJanitorTick must not panic when called on a closed store
//     (R11-CRITICAL-2 original purpose, still valid).
//  2. Stop must wait for the parent janitor goroutine to exit before
//     returning (caller's wg.Wait contract).
func TestSessionStore_Stop_WaitsForJanitor(t *testing.T) {
	store := NewSessionStore(context.Background())

	// Manually drive a tick in a goroutine. With R12-P1, this no longer
	// touches the WaitGroup (the body just runs panic-recovered work).
	// The test now asserts the R11-CRITICAL-2 contract: the body must
	// complete without panicking, even if Stop is called concurrently.
	hookEntered := make(chan struct{})
	hookRelease := make(chan struct{})
	store.mu.Lock()
	store.janitorHook = func() {
		close(hookEntered)
		<-hookRelease
	}
	store.mu.Unlock()

	tickDone := make(chan struct{})
	go func() {
		store.runJanitorTick()
		close(tickDone)
	}()

	// Wait for the body to enter the hook.
	select {
	case <-hookEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("janitor tick did not enter hook within 2s")
	}

	// Stop the store from another goroutine. With R12-P1, the in-flight
	// tick does NOT hold a WaitGroup +1 (body no longer Add/Done), so
	// Stop's wg.Wait() returns as soon as the parent janitor goroutine
	// exits (the janitor's `<-ctx.Done()` fires when Stop calls cancel()).
	stopDone := make(chan struct{})
	go func() {
		store.Stop()
		close(stopDone)
	}()

	// Stop should return promptly (no tick on the WaitGroup to wait for).
	select {
	case <-stopDone:
		// Good — Stop returned without blocking on the tick.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s; parent janitor may be stuck")
	}

	// Release the hook so the tick goroutine completes, then assert it
	// completed cleanly (no panic, no WaitGroup reuse).
	close(hookRelease)
	select {
	case <-tickDone:
		// Good — tick completed without panic.
	case <-time.After(2 * time.Second):
		t.Fatal("janitor tick did not complete cleanly post-Stop")
	}
}

// TestSessionStore_Stop_Idempotent verifies that calling Stop multiple times
// does not panic, double-close the cancel func, or double-wipe the map.
func TestSessionStore_Stop_Idempotent(t *testing.T) {
	store := NewSessionStore(context.Background())
	store.Stop()
	store.Stop() // must not panic
	store.Stop() // must not panic
	if !store.IsClosed() {
		t.Error("IsClosed should report true after Stop")
	}
}

func TestIsJSONRequest_Direct(t *testing.T) {
	tests := []struct {
		name     string
		accept   string
		xrw      string
		expected bool
	}{
		{"no accept header", "", "", false},
		{"application/json only", "application/json", "", true},
		{"text/html only", "text/html", "", false},
		{"application/json with q=0 (not acceptable)", "application/json; q=0", "", false},
		{"q=0 for json, text/html present", "application/json; q=0, text/html", "", false},
		{"json q=0.5, html q=1", "application/json; q=0.5, text/html", "", false},
		{"json q=1, html q=0.5", "text/html; q=0.5, application/json", "", true},
		{"*/* with X-Requested-With: fetch", "*/*", "fetch", true},
		{"*/* with X-Requested-With: XMLHttpRequest", "*/*", "XMLHttpRequest", true},
		{"*/* with attacker-controlled X-Requested-With", "*/*", "../../etc/passwd", false},
		{"*/* without X-Requested-With", "*/*", "", false},
		{"application/* with X-Requested-With: fetch", "application/*", "fetch", true},
		{"application/jsonp (should NOT match)", "application/jsonp", "", false},
		{"application/json5 (should NOT match)", "application/json5", "", false},
		{"text/htmlfoo (should NOT match HTML)", "text/htmlfoo", "", false},
		{"text/html; q=0, application/json", "text/html; q=0, application/json", "", true},
		{"text/html; q=0.5, application/json; q=0.5 (tie → HTML)", "text/html; q=0.5, application/json; q=0.5", "", false},
		{"application/json; q=1.5 (out of range → ignored)", "application/json; q=1.5", "", false},
		{"application/json; q=-0.1 (negative → ignored)", "application/json; q=-0.1", "", false},
		{"application/json; q=garbage (unparseable → ignored)", "application/json; q=garbage", "", false},
		{"application/json; charset=utf-8", "application/json; charset=utf-8", "", true},
		{"application/hal+json (should NOT match)", "application/hal+json", "", false},
		{"application/ld+json (should NOT match)", "application/ld+json", "", false},
		{"text/html; charset=utf-8", "text/html; charset=utf-8", "", false},
		{"mixed-case Application/JSON", "Application/JSON", "", true},
		{"mixed-case Text/HTML", "Text/HTML", "", false},
		{"leading comma", ",application/json", "", true},
		{"trailing comma", "application/json,", "", true},
		{"doubled comma", "application/json,,text/html", "", false},
		{"malformed part ignored (text/html wins at equal q)", "invalid, text/html, application/json", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			if tc.xrw != "" {
				req.Header.Set("X-Requested-With", tc.xrw)
			}
			got := IsJSONRequest(req)
			if got != tc.expected {
				t.Errorf("IsJSONRequest() = %v, want %v (accept=%q, xrw=%q)", got, tc.expected, tc.accept, tc.xrw)
			}
		})
	}
}

func TestSessionFromContext(t *testing.T) {
	ctx := context.Background()
	_, ok := SessionFromContext(ctx)
	if ok {
		t.Error("SessionFromContext on empty ctx should return false")
	}

	store := NewSessionStore(context.Background())
	defer store.Stop()
	session := store.Create("testuser")

	ctx = context.WithValue(context.Background(), sessionContextKey{}, session)
	got, ok := SessionFromContext(ctx)
	if !ok {
		t.Fatal("SessionFromContext should return true for valid ctx")
	}
	if got != session {
		t.Error("SessionFromContext returned wrong session")
	}

	// Wrong-type case: someone stuffs a non-*Session under the key.
	// Accessor must reject rather than panic on the type assertion.
	bogus := context.WithValue(context.Background(), sessionContextKey{}, "not a session")
	_, ok = SessionFromContext(bogus)
	if ok {
		t.Error("SessionFromContext should return false when ctx value is wrong type")
	}
}

// TestAuthenticate_TimingOracleDefense verifies the wall-clock duration of a
// wrong-username call is within 50%-200% of a wrong-password call. Both paths
// MUST incur bcrypt cost; if the wrong-username path early-returns, the ratio
// will be << 0.5 and the test fails.
//
// Bcrypt cost matching: testPasswordHash uses testBcryptCost (DefaultCost=10);
// the dummy hash resolved by resolveDummyBcrypt also matches that cost. If the
// costs diverge, the test fails (which is the point — divergence IS the bug).
func TestAuthenticate_TimingOracleDefense(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped in -short mode")
	}
	cfg := &types.Config{
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: testPasswordHash,
		},
	}

	// Warm-up: prime the dummy bcrypt cache so the first Authenticate doesn't
	// pay the one-time GenerateFromPassword cost during measurement.
	Authenticate(cfg, "warmup", "warmup")

	const iterations = 5

	measure := func(username string) time.Duration {
		// Run multiple iterations and take the median to reduce GC/scheduler noise.
		var total time.Duration
		for i := 0; i < iterations; i++ {
			start := time.Now()
			Authenticate(cfg, username, "anything")
			total += time.Since(start)
		}
		return total / iterations
	}

	durationWrongUser := measure("wronguser")
	durationCorrectUser := measure("admin")

	if durationWrongUser == 0 {
		t.Fatalf("wrong-user duration is zero — bcrypt was clearly skipped (timing oracle present)")
	}
	if durationCorrectUser == 0 {
		t.Fatalf("correct-user duration is zero — bcrypt was clearly skipped")
	}

	ratio := float64(durationCorrectUser) / float64(durationWrongUser)
	// Real-world variance can be wide on CI; we use 0.5x to 2x as the
	// "indistinguishable enough" bound. A real timing oracle would land
	// the wrong-user case at sub-millisecond while the correct-user case
	// stays at bcrypt-cost (tens of ms) → ratio of 30+ → test fails.
	if ratio < 0.5 || ratio > 2.0 {
		t.Errorf("Authenticate timing ratio = %.2f (wrong-user=%s, correct-user=%s); want between 0.5 and 2.0",
			ratio, durationWrongUser, durationCorrectUser)
	}

	// Sanity: both branches must return false.
	if Authenticate(cfg, "wronguser", "hunter2") {
		t.Error("Authenticate should return false for wrong username")
	}
	if Authenticate(cfg, "admin", "wrongpassword") {
		t.Error("Authenticate should return false for wrong password")
	}
}

// TestAuthenticate_EmptyConfig_RunsDummyBcrypt verifies that a nil/empty config
// still incurs bcrypt cost (no fast-path enumeration oracle between
// "no admin configured" and "admin present, wrong creds").
func TestAuthenticate_EmptyConfig_RunsDummyBcrypt(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped in -short mode")
	}
	// Warm up the dummy.
	cfgReal := &types.Config{Admin: types.AdminConfig{Username: "admin", PasswordHash: testPasswordHash}}
	Authenticate(cfgReal, "warm", "warm")

	measure := func(cfg *types.Config) time.Duration {
		var total time.Duration
		const iters = 3
		for i := 0; i < iters; i++ {
			start := time.Now()
			Authenticate(cfg, "user", "pass")
			total += time.Since(start)
		}
		return total / iters
	}

	durationNilCfg := measure(nil)
	durationEmptyHash := measure(&types.Config{Admin: types.AdminConfig{Username: "admin"}})
	durationRealMiss := measure(cfgReal) // wrong user, real cfg

	// All three branches must take at least 10ms (bcrypt MinCost is ~1ms;
	// DefaultCost is ~50ms; we use 10ms as a conservative floor).
	const floor = 10 * time.Millisecond
	if durationNilCfg < floor {
		t.Errorf("nil config Authenticate took %s; expected >= %s (bcrypt cost should be incurred)", durationNilCfg, floor)
	}
	if durationEmptyHash < floor {
		t.Errorf("empty-hash Authenticate took %s; expected >= %s (bcrypt cost should be incurred)", durationEmptyHash, floor)
	}
	if durationRealMiss < floor {
		t.Errorf("real-cfg wrong-user Authenticate took %s; expected >= %s", durationRealMiss, floor)
	}
}

func TestIsLoopback_Extended(t *testing.T) {
	tests := []struct {
		host     string
		expected bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.1:8080", true},
		{"localhost", true},
		{"localhost:8080", true},
		{"::1", true},
		{"[::1]", true},
		{"[::1]:8080", true},
		{"0.0.0.0", false},
		{"0.0.0.0:8080", false},
		{"192.168.1.1", false},
		{"10.0.0.1", false},
		{"::ffff:127.0.0.1", true},        // IPv4-mapped IPv6
		{"[::ffff:127.0.0.1]:8080", true}, //
		{"LOCALHOST", true},               // case-insensitive
		{"LocalHost:8080", true},          // case-insensitive with port
		{"", true},                        // empty host → safe default loopback
		{"  127.0.0.1  ", true},           // whitespace tolerated
		{"localhost:8080:8080", false},    // typo — multiple ports, not loopback
		{"fe80::1%eth0", false},           // link-local IPv6 with zone, not loopback
		{"[fe80::1%eth0]:8080", false},    // bracketed link-local, not loopback
		{"example.com", false},            // hostname is not loopback
		{"example.com:8080", false},       // hostname with port
	}

	for _, tc := range tests {
		got := isLoopback(tc.host)
		if got != tc.expected {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.host, got, tc.expected)
		}
	}
}

// TestIsLoopback_MalformedBracketDoesNotPanic is the R12-P7 regression
// gate for R11-HIGH-2: a malformed bracketed host (e.g. "[::1" with a
// missing trailing ]) must not panic. The previous isLoopback branch
// was reachable only via malformed operator config and the resulting
// Warn log flooded the log on every protected request. The new code
// wraps the Warn in sync.Once and uses best-effort strip.
//
// We don't directly assert the once-only log behavior (the package-level
// sync.Once can't be reset between tests). Instead we assert the
// function returns without panic for the malformed input and produces
// a sensible classification (best-effort strip yields "::1" for
// "[::1" — which is loopback).
func TestIsLoopback_MalformedBracketDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("isLoopback panicked on malformed bracket: %v", r)
		}
	}()
	// "[::1" best-effort strips the leading "[" → "::1" → valid loopback.
	// The function correctly classifies this; the WARN is logged once.
	if got := isLoopback("[::1"); !got {
		t.Errorf("isLoopback(\"[::1\") = false, want true (best-effort strip yields valid loopback ::1)")
	}
	// "[fe80::1" best-effort strips the leading "[" → "fe80::1" → link-local
	// (not loopback). Asserting the function classifies this correctly.
	if got := isLoopback("[fe80::1"); got {
		t.Errorf("isLoopback(\"[fe80::1\") = true, want false (link-local, not loopback)")
	}
}

func TestSetSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()
	expires := time.Now().Add(7 * 24 * time.Hour)

	// Story 9.7 (B7): SetSessionCookie now takes *http.Request. Use an HTTP
	// request (r.TLS == nil) to assert the non-Secure path.
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/admin/", nil)
	SetSessionCookie(w, r, "test-session-id", expires)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Expected 1 cookie, got %d", len(cookies))
	}

	cookie := cookies[0]
	if cookie.Name != SessionCookieName {
		t.Errorf("Cookie name = %q, want %q", cookie.Name, SessionCookieName)
	}
	if cookie.Value != "test-session-id" {
		t.Errorf("Cookie value = %q, want %q", cookie.Value, "test-session-id")
	}
	if cookie.Path != "/" {
		t.Errorf("Cookie path = %q, want %q", cookie.Path, "/")
	}
	if !cookie.HttpOnly {
		t.Error("Cookie should be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want %v", cookie.SameSite, http.SameSiteLaxMode)
	}
	// Secure should NOT be set when the request is plain HTTP.
	if cookie.Secure {
		t.Error("Cookie should NOT have Secure flag for plain-HTTP request")
	}
}

// TestSetSessionCookie_SecureFlag_HTTPS — Story 9.7 (B7): Secure is now driven
// by transport (r.TLS != nil), not by host loopback status. A direct HTTPS
// request to a loopback host gets Secure=true.
func TestSetSessionCookie_SecureFlag(t *testing.T) {
	w := httptest.NewRecorder()
	expires := time.Now().Add(7 * 24 * time.Hour)

	r := httptest.NewRequest(http.MethodGet, "https://127.0.0.1:8443/admin/", nil)
	r.TLS = &tls.ConnectionState{}
	SetSessionCookie(w, r, "test-session-id", expires)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Expected 1 cookie, got %d", len(cookies))
	}

	if !cookies[0].Secure {
		t.Error("Cookie should have Secure flag for direct HTTPS request")
	}
}

// TestSetSessionCookie_EmptyRequest — Story 9.7 (B7): the empty-host safe
// default is now automatic because httptest.NewRequest produces r.TLS == nil
// (HTTP), so isHTTPS returns false regardless of host.
func TestSetSessionCookie_EmptyHost(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	SetSessionCookie(w, r, "test-session-id", time.Now().Add(time.Hour))
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Secure {
		t.Error("Plain-HTTP request (r.TLS == nil) should produce a non-Secure cookie")
	}
}

// TestClearSessionCookie pins the Max-Age serialization to "0" (Go's
// http.Cookie.String() emits "Max-Age=0" when MaxAge is set to -1). Test no
// longer accepts both -1 and 0 — the implementation is unambiguous.
func TestClearSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()
	// Story 9.7 (B7): ClearSessionCookie now takes *http.Request. Use an
	// HTTP request to assert the non-Secure path.
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/admin/", nil)
	ClearSessionCookie(w, r)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Expected 1 cookie, got %d", len(cookies))
	}

	cookie := cookies[0]
	if cookie.Name != SessionCookieName {
		t.Errorf("Cookie name = %q, want %q", cookie.Name, SessionCookieName)
	}
	if cookie.Value != "" {
		t.Errorf("Cookie value should be empty, got %q", cookie.Value)
	}
	if cookie.MaxAge != -1 {
		t.Errorf("Cookie.MaxAge = %d, want -1 (Go's marker for Max-Age=0 in header)", cookie.MaxAge)
	}

	// Verify the rendered header contains Max-Age=0 (Go's serialization for MaxAge=-1).
	rawHeaders := w.Header().Values("Set-Cookie")
	if len(rawHeaders) != 1 {
		t.Fatalf("Expected 1 Set-Cookie header, got %d", len(rawHeaders))
	}
	if !strings.Contains(rawHeaders[0], "Max-Age=0") {
		t.Errorf("Set-Cookie header missing Max-Age=0: %s", rawHeaders[0])
	}
}

// TestClearSessionCookie_EmptyHost — Story 9.7 (B7): the "empty host" case
// is no longer the relevant signal. A plain-HTTP request (r.TLS == nil)
// always yields a non-Secure clear cookie so browsers accept the deletion
// on loopback HTTP.
func TestClearSessionCookie_EmptyHost(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ClearSessionCookie(w, r)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Secure {
		t.Error("Plain-HTTP request (r.TLS == nil) should produce a non-Secure clear cookie (so loopback HTTP can clear stale cookies)")
	}
}

func TestReadSessionCookie_Valid(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	validID, _ := generateSessionID()
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: validID,
	})

	id, ok := ReadSessionCookie(req)
	if !ok {
		t.Error("ReadSessionCookie should return ok=true for valid cookie")
	}
	if id != validID {
		t.Errorf("ID = %q, want %q", id, validID)
	}
}

func TestReadSessionCookie_Missing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, ok := ReadSessionCookie(req)
	if ok {
		t.Error("ReadSessionCookie should return ok=false for missing cookie")
	}
}

func TestReadSessionCookie_EmptyValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: "   ",
	})
	_, ok := ReadSessionCookie(req)
	if ok {
		t.Error("ReadSessionCookie should return ok=false for whitespace-only cookie")
	}
}

func TestReadSessionCookie_InvalidLength(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: "short",
	})
	_, ok := ReadSessionCookie(req)
	if ok {
		t.Error("ReadSessionCookie should return ok=false for too-short cookie")
	}
}

func TestReadSessionCookie_InvalidHex(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// 64 chars but contains non-hex characters.
	badID := strings.Repeat("g", 64)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: badID,
	})
	_, ok := ReadSessionCookie(req)
	if ok {
		t.Error("ReadSessionCookie should return ok=false for non-hex cookie")
	}
}

// TestReadSessionCookie_UnicodeWhitespace ensures we reject Unicode whitespace
// (e.g. U+00A0 NO-BREAK SPACE) that net/http.Cookie may or may not strip. Hex
// validation must be strict ASCII.
func TestReadSessionCookie_UnicodeWhitespace(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// Prefix with U+00A0, then 63 valid hex chars → 64 visible chars total once
	// the NBSP is included; ReadSessionCookie's hex check must reject the NBSP.
	id := " " + strings.Repeat("a", 63)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: id,
	})
	if _, ok := ReadSessionCookie(req); ok {
		t.Error("ReadSessionCookie should reject Unicode whitespace in the value")
	}
}

func TestAuthenticate_ValidCredentials(t *testing.T) {
	cfg := &types.Config{
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: testPasswordHash,
		},
	}

	if !Authenticate(cfg, "admin", "hunter2") {
		t.Error("Authenticate should return true for valid credentials")
	}
}

func TestAuthenticate_InvalidPassword(t *testing.T) {
	cfg := &types.Config{
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: testPasswordHash,
		},
	}

	if Authenticate(cfg, "admin", "wrongpassword") {
		t.Error("Authenticate should return false for wrong password")
	}
}

func TestAuthenticate_InvalidUsername(t *testing.T) {
	cfg := &types.Config{
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: testPasswordHash,
		},
	}

	if Authenticate(cfg, "wronguser", "hunter2") {
		t.Error("Authenticate should return false for wrong username")
	}
}

func TestAuthenticate_NilConfig(t *testing.T) {
	if Authenticate(nil, "admin", "hunter2") {
		t.Error("Authenticate should return false for nil config")
	}
}

func TestAuthenticate_EmptyHash(t *testing.T) {
	cfg := &types.Config{
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: "",
		},
	}

	if Authenticate(cfg, "admin", "hunter2") {
		t.Error("Authenticate should return false for empty hash (no nil-deref)")
	}
}

// TestAuthenticate_MalformedHash verifies we don't panic on a malformed admin
// hash in config.toml and that the malformed-hash log is emitted at Warn.
func TestAuthenticate_MalformedHash(t *testing.T) {
	cfg := &types.Config{
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: "not-a-valid-bcrypt-hash",
		},
	}
	// Must not panic; must return false.
	if Authenticate(cfg, "admin", "anything") {
		t.Error("Authenticate should return false for malformed hash")
	}
}

func TestSessionAuthMiddleware_MissingCookie(t *testing.T) {
	store := NewSessionStore(context.Background())
	defer store.Stop()

	middleware := SessionAuthMiddleware(store, "127.0.0.1")
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	// HTML client (no Accept header).
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusFound {
		t.Errorf("HTML client: status = %d, want %d", got, http.StatusFound)
	}
	// Live session 2026-06-09: redirect includes ?showLogin=1 so the
	// admin shell's setupLoginSection() reveals the login form.
	// Without it, the user lands on the shell with all widgets
	// broken (no auth) and never sees the form.
	if loc := w.Header().Get("Location"); loc != "/admin/login?showLogin=1" {
		t.Errorf("Location = %q, want %q", loc, "/admin/login?showLogin=1")
	}

	// JSON client.
	req = httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.Header.Set("Accept", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusUnauthorized {
		t.Errorf("JSON client: status = %d, want %d", got, http.StatusUnauthorized)
	}
}

func TestSessionAuthMiddleware_ValidCookie(t *testing.T) {
	store := NewSessionStore(context.Background())
	defer store.Stop()

	session := store.Create("testuser")

	middleware := SessionAuthMiddleware(store, "127.0.0.1")
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify session was injected into context.
		sess, ok := SessionFromContext(r.Context())
		if !ok {
			t.Error("Session should be injected into request context")
		} else if sess.Username != "testuser" {
			t.Errorf("Context session username = %q, want %q", sess.Username, "testuser")
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: session.ID,
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d", got, http.StatusOK)
	}
}

// TestSessionAuthMiddleware_PostTouchRefresh is the R12-P7 regression
// gate for R11-LOW-3: after Touch, the context's Session must carry
// FRESH LastSeenAt/ExpiresAt timestamps (not the pre-Touch stale
// values from the initial Get). This ensures handlers that read
// LastSeenAt/ExpiresAt from ctx.Session see the up-to-date timestamps
// set by the middleware's Touch call.
func TestSessionAuthMiddleware_PostTouchRefresh(t *testing.T) {
	store := NewSessionStore(context.Background())
	defer store.Stop()

	// Create a session. Backdate LastSeenAt/ExpiresAt to known values
	// so we can detect whether the middleware refreshed them.
	session := store.Create("testuser")
	originalLastSeen := time.Now().Add(-1 * time.Hour) // 1 hour ago
	originalExpiry := time.Now().Add(6 * 24 * time.Hour)
	store.mu.Lock()
	if s, ok := store.sessions[session.ID]; ok {
		s.LastSeenAt = originalLastSeen
		s.ExpiresAt = originalExpiry
	}
	store.mu.Unlock()

	var observedLastSeen, observedExpires time.Time
	middleware := SessionAuthMiddleware(store, "127.0.0.1")
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := SessionFromContext(r.Context())
		if !ok {
			t.Fatal("Session should be injected into request context")
		}
		observedLastSeen = sess.LastSeenAt
		observedExpires = sess.ExpiresAt
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/", nil)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: session.ID,
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Fatalf("status = %d, want %d", got, http.StatusOK)
	}

	// After R11-LOW-3, the middleware re-fetches after Touch so the
	// context carries fresh timestamps. observedLastSeen should be
	// approximately now (much later than originalLastSeen) and
	// observedExpires should be ~7 days from now (much later than
	// originalExpiry).
	if observedLastSeen.Before(originalLastSeen.Add(1 * time.Minute)) {
		t.Errorf("context LastSeenAt = %v, want >= ~now (was %v before middleware)",
			observedLastSeen, originalLastSeen)
	}
	if observedExpires.Before(originalExpiry.Add(1 * time.Minute)) {
		t.Errorf("context ExpiresAt = %v, want >= ~now+7d (was %v before middleware)",
			observedExpires, originalExpiry)
	}
}

func TestSessionAuthMiddleware_ExpiredCookie(t *testing.T) {
	store := NewSessionStore(context.Background())
	defer store.Stop()

	// Create a session and manually expire it.
	id, _ := generateSessionID()
	now := time.Now()
	store.mu.Lock()
	store.sessions[id] = &Session{
		ID:         id,
		Username:   "expired-user",
		CreatedAt:  now.Add(-8 * time.Hour),
		LastSeenAt: now.Add(-8 * time.Hour),
		ExpiresAt:  now.Add(-1 * time.Hour), // expired 1 hour ago
	}
	store.mu.Unlock()

	middleware := SessionAuthMiddleware(store, "127.0.0.1")
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	// HTML path: should redirect and clear cookie.
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: id,
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusFound {
		t.Errorf("Expired cookie HTML: status = %d, want %d", got, http.StatusFound)
	}
	if !hasClearedSessionCookie(w) {
		t.Errorf("Expired cookie HTML: expected Set-Cookie with Max-Age=0 to clear, got headers: %v",
			w.Header().Values("Set-Cookie"))
	}

	// JSON path: should return 401 and clear cookie.
	req = httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: id,
	})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusUnauthorized {
		t.Errorf("Expired cookie JSON: status = %d, want %d", got, http.StatusUnauthorized)
	}
	if !hasClearedSessionCookie(w) {
		t.Errorf("Expired cookie JSON: expected Set-Cookie with Max-Age=0 to clear, got headers: %v",
			w.Header().Values("Set-Cookie"))
	}
}

// hasClearedSessionCookie checks the Set-Cookie response headers for a
// vrhub_session deletion (Max-Age=0).
func hasClearedSessionCookie(w *httptest.ResponseRecorder) bool {
	for _, c := range w.Header().Values("Set-Cookie") {
		if strings.Contains(c, SessionCookieName+"=") && strings.Contains(c, "Max-Age=0") {
			return true
		}
	}
	return false
}

// TestSessionAuthMiddleware_NonExistentSession exercises the branch where the
// cookie is well-formed hex but does not correspond to a session in the store
// (e.g. server was restarted, sessions are in-memory only).
func TestSessionAuthMiddleware_NonExistentSession(t *testing.T) {
	store := NewSessionStore(context.Background())
	defer store.Stop()

	middleware := SessionAuthMiddleware(store, "127.0.0.1")
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	// Cookie with valid hex length but not present in store.
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: strings.Repeat("a", 64), // valid hex format but not in store
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusFound {
		t.Errorf("Non-existent session: status = %d, want %d", got, http.StatusFound)
	}
	if !hasClearedSessionCookie(w) {
		t.Errorf("Non-existent session: expected Set-Cookie to clear, got: %v", w.Header().Values("Set-Cookie"))
	}
}

// TestSessionAuthMiddleware_MalformedCookie covers the actual malformed-cookie
// branch (ReadSessionCookie returns !ok): cookie present but contains non-hex
// chars. The middleware MUST clear the bad cookie so the browser stops
// re-sending it on every retry.
func TestSessionAuthMiddleware_MalformedCookie(t *testing.T) {
	store := NewSessionStore(context.Background())
	defer store.Stop()

	middleware := SessionAuthMiddleware(store, "127.0.0.1")
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	// 64 chars but non-hex → ReadSessionCookie returns ok=false.
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: strings.Repeat("z", 64),
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusFound {
		t.Errorf("Malformed cookie (non-hex): status = %d, want %d", got, http.StatusFound)
	}
	if !hasClearedSessionCookie(w) {
		t.Errorf("Malformed cookie (non-hex): expected Set-Cookie to clear stale cookie, got: %v",
			w.Header().Values("Set-Cookie"))
	}
}

func TestSessionAuthMiddleware_NilStore(t *testing.T) {
	middleware := SessionAuthMiddleware(nil, "127.0.0.1")
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d (nil store should pass through)", got, http.StatusOK)
	}
}

// TestSessionAuthMiddleware_ClosedStore verifies the fail-closed semantic when
// the store has been Stop()ed: 503 with JSON error body, NOT auth-bypass. A
// regression that flips the fail-closed branch to next.ServeHTTP would let
// requests through unauthenticated during shutdown.
func TestSessionAuthMiddleware_ClosedStore(t *testing.T) {
	store := NewSessionStore(context.Background())
	store.Stop()

	middleware := SessionAuthMiddleware(store, "127.0.0.1")
	called := false
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if called {
		t.Fatal("next handler was invoked despite closed store — AUTH BYPASS")
	}
	if got := w.Code; got != http.StatusServiceUnavailable {
		t.Errorf("Closed store: status = %d, want %d (must fail-closed)", got, http.StatusServiceUnavailable)
	}
}

func TestIsLoopback(t *testing.T) {
	tests := []struct {
		host     string
		expected bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.1:8080", true},
		{"localhost", true},
		{"localhost:8080", true},
		{"::1", true},
		{"0.0.0.0", false},
		{"0.0.0.0:8080", false},
		{"192.168.1.1", false},
	}

	for _, tc := range tests {
		got := isLoopback(tc.host)
		if got != tc.expected {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.host, got, tc.expected)
		}
	}
}

// TestSessionStore_Stop_CreateConcurrent fuzzes the Stop-vs-Create race that
// motivated R4-STOP-CREATE-RACE: many goroutines call Create while another
// calls Stop. After Stop returns, the store MUST be empty AND IsClosed() must
// be true. No Create call may leak a session into the wiped map.
func TestSessionStore_Stop_CreateConcurrent(t *testing.T) {
	for trial := 0; trial < 5; trial++ {
		store := NewSessionStore(context.Background())

		const creators = 32
		var wg sync.WaitGroup
		// Used to signal when at least one Create has had a chance to start.
		started := atomic.Bool{}

		for i := 0; i < creators; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				started.Store(true)
				store.Create("racer")
			}()
		}

		// Wait until at least one goroutine has started, then Stop.
		for !started.Load() {
			time.Sleep(time.Microsecond)
		}
		store.Stop()
		wg.Wait()

		// Post-Stop invariant: map is wiped AND closed.
		store.mu.RLock()
		count := len(store.sessions)
		store.mu.RUnlock()
		if count != 0 {
			t.Errorf("trial %d: post-Stop sessions = %d, want 0 (Create leaked into wiped map → RACE)", trial, count)
		}
		if !store.IsClosed() {
			t.Errorf("trial %d: IsClosed should be true after Stop", trial)
		}
	}
}

// =====================================================================
// Story 9.7 (B7) — Secure cookie bound to actual HTTPS, not host loopback
// =====================================================================

// TestSetSessionCookie_HTTP_NonLoopback_NoSecureFlag is the AC1 regression
// gate for B7.1: a plain-HTTP request from a non-loopback IP (e.g. a phone
// on the LAN reaching the server at http://192.168.50.3:8080) MUST produce
// a cookie WITHOUT the Secure flag, so the browser will accept and store
// it. The previous !isLoopback(host) check incorrectly set Secure=true
// here, silently breaking LAN login.
func TestSetSessionCookie_HTTP_NonLoopback_NoSecureFlag(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://192.168.50.3:8080/admin/", nil)
	if r.TLS != nil {
		t.Fatalf("test setup: expected r.TLS == nil for http://, got non-nil")
	}
	w := httptest.NewRecorder()
	SetSessionCookie(w, r, "lan-session-id", time.Now().Add(time.Hour))

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Secure {
		t.Errorf("AC1: HTTP non-loopback request must produce Secure=false, got Secure=true (browser would drop cookie on HTTP)")
	}
}

// TestSetSessionCookie_HTTPS_Loopback_SecureFlag is the AC1 positive case:
// a direct HTTPS request to a loopback host MUST produce a Secure cookie.
// Before B7 this also held (via !isLoopback); after B7 it holds via
// r.TLS != nil.
func TestSetSessionCookie_HTTPS_Loopback_SecureFlag(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://127.0.0.1:8443/admin/", nil)
	r.TLS = &tls.ConnectionState{} // simulate direct HTTPS
	w := httptest.NewRecorder()
	SetSessionCookie(w, r, "test-session-id", time.Now().Add(time.Hour))

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if !cookies[0].Secure {
		t.Errorf("AC1: HTTPS loopback request must produce Secure=true, got Secure=false")
	}
}

// TestSetSessionCookie_XForwardedProto_Honored is the AC3 gate: when a
// reverse proxy sits in front of the server and sets X-Forwarded-Proto:
// https, the server should treat the request as HTTPS (Secure=true) even
// though r.TLS == nil (the TLS handshake is between the client and the
// proxy, not the server).
func TestSetSessionCookie_XForwardedProto_Honored(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/admin/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	SetSessionCookie(w, r, "test-session-id", time.Now().Add(time.Hour))

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if !cookies[0].Secure {
		t.Errorf("AC3: X-Forwarded-Proto: https must produce Secure=true, got false")
	}
}

// TestSetSessionCookie_XForwardedProto_HTTP verifies the negative case
// for AC3: a reverse proxy explicitly declaring "http" does NOT flip the
// cookie to Secure. Belt-and-suspenders for the X-Forwarded-Proto path.
func TestSetSessionCookie_XForwardedProto_HTTP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/admin/", nil)
	r.Header.Set("X-Forwarded-Proto", "http")
	w := httptest.NewRecorder()
	SetSessionCookie(w, r, "test-session-id", time.Now().Add(time.Hour))

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Secure {
		t.Errorf("AC3 negative: X-Forwarded-Proto: http must NOT produce Secure=true")
	}
}

// TestSetSessionCookie_XForwardedProto_CaseInsensitive verifies the
// EqualFold behavior of the X-Forwarded-Proto check — some proxies send
// "HTTPS" (uppercase). The match MUST be case-insensitive.
func TestSetSessionCookie_XForwardedProto_CaseInsensitive(t *testing.T) {
	for _, val := range []string{"HTTPS", "Https", "hTTpS"} {
		t.Run("value="+val, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://example.com/admin/", nil)
			r.Header.Set("X-Forwarded-Proto", val)
			w := httptest.NewRecorder()
			SetSessionCookie(w, r, "test-session-id", time.Now().Add(time.Hour))
			cookies := w.Result().Cookies()
			if !cookies[0].Secure {
				t.Errorf("X-Forwarded-Proto=%q must be honored case-insensitively (Secure=true), got false", val)
			}
		})
	}
}

// TestSetSessionCookie_HTTP_Loopback_NoSecureFlag is the AC4 regression
// gate: the pre-B7 behavior for loopback HTTP (Secure=false) MUST be
// preserved. Existing tests assume this. The fix changes only the
// non-loopback case.
func TestSetSessionCookie_HTTP_Loopback_NoSecureFlag(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/admin/", nil)
	w := httptest.NewRecorder()
	SetSessionCookie(w, r, "test-session-id", time.Now().Add(time.Hour))

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Secure {
		t.Errorf("AC4: HTTP loopback must preserve pre-B7 behavior (Secure=false), got Secure=true")
	}
}

// TestSetSessionCookie_PassesHTTPSContext is the AC2 compile-time gate:
// SetSessionCookie's new signature must accept an *http.Request. We pin
// the signature via a function-type assertion so a future refactor that
// reverts to a host string breaks the build, not the runtime.
func TestSetSessionCookie_PassesHTTPSContext(t *testing.T) {
	var _ func(http.ResponseWriter, *http.Request, string, time.Time) = SetSessionCookie
}

// TestClearSessionCookie_ParityWithSetCookie is the cross-function gate:
// ClearSessionCookie and SetSessionCookie MUST produce the same Secure
// flag for the same request. Otherwise the browser would refuse the
// deletion (a Secure cookie can only be cleared by a Set-Cookie with
// Secure=true). This is the live-session symptom in reverse.
func TestClearSessionCookie_ParityWithSetCookie(t *testing.T) {
	cases := []struct {
		name        string
		setupReq    func() *http.Request
		wantSecure  bool
		description string
	}{
		{
			name: "http-non-loopback",
			setupReq: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "http://192.168.50.3:8080/admin/", nil)
			},
			wantSecure:  false,
			description: "LAN HTTP must yield non-Secure set+clear",
		},
		{
			name: "http-loopback",
			setupReq: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/admin/", nil)
			},
			wantSecure:  false,
			description: "Local-dev HTTP must yield non-Secure set+clear",
		},
		{
			name: "https-direct",
			setupReq: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
				r.TLS = &tls.ConnectionState{}
				return r
			},
			wantSecure:  true,
			description: "Direct HTTPS must yield Secure set+clear (else clear would be rejected)",
		},
		{
			name: "https-via-proxy",
			setupReq: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
				r.Header.Set("X-Forwarded-Proto", "https")
				return r
			},
			wantSecure:  true,
			description: "Reverse-proxy HTTPS must yield Secure set+clear (else clear would be rejected)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Set path
			rSet := tc.setupReq()
			wSet := httptest.NewRecorder()
			SetSessionCookie(wSet, rSet, "sid", time.Now().Add(time.Hour))
			setCookies := wSet.Result().Cookies()
			if len(setCookies) != 1 {
				t.Fatalf("set: expected 1 cookie, got %d", len(setCookies))
			}

			// Clear path (use a fresh request mirroring the set path's transport)
			rClear := tc.setupReq()
			wClear := httptest.NewRecorder()
			ClearSessionCookie(wClear, rClear)
			clearCookies := wClear.Result().Cookies()
			if len(clearCookies) != 1 {
				t.Fatalf("clear: expected 1 cookie, got %d", len(clearCookies))
			}

			if setCookies[0].Secure != clearCookies[0].Secure {
				t.Errorf("Secure flag mismatch for %s: set.Secure=%v, clear.Secure=%v (%s) — browser would refuse the deletion",
					tc.name, setCookies[0].Secure, clearCookies[0].Secure, tc.description)
			}
			if setCookies[0].Secure != tc.wantSecure {
				t.Errorf("Set Secure = %v, want %v (%s)", setCookies[0].Secure, tc.wantSecure, tc.description)
			}
			if clearCookies[0].Secure != tc.wantSecure {
				t.Errorf("Clear Secure = %v, want %v (%s)", clearCookies[0].Secure, tc.wantSecure, tc.description)
			}
		})
	}
}

// TestIsHTTPS_Direct covers the helper that drives both cookie functions.
// Pinning its behavior here so a future refactor that breaks the public
// contract (r.TLS != nil || X-Forwarded-Proto == https) fails this test
// rather than silently regressing LAN login.
func TestIsHTTPS_Direct(t *testing.T) {
	cases := []struct {
		name string
		req  *http.Request
		want bool
	}{
		{
			name: "plain http",
			req:  httptest.NewRequest(http.MethodGet, "http://example.com/", nil),
			want: false,
		},
		{
			name: "https direct (r.TLS set)",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
				r.TLS = &tls.ConnectionState{}
				return r
			}(),
			want: true,
		},
		{
			name: "https via proxy",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
				r.Header.Set("X-Forwarded-Proto", "https")
				return r
			}(),
			want: true,
		},
		{
			name: "http via proxy (explicit downgrade)",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
				r.Header.Set("X-Forwarded-Proto", "http")
				return r
			}(),
			want: false,
		},
		{
			name: "empty X-Forwarded-Proto (no header)",
			req:  httptest.NewRequest(http.MethodGet, "http://example.com/", nil),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHTTPS(tc.req); got != tc.want {
				t.Errorf("isHTTPS() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestE2E_LANLogin_HTTPNonLoopback_CookieAccepted is the AC5 integration
// test: simulate a phone-on-LAN login flow at the unit level. We build a
// request that mimics what a browser would send from 192.168.50.3 over
// HTTP, run it through SetSessionCookie, and verify the resulting
// Set-Cookie header does NOT include "Secure". This is the precise
// signal the browser needs to accept and store the cookie.
//
// A full httptest.NewServer end-to-end test would also work, but the
// unit-level assertion on the rendered Set-Cookie header is the load-
// bearing contract for the bug. A server-level test would only confirm
// that SetSessionCookie gets called with a non-loopback IP — which is
// exactly the same assertion in disguise.
func TestE2E_LANLogin_HTTPNonLoopback_CookieAccepted(t *testing.T) {
	// Simulate the exact request shape the LAN phone would send:
	// Host: 192.168.50.3:8080 (non-loopback), URL path /admin/, no TLS.
	r := httptest.NewRequest(http.MethodPost, "http://192.168.50.3:8080/admin/api/auth/login", nil)
	r.Host = "192.168.50.3:8080"
	if r.TLS != nil {
		t.Fatalf("test setup: expected r.TLS == nil for http://, got non-nil")
	}

	w := httptest.NewRecorder()
	SetSessionCookie(w, r, "lan-session-id", time.Now().Add(7*24*time.Hour))

	// Inspect the raw Set-Cookie header (the byte-level contract the
	// browser sees). A non-loopback HTTP request MUST NOT have the
	// "Secure" attribute — that's the bug we just fixed.
	rawHeaders := w.Header().Values("Set-Cookie")
	if len(rawHeaders) != 1 {
		t.Fatalf("expected 1 Set-Cookie header, got %d", len(rawHeaders))
	}
	if strings.Contains(rawHeaders[0], "Secure") || strings.Contains(rawHeaders[0], "secure") {
		t.Errorf("AC5: LAN HTTP login must not emit Secure; got header: %s", rawHeaders[0])
	}
	// Sanity: the cookie IS set, just without Secure.
	if !strings.Contains(rawHeaders[0], SessionCookieName+"=lan-session-id") {
		t.Errorf("AC5: expected cookie name+value in header, got: %s", rawHeaders[0])
	}
}
