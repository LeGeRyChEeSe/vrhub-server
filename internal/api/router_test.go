package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	"golang.org/x/crypto/bcrypt"
)

// testLoginPasswordHash is a precomputed bcrypt hash for "hunter2" using MinCost.
var testLoginPasswordHash = func() string {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		panic("router_test: failed to generate login password hash: " + err.Error())
	}
	return string(hash)
}()

func modeVal(mode types.ServerMode) *atomic.Value {
	v := new(atomic.Value)
	v.Store(string(mode))
	return v
}

func TestSetupModeRedirectHandler_SetupMode_Returns302(t *testing.T) {
	handler := SetupModeRedirectHandler(modeVal(types.ModeSetup))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusFound {
		t.Errorf("status = %d, want %d", got, http.StatusFound)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/setup" {
		t.Errorf("Location = %q, want %q", loc, "/admin/setup")
	}
}

func TestSetupModeRedirectHandler_NormalMode_RedirectsToAdmin(t *testing.T) {
	handler := SetupModeRedirectHandler(modeVal(types.ModeNormal))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusFound {
		t.Errorf("status = %d, want %d", got, http.StatusFound)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/" {
		t.Errorf("Location = %q, want %q", loc, "/admin/")
	}
}

func TestSetupModeMiddleware_SetupMode_RedirectsAdminRoutes(t *testing.T) {
	middleware := SetupModeMiddleware(modeVal(types.ModeSetup))
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	testCases := []struct {
		path       string
		wantStatus int
	}{
		{"/admin/setup", http.StatusOK},
		{"/admin/setup/", http.StatusOK},
		{"/admin/games", http.StatusFound},
		{"/admin/api/config", http.StatusFound},
		{"/admin/status", http.StatusFound},
	}

	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if got := w.Code; got != tc.wantStatus {
				t.Errorf("path %s: status = %d, want %d", tc.path, got, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusFound {
				if loc := w.Header().Get("Location"); loc != "/admin/setup" {
					t.Errorf("path %s: Location = %q, want %q", tc.path, loc, "/admin/setup")
				}
			}
		})
	}
}

// TestSetupModeMiddleware_SetupMode_AllowsStaticAssets (Story 1.6) verifies
// that /admin/static/* is allowed through the middleware in setup mode so
// the setup wizard page can load its CSS/JS. Without this, the middleware
// would 302 static asset requests back to /admin/setup, breaking the wizard.
func TestSetupModeMiddleware_SetupMode_AllowsStaticAssets(t *testing.T) {
	middleware := SetupModeMiddleware(modeVal(types.ModeSetup))
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	testCases := []string{
		"/admin/static/setup.css",
		"/admin/static/setup.js",
		"/admin/static/admin.css",
		"/admin/static/admin.js",
		"/admin/static/any-other-asset.png",
	}

	for _, path := range testCases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if got := w.Code; got != http.StatusOK {
				t.Errorf("path %s: status = %d, want %d (static assets must be allowed in setup mode)", path, got, http.StatusOK)
			}
		})
	}
}

func TestSetupModeMiddleware_SetupMode_RedirectsAdminSetupSubpaths(t *testing.T) {
	middleware := SetupModeMiddleware(modeVal(types.ModeSetup))
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	testCases := []string{
		"/admin/setup/foo",
		"/admin/setup/anything/else",
	}

	for _, path := range testCases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if got := w.Code; got != http.StatusFound {
				t.Errorf("path %s: status = %d, want %d (should redirect)", path, got, http.StatusFound)
			}
			if loc := w.Header().Get("Location"); loc != "/admin/setup" {
				t.Errorf("path %s: Location = %q, want %q", path, loc, "/admin/setup")
			}
		})
	}
}

func TestSetupRouter_SetupMode_AdminSetupSubpathsRedirect(t *testing.T) {
	router := SetupRouter(modeVal(types.ModeSetup), t.TempDir(), nil, nil, nil, nil, nil, nil, nil, nil)

	testCases := []string{
		"/admin/setup/foo",
		"/admin/setup/anything/else",
	}

	for _, path := range testCases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if got := w.Code; got != http.StatusFound {
				t.Errorf("path %s: status = %d, want %d (should redirect to setup)", path, got, http.StatusFound)
			}
			if loc := w.Header().Get("Location"); loc != "/admin/setup" {
				t.Errorf("path %s: Location = %q, want %q", path, loc, "/admin/setup")
			}
		})
	}
}

func TestSetupModeMiddleware_NormalMode_AllowsAllRoutes(t *testing.T) {
	middleware := SetupModeMiddleware(modeVal(types.ModeNormal))
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/admin/games", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d (normal mode should allow all admin routes)", got, http.StatusOK)
	}
}

func TestSetupMode503Handler_SetupMode_Returns503(t *testing.T) {
	middleware := SetupMode503Handler(modeVal(types.ModeSetup))
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	testPaths := []string{"/meta.7z", "/abc123/", "/abc123/game.apk"}

	for _, path := range testPaths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if got := w.Code; got != http.StatusServiceUnavailable {
				t.Errorf("path %s: status = %d, want %d", path, got, http.StatusServiceUnavailable)
			}
			body := w.Body.String()
			if body != "Server not configured\n" {
				t.Errorf("body = %q, want %q", body, "Server not configured\n")
			}
		})
	}
}

func TestSetupMode503Handler_NormalMode_AllowsThrough(t *testing.T) {
	middleware := SetupMode503Handler(modeVal(types.ModeNormal))
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/meta.7z", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d (normal mode should allow public API)", got, http.StatusOK)
	}
}

func TestSetupRouter_SetupMode_RedirectsRoot(t *testing.T) {
	router := SetupRouter(modeVal(types.ModeSetup), t.TempDir(), nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusFound {
		t.Errorf("status = %d, want %d", got, http.StatusFound)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/setup" {
		t.Errorf("Location = %q, want %q", loc, "/admin/setup")
	}
}

func TestSetupRouter_SetupMode_AdminSetupAccessible(t *testing.T) {
	router := SetupRouter(modeVal(types.ModeSetup), t.TempDir(), nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/setup", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d (setup page should be accessible)", got, http.StatusOK)
	}
}

func TestSetupRouter_SetupMode_AdminRedirects(t *testing.T) {
	router := SetupRouter(modeVal(types.ModeSetup), t.TempDir(), nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/games", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusFound {
		t.Errorf("status = %d, want %d (other admin routes should redirect)", got, http.StatusFound)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/setup" {
		t.Errorf("Location = %q, want %q", loc, "/admin/setup")
	}
}

func TestSetupRouter_SetupMode_PublicRoutesReturn503(t *testing.T) {
	router := SetupRouter(modeVal(types.ModeSetup), t.TempDir(), nil, nil, nil, nil, nil, nil, nil, nil)

	testPaths := []string{"/meta.7z", "/abc123/game.apk"}

	for _, path := range testPaths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if got := w.Code; got != http.StatusServiceUnavailable {
				t.Errorf("path %s: status = %d, want %d", path, got, http.StatusServiceUnavailable)
			}
		})
	}
}

// TestSetupRouter_NormalMode_Meta7zIsPublic documents the contract that
// GET /meta.7z is intentionally UNAUTHENTICATED — the VRHub client
// (com.vrhub.logic.CatalogUtils.downloadFile) does a plain GET without
// any password header. The 7z archive is AES-256 encrypted and the
// password is used to extract it locally on the Quest (see
// ServerConfig.kt: "The password for extracting archives from the
// server"). In a router-only test (no DB, no Config) the handler
// returns 500 because it cannot encrypt the archive without a config;
// this is the expected behaviour for a not-fully-wired server. The
// important assertion is the ROUTE is reachable (not blocked by 503 or
// 401 setup-mode middleware).
func TestSetupRouter_NormalMode_Meta7zIsPublic(t *testing.T) {
	router := SetupRouter(modeVal(types.ModeNormal), t.TempDir(), nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/meta.7z", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got == http.StatusServiceUnavailable {
		t.Errorf("status = %d, want != 503 (SetupMode503Handler must not block /meta.7z in normal mode)", got)
	}
	if got := w.Code; got == http.StatusUnauthorized {
		t.Errorf("status = %d, want != 401 (/meta.7z is intentionally unauthenticated)", got)
	}
}

func TestSetupRouter_NormalMode_RootRedirectsToAdmin(t *testing.T) {
	router := SetupRouter(modeVal(types.ModeNormal), t.TempDir(), nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusFound {
		t.Errorf("status = %d, want %d (normal mode GET / should redirect to /admin/)", got, http.StatusFound)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/" {
		t.Errorf("Location = %q, want %q", loc, "/admin/")
	}
}

func TestSetupRouter_LoginRouteRegistered(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	router := SetupRouter(modeVal, t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil, nil)

	body := []byte(`{"username":"admin","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d (login route should be registered)\nbody: %s", got, http.StatusOK, w.Body.String())
	}
}

func TestSetupRouter_LogoutRouteRegistered(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	router := SetupRouter(modeVal, t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/logout", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusNoContent {
		t.Errorf("status = %d, want %d (logout route should be registered)\nbody: %s", got, http.StatusNoContent, w.Body.String())
	}
}

// TestSetupRouter_EndToEndLoginFlow is the Subtask 8.6 regression gate for
// AC1/AC3. It exercises the full login → cookie → protected route flow:
//  1. POST /admin/api/auth/login (HTML accept) with valid creds → 302 to PostLoginRedirect
//  2. Verify vrhub_session cookie is set
//  3. GET /admin/ with cookie → 200 (admin UI)
//  4. GET /admin/api/games (protected) WITHOUT cookie → 302 to /admin/login
//  5. GET /admin/api/games (protected) WITH cookie → not 302/401 (protected route reachable)
//
// This test exists because previous reviews (R1-R5) showed the auth wiring
// could regress silently in unit-test mode (e.g. middleware skipping when
// sessionStore is nil). This is the integration gate that catches such drift.
func TestSetupRouter_EndToEndLoginFlow(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1:39457"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	router := SetupRouter(modeVal, t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil, nil)

	// Step 1: POST /admin/api/auth/login with HTML Accept → 302 to PostLoginRedirect.
	loginBody := []byte(`{"username":"admin","password":"hunter2"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Accept", "text/html")
	loginReq.Header.Set("Content-Type", "application/json")
	loginW := httptest.NewRecorder()
	router.ServeHTTP(loginW, loginReq)

	if got := loginW.Code; got != http.StatusSeeOther {
		t.Fatalf("step 1: login status = %d, want %d (303 See Other after POST login per RFC 7231 §6.4.3)\nbody: %s", got, http.StatusSeeOther, loginW.Body.String())
	}
	if loc := loginW.Header().Get("Location"); loc != PostLoginRedirect {
		t.Errorf("step 1: Location = %q, want %q", loc, PostLoginRedirect)
	}

	// Step 2: Extract the session cookie from the login response.
	var sessionCookie *http.Cookie
	for _, c := range loginW.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("step 2: vrhub_session cookie was not set on successful login")
	}
	if sessionCookie.Value == "" {
		t.Fatal("step 2: vrhub_session cookie value is empty")
	}
	// Cookie attributes (per AC1 + Dev Notes cross-cutting constraints).
	if !sessionCookie.HttpOnly {
		t.Error("step 2: cookie must be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("step 2: cookie SameSite = %v, want %v", sessionCookie.SameSite, http.SameSiteLaxMode)
	}
	if sessionCookie.Path != "/" {
		t.Errorf("step 2: cookie Path = %q, want %q", sessionCookie.Path, "/")
	}
	if sessionCookie.Secure {
		t.Error("step 2: cookie should not be Secure for 127.0.0.1 (loopback)")
	}

	// Step 3: GET /admin/ with the cookie → 200 (admin shell HTML).
	adminReq := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	adminReq.AddCookie(sessionCookie)
	adminW := httptest.NewRecorder()
	router.ServeHTTP(adminW, adminReq)

	if got := adminW.Code; got != http.StatusOK {
		t.Errorf("step 3: GET /admin/ with cookie status = %d, want %d", got, http.StatusOK)
	}

	// Step 4: GET /admin/api/games WITHOUT cookie → 302 to /admin/login
	// (AC3: unauthenticated request to admin UI returns redirect).
	noAuthReq := httptest.NewRequest(http.MethodGet, "/admin/api/games", nil)
	noAuthW := httptest.NewRecorder()
	router.ServeHTTP(noAuthW, noAuthReq)

	// Note: /admin/api/games is wired only when gameDB != nil. With gameDB=nil,
	// the route isn't registered → 404 from chi. We still want to verify the
	// middleware blocks unauthenticated access; use an alternative protected
	// route that IS registered: the update routes (cfg != nil).
	if got := noAuthW.Code; got == http.StatusOK {
		t.Errorf("step 4: GET /admin/api/games without cookie reached the handler — AUTH BYPASS")
	}

	// Step 4b: GET /admin/api/update/status without cookie → 302 (or 401 for JSON).
	updateNoAuthReq := httptest.NewRequest(http.MethodGet, "/admin/api/update/status", nil)
	updateNoAuthW := httptest.NewRecorder()
	router.ServeHTTP(updateNoAuthW, updateNoAuthReq)

	if got := updateNoAuthW.Code; got != http.StatusFound {
		t.Errorf("step 4b: GET /admin/api/update/status without cookie status = %d, want %d (HTML redirect to login)",
			got, http.StatusFound)
	}
	if loc := updateNoAuthW.Header().Get("Location"); loc != "/admin/login?showLogin=1" {
		t.Errorf("step 4b: Location = %q, want %q", loc, "/admin/login?showLogin=1")
	}

	// Step 4c: same request with Accept: application/json → 401 JSON.
	updateJSONReq := httptest.NewRequest(http.MethodGet, "/admin/api/update/status", nil)
	updateJSONReq.Header.Set("Accept", "application/json")
	updateJSONW := httptest.NewRecorder()
	router.ServeHTTP(updateJSONW, updateJSONReq)

	if got := updateJSONW.Code; got != http.StatusUnauthorized {
		t.Errorf("step 4c: GET /admin/api/update/status JSON without cookie status = %d, want %d",
			got, http.StatusUnauthorized)
	}

	// Step 5: GET /admin/api/update/status WITH cookie → not 302/401.
	updateAuthReq := httptest.NewRequest(http.MethodGet, "/admin/api/update/status", nil)
	updateAuthReq.AddCookie(sessionCookie)
	updateAuthW := httptest.NewRecorder()
	router.ServeHTTP(updateAuthW, updateAuthReq)

	if got := updateAuthW.Code; got == http.StatusFound || got == http.StatusUnauthorized {
		t.Errorf("step 5: GET /admin/api/update/status WITH cookie was rejected (status=%d) — middleware over-blocking", got)
	}
}

// TestSetupRouter_LoginRouteNotRegisteredInSetupMode asserts that in setup mode
// (no sessionStore, no cfg) the login endpoint is not exposed. A regression
// that wires the auth handler in setup mode would let attackers POST to
// /admin/api/auth/login before the admin password has even been configured.
func TestSetupRouter_LoginRouteNotRegisteredInSetupMode(t *testing.T) {
	router := SetupRouter(modeVal(types.ModeSetup), t.TempDir(), nil, nil, nil, nil, nil, nil, nil, nil)

	body := []byte(`{"username":"admin","password":"any"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Setup-mode middleware redirects all non-/setup admin routes to /admin/setup.
	if got := w.Code; got != http.StatusFound {
		t.Errorf("login in setup mode: status = %d, want %d (should redirect to setup)", got, http.StatusFound)
	}
}

// TestSetupRouter_LoginRouteHandlesMissingConfig asserts that the login route is
// registered (R7-CRITICAL-SESSION-INIT fix) and that a request when no config.toml
// exists on disk fails with 401 (not 404, not 500). The previous behavior was to
// silently 404 the route when cfg was nil; the new behavior is to invoke the handler,
// which attempts config.Load from disk, fails (no file), and reports 401 to keep
// the response indistinguishable from a wrong-password attempt. The underlying
// error is logged server-side for operator forensics but never surfaces to the
// attacker.
func TestSetupRouter_LoginRouteHandlesMissingConfig(t *testing.T) {
	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	router := SetupRouter(modeVal(types.ModeNormal), t.TempDir(), nil, nil, sessionStore, nil, nil, nil, nil, nil)

	body := []byte(`{"username":"admin","password":"any"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusUnauthorized {
		t.Errorf("login with no config on disk: status = %d, want %d (must collapse to 401 to avoid leaking server state)\nbody: %s", got, http.StatusUnauthorized, w.Body.String())
	}
}

// TestSetupRouter_LoginAfterSetupTransition is the R7-CRITICAL-SESSION-INIT regression gate.
// It simulates the fresh-install → setup-wizard → login flow that previously returned
// 404 because the session store was only created when mode == ModeNormal && cfg != nil
// at startup, and TransitionToNormal only flips an atomic flag (no router rebuild).
//
// After the fix:
//   - sessionStore is created unconditionally at startup (cmd/server/main.go)
//   - the login route is registered whenever sessionStore is non-nil
//   - the login handler reloads config.toml from disk when h.Config is nil
//
// The test writes a config.toml containing the bcrypt-hashed admin password AFTER
// the router is built (mimicking the post-setup state), then POSTs to /login and
// expects 302 + a valid session cookie.
func TestSetupRouter_LoginAfterSetupTransition(t *testing.T) {
	dataDir := t.TempDir()

	// Build the router as if at startup in setup mode: cfg == nil.
	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()
	mv := modeVal(types.ModeNormal) // transition already happened
	router := SetupRouter(mv, dataDir, nil, nil, sessionStore, nil, nil, nil, nil, nil)

	// Simulate the setup wizard: write config.toml with admin credentials.
	// bcrypt hash for "hunter2" generated with MinCost (test speed).
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generate bcrypt: %v", err)
	}
	cfgContent := []byte(`[server]
host = "127.0.0.1"
port = 8080

[admin]
username = "admin"
password_hash = "` + string(hash) + `"
`)
	if err := os.WriteFile(filepath.Join(dataDir, "config.toml"), cfgContent, 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	// Now attempt login — the handler must reload config from disk and authenticate.
	body := []byte(`{"username":"admin","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusSeeOther {
		t.Fatalf("login after setup transition: status = %d, want %d (303 See Other after POST login per RFC 7231 §6.4.3)\nbody: %s", got, http.StatusSeeOther, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != PostLoginRedirect {
		t.Errorf("login after setup transition: Location = %q, want %q", loc, PostLoginRedirect)
	}

	// Verify a session cookie was set.
	var found *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			found = c
			break
		}
	}
	if found == nil {
		t.Fatalf("login after setup transition: no %q cookie set (cookies: %v)", auth.SessionCookieName, w.Result().Cookies())
	}
}

// TestSetupRouter_LoginFormFallback_NoJS is the R7-CRITICAL-FORM-LEAK regression gate.
// The login form at internal/ui/ui.go declares
// `action="/admin/api/auth/login" method="post"`. If admin.js fails to load
// (parse error, CSP block, network error) the browser submits the form via
// standard HTML defaults — which would be GET to the current URL without
// our action/method attributes. This test asserts the handler accepts
// `application/x-www-form-urlencoded` so the no-JS fallback path is functional
// and the password does not leak into the URL bar.
func TestSetupRouter_LoginFormFallback_NoJS(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	router := SetupRouter(modeVal(types.ModeNormal), t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil, nil)

	// Simulate a browser submitting the form with the defaults in place
	// (Content-Type: application/x-www-form-urlencoded, no JSON, no Accept header).
	formBody := "username=admin&password=hunter2"
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader([]byte(formBody)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusSeeOther {
		t.Errorf("form-urlencoded login: status = %d, want %d (303 See Other after POST login per RFC 7231 §6.4.3)\nbody: %s", got, http.StatusSeeOther, w.Body.String())
	}
}

// TestSetupRouter_LoginRoute_NotProtected (live session 2026-06-08) is a
// regression gate for the infinite-redirect-loop bug:
//
//  1. User visits /admin/login without a session cookie.
//  2. SessionAuthMiddleware sees no session and 302-redirects to /admin/login
//     (writeAuthError, internal/auth/session.go:759).
//  3. Browser follows the redirect to /admin/login — still no session — and
//     the middleware redirects again to /admin/login. Loop. The browser
//     shows an empty page.
//
// The fix: /admin/login MUST be on the unprotected admin sub-router
// (setupAdminRouter in router.go) so it is reachable without a session.
// This test asserts that an unauthenticated GET /admin/login returns 200
// and does NOT 302-redirect to itself.
func TestSetupRouter_LoginRoute_NotProtected(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	router := SetupRouter(modeVal(types.ModeNormal), t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil, nil)

	// Step 1: GET /admin/login WITHOUT a session cookie.
	req := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// The login page MUST be reachable. It MUST NOT 302 to itself
	// (which would be a redirect loop the browser surfaces as an
	// empty page).
	//
	// We only assert the status code, not the body, because the
	// adminHTMLFn renderer is wired by main.go in production and is
	// nil in the test wiring (no SetAdminHTML call). The full HTML
	// render is exercised by other tests; here we just need to
	// confirm the route is reachable without auth.
	if got := w.Code; got == http.StatusFound {
		loc := w.Header().Get("Location")
		if loc == "/admin/login" {
			t.Fatalf("GET /admin/login (no session) redirected to itself (%q) — infinite redirect loop regression", loc)
		}
		// A 302 to a different target (e.g. /admin/setup) is also
		// unexpected for normal mode, but we leave the assertion
		// loose to avoid false positives from future router changes.
		t.Errorf("GET /admin/login (no session) returned 302 (Location=%q); want 200", loc)
	}
	if got := w.Code; got != http.StatusOK {
		t.Errorf("GET /admin/login (no session): status = %d, want %d\nbody: %s", got, http.StatusOK, w.Body.String())
	}
}

// TestSetupRouter_UpdateApplyRoute_Reachable is the regression gate for the Round 9
// BLOCKER "Double slash //api/update/apply". The protected router is mounted with
// r.Mount("/", protectedRouter) at router.go:~200; a regression that turned the
// trailing slash into a literal double-slash (e.g. r.Mount("//", ...)) would
// break POST /admin/api/update/apply. This test exercises the full route with a
// valid session cookie and asserts the request reaches the handler (no 404).
func TestSetupRouter_UpdateApplyRoute_Reachable(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1:39457"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
		Update: types.UpdateConfig{Enabled: true},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))
	router := SetupRouter(modeVal, t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil, nil)

	// Login to obtain a valid session cookie.
	loginBody := []byte(`{"username":"admin","password":"hunter2"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Accept", "application/json")
	loginW := httptest.NewRecorder()
	router.ServeHTTP(loginW, loginReq)
	if got := loginW.Code; got != http.StatusOK {
		t.Fatalf("login: status = %d, want 200", got)
	}
	var sessionCookie *http.Cookie
	for _, c := range loginW.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("login did not return a session cookie")
	}

	// POST /admin/api/update/apply with the valid session. The handler returns
	// 400 (no downloadURL configured) on a freshly-built router — what matters
	// here is that the request reaches the handler at all, NOT a 404 from chi.
	req := httptest.NewRequest(http.MethodPost, "/admin/api/update/apply", nil)
	req.AddCookie(sessionCookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Errorf("POST /admin/api/update/apply returned 404 — route is broken (BLOCKER REGRESSION)")
	}
	if w.Code == http.StatusFound || w.Code == http.StatusUnauthorized {
		t.Errorf("POST /admin/api/update/apply redirected/rejected (status=%d) — middleware over-blocking on a valid session", w.Code)
	}
}

// TestSetupRouter_AdminHTML_Unprotected documents the deliberate design: the
// admin HTML shell at /admin/ is served WITHOUT session middleware, so the
// login form is reachable for unauthenticated users. The form is hidden by
// default and revealed by ?showLogin=1; after login the JS navigates back to
// /admin/ and the shell re-renders. This is the SPA design chosen for 6-2
// (Documented as Story 6-3 carry-over in the R1 follow-up DN-1 decision).
//
// Story 9.5 (B5): this test has been REPLACED by the four tests below
// (TestAdminLoginHandler_ServesLoginHTML,
// TestAdminLoginHandler_DoesNotServeShell,
// TestAdminRoot_Unauthenticated_RedirectsToLogin,
// TestAdminRoot_Authenticated_ServesShell). The previous design (shell
// served to unauthenticated users, login form hidden inside it) is no
// longer the contract: the dedicated /admin/login page is now served
// to unauthenticated users, and /admin/ requires a valid session.
//
// func TestSetupRouter_AdminHTML_Unprotected(t *testing.T) {
// 	cfg := &types.Config{
// 		Server: types.ServerConfig{Host: "127.0.0.1:39457"},
// 		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
// 	}
//
// 	sessionStore := auth.NewSessionStore(context.Background())
// 	defer sessionStore.Stop()
//
// 	modeVal := new(atomic.Value)
// 	modeVal.Store(string(types.ModeNormal))
// 	router := SetupRouter(modeVal, t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil)
//
// 	// No cookie → admin HTML still served (200), NOT 302/401.
// 	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
// 	w := httptest.NewRecorder()
// 	router.ServeHTTP(w, req)
//
// 	if got := w.Code; got != http.StatusOK {
// 		t.Errorf("GET /admin/ without cookie: status = %d, want 200 (admin HTML must be reachable for login form)", got)
// 	}
// 	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
// 		t.Errorf("Content-Type = %q, want text/html prefix", ct)
// 	}
// 	if !strings.Contains(w.Body.String(), `id="login-section"`) {
// 		t.Error("admin HTML body does not contain #login-section — login form is not embedded in the shell")
// 	}
// 	if !strings.Contains(w.Body.String(), `action="/admin/api/auth/login"`) {
// 		t.Error("admin HTML body does not contain the login form action — login form is not embedded in the shell")
// 	}
// }

// TestAdminLoginHandler_ServesLoginHTML (Story 9.5 / B5, AC1) asserts that
// GET /admin/login returns 200 + text/html + a body that contains the
// login form. This is the dedicated login page introduced to fix the
// "dashboard-behind-login-form" UX bug.
func TestAdminLoginHandler_ServesLoginHTML(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	router := SetupRouter(modeVal(types.ModeNormal), t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Fatalf("GET /admin/login: status = %d, want 200\nbody: %s", got, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html prefix", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="login-form"`) {
		t.Error("login page should contain the login form (id='login-form')")
	}
	if !strings.Contains(body, `id="login-username"`) {
		t.Error("login page should contain the username input (id='login-username')")
	}
	if !strings.Contains(body, `id="login-password"`) {
		t.Error("login page should contain the password input (id='login-password')")
	}
	if !strings.Contains(body, `id="login-submit"`) {
		t.Error("login page should contain the submit button (id='login-submit')")
	}
	if !strings.Contains(body, `action="/admin/api/auth/login"`) {
		t.Error("login page form action should be /admin/api/auth/login")
	}
	if !strings.Contains(body, `method="post"`) {
		t.Error("login page form method should be post")
	}
	// And the page references the login.js script (the form-submit glue).
	if !strings.Contains(body, `/admin/static/login.js`) {
		t.Error("login page should load /admin/static/login.js (form submit handler)")
	}
}

// TestAdminLoginHandler_DoesNotServeShell (Story 9.5 / B5, AC1) is the
// negative assertion: the dedicated /admin/login page must NOT contain
// any of the dashboard markers. A regression that re-embedded the login
// form inside the shell would re-introduce the B5 UX bug.
func TestAdminLoginHandler_DoesNotServeShell(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	router := SetupRouter(modeVal(types.ModeNormal), t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Fatalf("GET /admin/login: status = %d, want 200\nbody: %s", got, w.Body.String())
	}
	body := w.Body.String()

	// Substring-negative gate on every shell marker that the B5 bug
	// story enumerates. A regression that re-inlines the shell into
	// the login page would leak all of these markers.
	shellMarkers := []string{
		"sidebar-brand",
		"michel-header",
		"status-widget",
		"config-widget",
		"game-count-widget",
	}
	for _, marker := range shellMarkers {
		if strings.Contains(body, marker) {
			t.Errorf("login page should NOT contain shell marker %q (the dashboard is served separately on /admin/)", marker)
		}
	}
	// And no body class for mode-michel / mode-power (the login page
	// is mode-neutral — there is no Michel/Power distinction for the
	// first time the user logs in).
	if strings.Contains(body, "mode-michel") || strings.Contains(body, "mode-power") {
		t.Error("login page should not declare a mode body class (the page is mode-neutral)")
	}
	// And no admin.js script tag (the login page is self-contained —
	// it only loads login.js for the form submit handler).
	if strings.Contains(body, `/admin/static/admin.js`) {
		t.Error("login page should NOT load admin.js (the page is mode-neutral; login.js is sufficient)")
	}
}

// TestAdminRoot_Unauthenticated_RedirectsToLogin (Story 9.5 / B5, AC2)
// asserts that GET /admin/ WITHOUT a session cookie returns 302 to
// /admin/login?showLogin=1. The /admin/ route is now behind the session
// middleware (was previously unprotected).
func TestAdminRoot_Unauthenticated_RedirectsToLogin(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	router := SetupRouter(modeVal(types.ModeNormal), t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusFound {
		t.Errorf("GET /admin/ without cookie: status = %d, want %d (302 redirect to login)", got, http.StatusFound)
	}
	if loc := w.Header().Get("Location"); loc != "/admin/login?showLogin=1" {
		t.Errorf("Location = %q, want %q (session middleware redirect)", loc, "/admin/login?showLogin=1")
	}
}

// TestAdminRoot_Authenticated_ServesShell (Story 9.5 / B5, AC2) is the
// companion to TestAdminRoot_Unauthenticated_RedirectsToLogin. It
// asserts that an authenticated user (valid session cookie) gets the
// full admin shell (HTTP 200 + sidebar-brand + michel-header) on
// GET /admin/.
func TestAdminRoot_Authenticated_ServesShell(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	router := SetupRouter(modeVal(types.ModeNormal), t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil, nil)

	// Login to obtain a valid session cookie.
	loginBody := []byte(`{"username":"admin","password":"hunter2"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Accept", "application/json")
	loginW := httptest.NewRecorder()
	router.ServeHTTP(loginW, loginReq)
	if got := loginW.Code; got != http.StatusOK {
		t.Fatalf("login: status = %d, want 200", got)
	}
	var sessionCookie *http.Cookie
	for _, c := range loginW.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("login did not return a session cookie")
	}

	// GET /admin/ WITH the cookie → 200 + shell markers.
	adminReq := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	adminReq.AddCookie(sessionCookie)
	adminW := httptest.NewRecorder()
	router.ServeHTTP(adminW, adminReq)

	if got := adminW.Code; got != http.StatusOK {
		t.Errorf("GET /admin/ WITH cookie: status = %d, want 200 (admin shell)\nbody: %s", got, adminW.Body.String())
	}
	body := adminW.Body.String()
	// Story X: the legacy #sidebar and #michel-header are gone.
	// The SPA shell's unified #app-header is identical in both
	// modes, so we assert on the new header IDs.
	if !strings.Contains(body, `id="app-header"`) {
		t.Error("admin shell body should contain 'id=\"app-header\"' (the unified header)")
	}
	if !strings.Contains(body, `id="mode-switch"`) {
		t.Error("admin shell body should contain 'id=\"mode-switch\"' (the bottom-left mode toggle)")
	}
	if !strings.Contains(body, `id="section-dashboard"`) {
		t.Error("admin shell body should contain 'id=\"section-dashboard\"' (the default SPA section)")
	}
}
