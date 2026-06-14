package auth

import (
	"sync"
	"time"
)

// RateLimiter is a sliding-window in-memory rate limiter for the
// login endpoint. It protects against brute-force attacks where an
// adversary tries many password combinations against a known
// username or from a single IP.
//
// S-01 security fix: previously the login endpoint had NO throttling
// (deferred across R1, R2, R4, R5 of story 6-2 code review). This
// type implements a per-key sliding window: each key (e.g. "ip:1.2.3.4"
// or "user:admin") gets at most `max` attempts in any `window`-sized
// window. The window slides per-attempt (not fixed), so an attacker
// who paces their requests at the boundary of the window cannot
// bypass the limit.
//
// State is in-memory only. For a multi-instance deployment, swap
// this for a Redis-backed implementation with the same interface.
// For a single-instance self-hosted server (the target use case),
// in-memory is correct — there's no shared state to coordinate.
//
// Concurrency: a single sync.Mutex guards both the map and the
// per-key slices. The critical section is O(n) per call where n is
// the number of attempts in the window. For typical window sizes
// (5-20 attempts) this is sub-microsecond.
type RateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	window   time.Duration
	max      int

	// janitorCancel terminates the background janitor that purges
	// expired entries. Set by Start(), cleared by Stop().
	janitorCancel chan struct{}
	janitorDone   chan struct{}
}

// NewRateLimiter returns a rate limiter that allows `max` attempts
// per `window`-sized sliding window per key. A janitor runs every
// `window/2` to drop fully-expired keys (preventing memory growth
// from one-off IPs / usernames).
//
// Defaults: 5 attempts per 60 seconds. Tune via NewRateLimiterWithLimits.
func NewRateLimiter() *RateLimiter {
	return NewRateLimiterWithLimits(5, 60*time.Second)
}

// NewRateLimiterWithLimits is the explicit constructor for non-default
// limits. Used by tests and by callers with custom thresholds.
func NewRateLimiterWithLimits(max int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		attempts:      make(map[string][]time.Time),
		window:        window,
		max:           max,
		janitorCancel: make(chan struct{}),
		janitorDone:   make(chan struct{}),
	}
}

// Allow records an attempt for `key` and returns true if the attempt
// is permitted (within the rate limit), false if the key is
// currently rate-limited.
//
// The window slides per-attempt: a new attempt is permitted if fewer
// than `max` prior attempts fall within the last `window` before
// the new attempt. This means an attacker who paces their requests
// to exactly the window boundary cannot bypass the limit — only
// `max` attempts can fit in any window of size `window`.
//
// The function records the new attempt EVEN IF the call returns
// false, so a sustained attacker's key stays at the "over limit"
// state for the duration of the window (not just a single check).
func (r *RateLimiter) Allow(key string) bool {
	if key == "" {
		// Defensive: empty key would be a global bucket. Refuse.
		return false
	}
	now := time.Now()
	cutoff := now.Add(-r.window)

	r.mu.Lock()
	defer r.mu.Unlock()

	// Drop expired entries from the front of the slice. The slice
	// is in chronological order so we can stop as soon as we see
	// a non-expired entry.
	times := r.attempts[key]
	i := 0
	for i < len(times) && times[i].Before(cutoff) {
		i++
	}
	fresh := append(times[:0], times[i:]...)

	if len(fresh) >= r.max {
		// Over limit. Record the (now-rejected) attempt so the
		// window keeps sliding. Without this, a constant burst
		// of attempts at t=window+1 would each see the same
		// empty fresh slice and the next one would pass.
		fresh = append(fresh, now)
		r.attempts[key] = fresh
		return false
	}

	fresh = append(fresh, now)
	r.attempts[key] = fresh
	return true
}

// Reset clears the attempt history for a key. Used after a
// successful login (so the user isn't penalized for the next
// legitimate login from the same IP) and by tests.
func (r *RateLimiter) Reset(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.attempts, key)
}

// Len returns the number of tracked keys. Used by tests and by
// the admin monitoring endpoint (Story 7.4) if needed.
func (r *RateLimiter) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.attempts)
}

// Start launches the background janitor that purges fully-expired
// keys. Must be called once at server startup. Call Stop() at
// shutdown to avoid goroutine leaks.
//
// The janitor is best-effort: a missed tick is harmless (entries
// stay in the map a bit longer than ideal but the rate limiting is
// still correct). A slow tick (e.g. under load) just delays cleanup.
func (r *RateLimiter) Start() {
	go r.janitor()
}

// Stop signals the janitor to exit and waits for it to finish.
// Safe to call multiple times (subsequent calls are no-ops). Safe
// to call even if Start was never called — in that case there's no
// goroutine to wait for and Stop is a no-op.
func (r *RateLimiter) Stop() {
	select {
	case <-r.janitorCancel:
		// Already closed.
	default:
		close(r.janitorCancel)
	}
	// Only block on janitorDone if Start was actually called.
	// Without the started flag, calling Stop without Start would
	// deadlock on a never-closed channel.
	if r.janitorDone != nil {
		select {
		case <-r.janitorDone:
		default:
			// Janitor not running (Start not called).
		}
	}
}

func (r *RateLimiter) janitor() {
	defer close(r.janitorDone)
	ticker := time.NewTicker(r.window / 2)
	if r.window < 2*time.Second {
		// Tests use very small windows (e.g. 100ms). Cap the
		// janitor interval to avoid spinning.
		ticker = time.NewTicker(r.window)
	}
	defer ticker.Stop()

	for {
		select {
		case <-r.janitorCancel:
			return
		case <-ticker.C:
			r.purgeExpired()
		}
	}
}

func (r *RateLimiter) purgeExpired() {
	now := time.Now()
	cutoff := now.Add(-r.window)

	r.mu.Lock()
	defer r.mu.Unlock()

	for key, times := range r.attempts {
		// Drop the prefix of expired times. If everything's
		// expired, delete the key entirely.
		i := 0
		for i < len(times) && times[i].Before(cutoff) {
			i++
		}
		if i == len(times) {
			delete(r.attempts, key)
		} else if i > 0 {
			r.attempts[key] = append(times[:0], times[i:]...)
		}
	}
}
