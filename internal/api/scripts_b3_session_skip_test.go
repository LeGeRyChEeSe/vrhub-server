package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestB3_ScriptsAPIKey_NoSession_Authenticates is the AC1 regression gate
// for Story 9.3 (B3). Before the fix, a request to /admin/api/scripts/_ping
// with a valid X-API-Key but WITHOUT a session cookie was rejected by the
// SessionAuthMiddlewareWithHostFunc on the parent protectedRouter and
// 302-redirected to /admin/login. The /admin/api/scripts/* sub-tree is
// mounted INSIDE the protected router, so the session check ran BEFORE the
// API-key middleware on apiKeyRouter.
//
// After the fix, the session middleware honours a path-skip predicate for
// /admin/api/scripts/* (minimal change, preserves the auth contract for
// every other route on protectedRouter). The X-API-Key middleware on the
// inner sub-router still enforces its own 401 contract.
//
// We exercise the /_ping endpoint (no auth, no DB, no cfg) to assert the
// session middleware no longer short-circuits the request. The _ping body
// returns the literal "via":"api_key" string, but the test does NOT assert
// on the body — it asserts the request reaches the handler (i.e. NOT 302
// to /admin/login) and returns 200.
func TestB3_ScriptsAPIKey_NoSession_Authenticates(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1:39457"},
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: testLoginPasswordHash,
		},
		Update: types.UpdateConfig{Enabled: true},
	}
	// Generate a real API key + seed the hash so APIKeyAuthMiddleware
	// (mounted on the inner scripts sub-router) is happy.
	plaintext, hash, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	cfg.Admin.APIKeyHash = hash

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	mv := new(atomic.Value)
	mv.Store(string(types.ModeNormal))
	router := SetupRouter(mv, t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil)

	// AC1: request to /admin/api/scripts/_ping with a valid X-API-Key
	// and NO session cookie. Pre-fix: 302 to /admin/login. Post-fix: 200.
	req := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/_ping", nil)
	req.Header.Set("X-API-Key", plaintext)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusFound {
		loc := w.Header().Get("Location")
		t.Fatalf("AC1: GET /admin/api/scripts/_ping with X-API-Key got 302 (Location=%q) — session middleware blocked API-key auth (B3 NOT FIXED)", loc)
	}
	if w.Code != http.StatusOK {
		t.Errorf("AC1: status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
}

// TestB3_ScriptsSessionAuth_NoAPIKey_Authenticates is the AC2 regression
// gate. The /admin/api/scripts/* sub-tree must ALSO be reachable with a
// valid session cookie alone (backwards compat: an operator who has
// already logged in via the browser should not be forced to also pass
// the X-API-Key to hit the script-friendly endpoints).
//
// Pre-fix, this test would have been 200 (the session middleware
// accepted the cookie and passed through to the _ping handler).
// Post-fix, it MUST still be 200. This is the "no regression" gate.
func TestB3_ScriptsSessionAuth_NoAPIKey_Authenticates(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1:39457"},
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: testLoginPasswordHash,
		},
		Update: types.UpdateConfig{Enabled: true},
	}
	_, hash, _ := auth.GenerateAPIKey()
	cfg.Admin.APIKeyHash = hash

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	mv := new(atomic.Value)
	mv.Store(string(types.ModeNormal))
	router := SetupRouter(mv, t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil)

	// Login to obtain a valid session cookie.
	loginBody := []byte(`{"username":"admin","password":"hunter2"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Accept", "application/json")
	loginW := httptest.NewRecorder()
	router.ServeHTTP(loginW, loginReq)
	if loginW.Code != http.StatusOK {
		t.Fatalf("setup: login status = %d, want 200 (body=%s)", loginW.Code, loginW.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, c := range loginW.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("setup: login did not return a session cookie")
	}

	// AC2: GET /admin/api/scripts/_ping with the session cookie,
	// NO X-API-Key. Should be 200.
	req := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/_ping", nil)
	req.AddCookie(sessionCookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("AC2: status = %d, want 200 (session cookie alone must still authenticate scripts/*)\nbody: %s", w.Code, w.Body.String())
	}
}

// TestB3_ScriptsBothAuth_OK is the AC3 regression gate. A request that
// carries BOTH a valid session cookie AND a valid X-API-Key must be
// accepted. The system must NOT double-fail (e.g. by passing one
// credential through the session middleware and then checking the
// other and rejecting because the per-middleware contract is
// "either-or"). The /_ping endpoint accepts any request that reaches
// it, so we expect 200 either way.
func TestB3_ScriptsBothAuth_OK(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1:39457"},
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: testLoginPasswordHash,
		},
		Update: types.UpdateConfig{Enabled: true},
	}
	plaintext, hash, _ := auth.GenerateAPIKey()
	cfg.Admin.APIKeyHash = hash

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	mv := new(atomic.Value)
	mv.Store(string(types.ModeNormal))
	router := SetupRouter(mv, t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil)

	// Login to obtain a valid session cookie.
	loginBody := []byte(`{"username":"admin","password":"hunter2"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Accept", "application/json")
	loginW := httptest.NewRecorder()
	router.ServeHTTP(loginW, loginReq)
	if loginW.Code != http.StatusOK {
		t.Fatalf("setup: login status = %d, want 200", loginW.Code)
	}
	var sessionCookie *http.Cookie
	for _, c := range loginW.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("setup: login did not return a session cookie")
	}

	// AC3: GET /admin/api/scripts/_ping with BOTH auth methods. 200.
	req := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/_ping", nil)
	req.AddCookie(sessionCookie)
	req.Header.Set("X-API-Key", plaintext)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("AC3: status = %d, want 200 (both auth methods must be accepted; no double-fail)\nbody: %s", w.Code, w.Body.String())
	}
}

// TestB3_ScriptsNoAuth_Returns401 is the AC4a regression gate. A
// request with NEITHER a session cookie NOR an X-API-Key must still
// be rejected. The session middleware now skips /admin/api/scripts/*,
// so the request reaches the inner apiKeyRouter which mounts
// APIKeyAuthMiddleware — that middleware must return 401 with
// API_KEY_MISSING. This is the "skip must NOT open an auth-free hole"
// regression gate.
//
// We hit /admin/api/scripts/status (an apiKeyRouter route, NOT
// /_ping) so the request actually traverses the APIKeyAuthMiddleware.
func TestB3_ScriptsNoAuth_Returns401(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1:39457"},
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: testLoginPasswordHash,
		},
		Update: types.UpdateConfig{Enabled: true},
	}
	_, hash, _ := auth.GenerateAPIKey()
	cfg.Admin.APIKeyHash = hash

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	mv := new(atomic.Value)
	mv.Store(string(types.ModeNormal))
	router := SetupRouter(mv, t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil)

	// AC4a: GET /admin/api/scripts/status, NO cookie, NO X-API-Key.
	// Pre-fix: 302 to /admin/login. Post-fix: 401 API_KEY_MISSING
	// (the session middleware skipped, the inner APIKeyAuthMiddleware
	// rejected the missing header).
	req := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/status", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusFound {
		t.Fatalf("AC4a: GET /admin/api/scripts/status (no auth) got 302 — session middleware STILL blocking scripts/* (B3 NOT FIXED)")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("AC4a: status = %d, want 401 (API-key middleware must reject missing header)\nbody: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "API_KEY_MISSING") {
		t.Errorf("AC4a: body should contain API_KEY_MISSING, got: %s", body)
	}
}

// TestB3_ScriptsBadAPIKey_Returns401 is the AC4b regression gate. A
// request with an INVALID X-API-Key (and no session cookie) must be
// rejected with 401 API_KEY_INVALID. The session middleware skips, the
// inner APIKeyAuthMiddleware sees a wrong key, and rejects.
//
// This is a defence-in-depth check: even though the inner middleware
// already covered this case (and the existing apikey_middleware tests
// verify it in isolation), the end-to-end router wiring must preserve
// the same behaviour when the session middleware has a path-skip.
func TestB3_ScriptsBadAPIKey_Returns401(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1:39457"},
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: testLoginPasswordHash,
		},
		Update: types.UpdateConfig{Enabled: true},
	}
	_, hash, _ := auth.GenerateAPIKey()
	cfg.Admin.APIKeyHash = hash

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	mv := new(atomic.Value)
	mv.Store(string(types.ModeNormal))
	router := SetupRouter(mv, t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil)

	// AC4b: GET /admin/api/scripts/status with a bogus X-API-Key,
	// NO session cookie. Pre-fix: 302 (session middleware blocked
	// before the API key check could ever run). Post-fix: 401
	// API_KEY_INVALID.
	req := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/status", nil)
	req.Header.Set("X-API-Key", "this-is-a-bogus-key-xxxxxxxxxxxxxxxxxxxx")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusFound {
		t.Fatalf("AC4b: GET /admin/api/scripts/status (bad key) got 302 — session middleware STILL blocking scripts/* (B3 NOT FIXED)")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("AC4b: status = %d, want 401 (API-key middleware must reject invalid key)\nbody: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "API_KEY_INVALID") {
		t.Errorf("AC4b: body should contain API_KEY_INVALID, got: %s", body)
	}
}
