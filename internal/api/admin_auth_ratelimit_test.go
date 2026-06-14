package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestHandleAuthLoginPOST_RateLimit_PerIP verifies that the
// rate limiter in S-01 actually blocks repeated login attempts
// from the same IP. This is the integration gate — the unit
// tests in internal/auth cover the limiter math; this test
// covers the wiring in the login handler.
func TestHandleAuthLoginPOST_RateLimit_PerIP(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	// 3 attempts max, 60s window — small enough to exhaust in a test.
	limiter := auth.NewRateLimiterWithLimits(3, 60*1000_000_000) // 60s in ns

	// Build a router with just the login route (no session middleware
	// so the test is focused on the rate limiter behavior, not the
	// auth flow).
	adminHandler := NewAdminHandler(t.TempDir(), nil, nil, sessionStore, cfg)
	adminHandler.LoginRateLimiter = limiter

	r := http.NewServeMux()
	r.HandleFunc("/admin/api/auth/login", adminHandler.HandleAuthLoginPOST)

	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrongpass"})

	// 3 attempts should all return 401 (bad password) — the
	// rate limiter permits the attempt and bcrypt fails.
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		// Same IP for all 3 (httptest default is 192.0.2.1).
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("attempt %d: status = %d, want 401 (wrong password)", i+1, w.Code)
		}
	}

	// 4th attempt should be rate-limited (429), not 401.
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("4th attempt: status = %d, want 429 (rate limited)", w.Code)
	}
	if retryAfter := w.Header().Get("Retry-After"); retryAfter == "" {
		t.Error("Retry-After header missing on 429")
	}
}

// TestHandleAuthLogoutPOST_CSRFProtection verifies S-02: a logout
// request without a valid CSRF token is rejected with 403. This
// blocks a cross-site page from logging the user out via
// <form action="https://server/admin/api/auth/logout" method="POST">.
//
// The test logs in first (to get a valid session cookie), then
// issues a logout WITHOUT the X-CSRF-Token header. Expects 403
// CSRF_INVALID. The session must NOT be deleted (the attacker
// failed).
func TestHandleAuthLogoutPOST_CSRFProtection(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	// Login to get a valid session cookie.
	loginBody, _ := json.Marshal(map[string]string{"username": "admin", "password": "adminpass"})
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Accept", "application/json")
	loginW := httptest.NewRecorder()
	router.ServeHTTP(loginW, loginReq)
	if loginW.Code != http.StatusOK {
		t.Fatalf("login: status = %d, want 200", loginW.Code)
	}

	var sessionID string
	for _, c := range loginW.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionID = c.Value
			break
		}
	}
	if sessionID == "" {
		t.Fatal("no session cookie from login")
	}

	// Attacker request: has the session cookie (the browser
	// includes it automatically on cross-origin POSTs for the
	// target origin — but SameSite=Lax cookies block this for
	// modern browsers; the test simulates the legacy or future
	// case where the cookie is included) but NO CSRF token.
	attackerReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/logout", nil)
	attackerReq.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: sessionID,
	})
	// Intentionally NO X-CSRF-Token header.

	attackerW := httptest.NewRecorder()
	router.ServeHTTP(attackerW, attackerReq)

	if attackerW.Code != http.StatusForbidden {
		t.Errorf("attacker logout: status = %d, want 403 (CSRF protection)", attackerW.Code)
	}

	// Session must still be alive — the attacker didn't succeed.
	if store.Get(sessionID) == nil {
		t.Error("session was deleted despite CSRF rejection — should be preserved")
	}
}

// the S-01 success-path reset: after a successful login, the
// per-IP bucket is cleared so a legitimate user (e.g. on a
// shared NAT) isn't penalized for the next attempt.
//
// Test design: use a limit of 5 so a user can fail twice, then
// succeed on the 3rd attempt (under the limit), then verify the
// next 3 attempts are permitted (proving the reset happened —
// without the reset, the bucket would be at 3/5 and would allow
// 2 more, then deny).
func TestHandleAuthLoginPOST_RateLimit_ResetsOnSuccess(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	limiter := auth.NewRateLimiterWithLimits(5, 60*1000_000_000) // 60s

	adminHandler := NewAdminHandler(t.TempDir(), nil, nil, sessionStore, cfg)
	adminHandler.LoginRateLimiter = limiter

	r := http.NewServeMux()
	r.HandleFunc("/admin/api/auth/login", adminHandler.HandleAuthLoginPOST)

	failBody, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrongpass"})
	goodBody, _ := json.Marshal(map[string]string{"username": "admin", "password": "adminpass"})

	doRequest := func(body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// 2 failed attempts.
	for i := 0; i < 2; i++ {
		if w := doRequest(failBody); w.Code != http.StatusUnauthorized {
			t.Fatalf("failed attempt %d: status = %d, want 401", i+1, w.Code)
		}
	}

	// 3rd attempt: CORRECT password — should succeed (200) AND
	// reset the per-IP bucket.
	if w := doRequest(goodBody); w.Code != http.StatusOK {
		t.Fatalf("successful login: status = %d, want 200", w.Code)
	}

	// After the reset, 4 more attempts (failed passwords) should
	// all be permitted (5 - 0 in bucket, plus 4 new = 4/5, all
	// under the limit). Without the reset, only 3 of these 4
	// would be permitted (we'd already be at 3/5 from the prior
	// failed attempts).
	for i := 0; i < 4; i++ {
		if w := doRequest(failBody); w.Code == http.StatusTooManyRequests {
			t.Errorf("post-reset failed attempt %d: rate-limited (429) — reset did NOT clear the bucket", i+1)
		} else if w.Code != http.StatusUnauthorized {
			t.Errorf("post-reset failed attempt %d: status = %d, want 401 (not rate-limited, but password is wrong)", i+1, w.Code)
		}
	}
}
