package auth

import (
	"sync"
	"testing"
	"time"
)

func TestRateLimiter_Allow_BelowLimit(t *testing.T) {
	rl := NewRateLimiterWithLimits(5, 100*time.Millisecond)
	defer rl.Stop()

	// First 5 attempts (the limit) should all be allowed.
	for i := 0; i < 5; i++ {
		if !rl.Allow("ip:1.2.3.4") {
			t.Errorf("attempt %d: Allow returned false, want true (under limit)", i+1)
		}
	}
}

func TestRateLimiter_Deny_AtLimit(t *testing.T) {
	rl := NewRateLimiterWithLimits(3, 1*time.Second)
	defer rl.Stop()

	// 3 allowed, 4th should be denied.
	for i := 0; i < 3; i++ {
		if !rl.Allow("ip:1.2.3.4") {
			t.Fatalf("attempt %d: should be allowed", i+1)
		}
	}
	if rl.Allow("ip:1.2.3.4") {
		t.Error("4th attempt should be denied (over limit)")
	}
	// Subsequent attempts should also be denied (window still active).
	if rl.Allow("ip:1.2.3.4") {
		t.Error("5th attempt should also be denied")
	}
}

func TestRateLimiter_Reset(t *testing.T) {
	rl := NewRateLimiterWithLimits(2, 1*time.Second)
	defer rl.Stop()

	// Burn the limit.
	rl.Allow("ip:1.2.3.4")
	rl.Allow("ip:1.2.3.4")
	if rl.Allow("ip:1.2.3.4") {
		t.Fatal("3rd attempt should be denied before reset")
	}

	// Reset (simulates a successful login from the same IP).
	rl.Reset("ip:1.2.3.4")

	if !rl.Allow("ip:1.2.3.4") {
		t.Error("after Reset, next attempt should be allowed")
	}
}

func TestRateLimiter_KeysAreIndependent(t *testing.T) {
	rl := NewRateLimiterWithLimits(2, 1*time.Second)
	defer rl.Stop()

	// Burn the limit for one key.
	rl.Allow("ip:1.2.3.4")
	rl.Allow("ip:1.2.3.4")
	if rl.Allow("ip:1.2.3.4") {
		t.Fatal("key A should be over limit")
	}

	// A different key is still allowed.
	if !rl.Allow("ip:5.6.7.8") {
		t.Error("key B should be allowed (independent bucket)")
	}

	// Different bucket types are also independent.
	if !rl.Allow("user:admin") {
		t.Error("user bucket should be independent of ip bucket")
	}
}

func TestRateLimiter_WindowSliding(t *testing.T) {
	rl := NewRateLimiterWithLimits(2, 50*time.Millisecond)
	defer rl.Stop()

	// 2 allowed within window.
	rl.Allow("ip:1.2.3.4")
	rl.Allow("ip:1.2.3.4")
	if rl.Allow("ip:1.2.3.4") {
		t.Fatal("3rd attempt should be denied within window")
	}

	// Wait for the window to fully elapse (twice the window to be
	// safe — the test window is 50ms).
	time.Sleep(120 * time.Millisecond)

	// After the window, the key should be allowed again.
	if !rl.Allow("ip:1.2.3.4") {
		t.Error("attempt after window expiry should be allowed")
	}
}

func TestRateLimiter_EmptyKeyDenied(t *testing.T) {
	rl := NewRateLimiterWithLimits(5, 1*time.Second)
	defer rl.Stop()

	// Empty key is a global bucket (vulnerability: any attacker
	// could DoS the entire system by spamming with empty keys).
	// The limiter must refuse to use it.
	if rl.Allow("") {
		t.Error("empty key should be denied (would create a global bucket)")
	}
}

func TestRateLimiter_PurgeExpired(t *testing.T) {
	rl := NewRateLimiterWithLimits(2, 30*time.Millisecond)
	rl.Start() // Start the janitor goroutine
	defer rl.Stop()

	// Add attempts and let them expire.
	rl.Allow("ip:1.2.3.4")
	rl.Allow("ip:1.2.3.4")
	if got := rl.Len(); got != 1 {
		t.Errorf("Len() = %d, want 1 (1 active key)", got)
	}

	// Wait for window to expire + janitor to run.
	time.Sleep(100 * time.Millisecond)

	// Janitor should have purged the expired key.
	if got := rl.Len(); got != 0 {
		t.Errorf("Len() after expiry = %d, want 0 (janitor should purge)", got)
	}
}

func TestRateLimiter_ConcurrentSafe(t *testing.T) {
	rl := NewRateLimiterWithLimits(100, 1*time.Second)
	defer rl.Stop()

	// 50 goroutines each call Allow 10 times with the same key.
	// Total: 500 attempts. Limit is 100 → first 100 allowed, rest
	// denied. The check is on the COUNT, not per-goroutine.
	const goroutines = 50
	const perGoroutine = 10
	var wg sync.WaitGroup
	var allowed, denied int
	var mu sync.Mutex

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				if rl.Allow("ip:shared") {
					mu.Lock()
					allowed++
					mu.Unlock()
				} else {
					mu.Lock()
					denied++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	// The limit is 100; the first 100 calls (regardless of which
	// goroutine) should be allowed, the rest denied. Due to the
	// "record the attempt even on deny" behavior, exactly 100 are
	// allowed (one per bucket slot), and the rest of the
	// attempts are denied.
	if allowed != 100 {
		t.Errorf("allowed = %d, want 100 (limit)", allowed)
	}
	if denied != goroutines*perGoroutine-100 {
		t.Errorf("denied = %d, want %d", denied, goroutines*perGoroutine-100)
	}
}

func TestRateLimiter_StopIsIdempotent(t *testing.T) {
	rl := NewRateLimiterWithLimits(5, 1*time.Second)
	rl.Start()
	rl.Stop()
	// Second call must not panic (close of closed channel).
	rl.Stop()
}
