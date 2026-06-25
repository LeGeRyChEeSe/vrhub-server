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
	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	chi "github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

// TestB2_AdminAPI_AllRoutesAvailable_AfterSetupTransition is the AC1
// regression gate for Story 9.2 (B2). Before the fix, the
// `if gameDB != nil && adminHandler != nil` and `if cfg != nil` gates
// in setupAdminRouter prevented these 14 admin routes from being
// mounted when the server started in setup mode (gameDB == nil,
// cfg == nil). After launch, mode flipped to normal but the router
// was never rebuilt, so the routes returned 404 until the operator
// restarted the server.
//
// Fix (B2): the gates were removed; the routes are ALWAYS registered
// when sessionStore != nil. The dependencies (gameDB, cfg) are
// late-bound via the ConfigPropagator closure (Story 9.1 B1
// extension). Each handler that touches a nil-replaced dependency
// returns 503 NOT_READY at request time (see TestB2_LazyBinding_
// DependencyNotReady_Returns503 for the runtime contract).
//
// This test walks the full router (chi.Walk) after building it in
// setup-mode (gameDB=nil, cfg=nil) and verifies that every admin
// route listed in the AC is REGISTERED — regardless of whether the
// dependencies are populated. The runtime test
// (TestB2_LazyBinding_DependencyNotReady_Returns503) covers the
// 503 path.
func TestB2_AdminAPI_AllRoutesAvailable_AfterSetupTransition(t *testing.T) {
	// Setup-mode wiring: gameDB=nil, cfg=nil, sessionStore present
	// (always created at startup per R7-CRITICAL-SESSION-INIT).
	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	router := SetupRouter(modeVal(types.ModeSetup), t.TempDir(), nil, nil, sessionStore, nil, nil, nil, nil, nil)

	// Collect (method, path) pairs registered on the router.
	registered := make(map[string]bool)
	chi.Walk(router, func(method string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// Normalize chi's `/*/` mount segment back to a single `/`
		// (see api_docs_test.go for the same convention).
		normalized := strings.ReplaceAll(route, "/*/", "/")
		for strings.Contains(normalized, "//") {
			normalized = strings.ReplaceAll(normalized, "//", "/")
		}
		key := method + " " + normalized
		registered[key] = true
		return nil
	})

	// Every admin route from the story AC must be registered, even
	// when the dependencies are nil at router construction time.
	//
	// B2.1 — game routes (previously gated by `gameDB != nil`):
	wantGameRoutes := []struct{ method, path string }{
		{"POST", "/admin/api/games/rescan"},
		{"GET", "/admin/api/games/{releaseName}/corruption-status"},
		{"POST", "/admin/api/games/{releaseName}/revalidate"},
		{"PATCH", "/admin/api/games/{releaseName}/exposed"},
		{"GET", "/admin/api/games"},
		{"DELETE", "/admin/api/games/{releaseName}"},
	}
	// B2.2 — admin settings + scripts routes (previously gated by
	// `cfg != nil`):
	wantAdminScriptsRoutes := []struct{ method, path string }{
		{"GET", "/admin/api/admin/settings"},
		{"PUT", "/admin/api/admin/settings"},
		{"POST", "/admin/api/admin/api-key/regenerate"},
		{"GET", "/admin/api/admin/api-key"},
		{"GET", "/admin/api/scripts/_ping"},
		{"GET", "/admin/api/scripts/games"},
		{"DELETE", "/admin/api/scripts/games/{releaseName}"},
		{"PATCH", "/admin/api/scripts/games/{releaseName}/exposed"},
		{"PATCH", "/admin/api/scripts/games/{releaseName}"},
		{"POST", "/admin/api/scripts/apps"},
		{"GET", "/admin/api/scripts/config"},
		{"PUT", "/admin/api/scripts/config"},
		{"POST", "/admin/api/scripts/backup"},
		{"POST", "/admin/api/scripts/restore"},
	}

	for _, r := range wantGameRoutes {
		key := r.method + " " + r.path
		if !registered[key] {
			t.Errorf("AC1/B2.1: route %q NOT registered on the router (gameDB-nil gate regression)", key)
		}
	}
	for _, r := range wantAdminScriptsRoutes {
		key := r.method + " " + r.path
		if !registered[key] {
			t.Errorf("AC1/B2.2: route %q NOT registered on the router (cfg-nil gate regression)", key)
		}
	}
}

// TestB2_LazyBinding_DependencyNotReady_Returns503 is the AC2
// regression gate. The router now always mounts the admin routes
// (no more gameDB/cfg nil gates), but the dependencies (gameDB,
// Importer, API-key cfg) may still be nil at request time. Each
// handler must surface a 503 NOT_READY instead of panicking on a
// nil deref.
//
// We construct a router with sessionStore wired (so the admin
// routes ARE mounted) but gameDB=nil and cfg=nil. We then:
//  1. Hit GET /admin/api/games → expect 503 DB_NOT_READY.
//  2. Hit POST /admin/api/games/rescan → expect 503 IMPORTER_NOT_READY.
//  3. Hit GET /admin/api/scripts/games → expect 503 (the script
//     router has the B2 cfg-nil fallback middleware installed).
//  4. Hit GET /admin/api/scripts/_ping → expect 200 (always
//     reachable, no DB / no cfg).
//
// The mode is NORMAL (not Setup) so the SetupModeMiddleware does
// not redirect our test requests; we still construct the router
// with cfg=nil, gameDB=nil, mimicking the worst case of a
// setup→normal transition BEFORE HandleLaunchPOST runs (i.e. the
// B2 late-bind hasn't happened yet).
func TestB2_LazyBinding_DependencyNotReady_Returns503(t *testing.T) {
	// Pre-seed config.toml so any operator that hits an
	// authenticated route can still log in. The 503 paths we
	// exercise don't require auth, but writing the file makes
	// the test more robust against future handler changes that
	// might wrap requireDB behind a session check.
	dataDir := t.TempDir()
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

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	// Mode=Normal so the SetupModeMiddleware doesn't redirect
	// our test requests. The router is still built with
	// cfg=nil, gameDB=nil (mimics a setup→normal transition
	// BEFORE HandleLaunchPOST runs, which is the B2 worst case
	// for the late-binding to fail / not have happened yet).
	mv := new(atomic.Value)
	mv.Store(string(types.ModeNormal))
	router := SetupRouter(mv, dataDir, nil, nil, sessionStore, nil, nil, nil, nil, nil)

	// (1) GET /admin/api/games with no session but gameDB=nil
	// → 503 DB_NOT_READY (the protected router requires a
	// session; the session middleware 401s the unauthenticated
	// request. To hit the handler, we need a session. We login
	// first using the config.toml we wrote above.)
	loginBody := []byte(`{"username":"admin","password":"hunter2"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Accept", "application/json")
	loginW := httptest.NewRecorder()
	router.ServeHTTP(loginW, loginReq)
	if got := loginW.Code; got != http.StatusOK {
		t.Fatalf("login: status = %d, want 200 (response: %s)", got, loginW.Body.String())
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

	// (1) GET /admin/api/games with valid session but gameDB=nil
	// → 503 DB_NOT_READY.
	gamesReq := httptest.NewRequest(http.MethodGet, "/admin/api/games", nil)
	gamesReq.AddCookie(sessionCookie)
	gamesW := httptest.NewRecorder()
	router.ServeHTTP(gamesW, gamesReq)
	if got := gamesW.Code; got != http.StatusServiceUnavailable {
		t.Errorf("step 1: GET /admin/api/games status = %d, want 503 (DB_NOT_READY)\nbody: %s",
			got, gamesW.Body.String())
	}
	if body := gamesW.Body.String(); !strings.Contains(body, "DB_NOT_READY") {
		t.Errorf("step 1: body = %q, want it to contain DB_NOT_READY error code", body)
	}

	// (2) POST /admin/api/games/rescan with valid session but
	// Importer=nil → 503 IMPORTER_NOT_READY. M-06 (review
	// 2026-06-11): the rescan route now requires CSRF too. Pull the
	// session ID off the cookie and compute the matching token so
	// the test reaches the importer check.
	rescanReq := httptest.NewRequest(http.MethodPost, "/admin/api/games/rescan", nil)
	rescanReq.AddCookie(sessionCookie)
	rescanReq.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(sessionCookie.Value))
	rescanW := httptest.NewRecorder()
	router.ServeHTTP(rescanW, rescanReq)
	if got := rescanW.Code; got != http.StatusServiceUnavailable {
		t.Errorf("step 2: POST /admin/api/games/rescan status = %d, want 503 (IMPORTER_NOT_READY)\nbody: %s",
			got, rescanW.Body.String())
	}
	if body := rescanW.Body.String(); !strings.Contains(body, "IMPORTER_NOT_READY") {
		t.Errorf("step 2: body = %q, want it to contain IMPORTER_NOT_READY error code", body)
	}

	// (3) GET /admin/api/scripts/games with valid session but
	// cfg=nil (API-key auth disabled) → 503 NOT_CONFIGURED.
	// (The B2 fallback middleware replaces auth.APIKeyAuthMiddleware
	// when cfg is nil; it always 503s.)
	scriptsGamesReq := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/games", nil)
	scriptsGamesReq.AddCookie(sessionCookie)
	scriptsGamesW := httptest.NewRecorder()
	router.ServeHTTP(scriptsGamesW, scriptsGamesReq)
	if got := scriptsGamesW.Code; got != http.StatusServiceUnavailable {
		t.Errorf("step 3: GET /admin/api/scripts/games status = %d, want 503 (NOT_CONFIGURED)\nbody: %s",
			got, scriptsGamesW.Body.String())
	}
	if body := scriptsGamesW.Body.String(); !strings.Contains(body, "NOT_CONFIGURED") {
		t.Errorf("step 3: body = %q, want it to contain NOT_CONFIGURED error code", body)
	}

	// (4) GET /admin/api/scripts/_ping is a pure reachability probe
	// — it never touches a dep and must always return 200.
	pingReq := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/_ping", nil)
	pingReq.AddCookie(sessionCookie)
	pingW := httptest.NewRecorder()
	router.ServeHTTP(pingW, pingReq)
	if got := pingW.Code; got != http.StatusOK {
		t.Errorf("step 4: GET /admin/api/scripts/_ping status = %d, want 200 (reachability probe must not depend on cfg)\nbody: %s",
			got, pingW.Body.String())
	}
}

// TestB2_SetupRouter_NormalMode_AllRoutesMounted is the AC3
// regression gate. The B2 fix must NOT regress the normal-mode
// startup path. When the server boots in normal mode (gameDB != nil,
// cfg != nil), every admin route is still mounted AND every
// request reaches a real handler (not 404, not 503).
//
// We use chi.Walk to verify route registration, plus one
// representative request per group to verify the handler chain
// works end-to-end with real dependencies.
func TestB2_SetupRouter_NormalMode_AllRoutesMounted(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testLoginPasswordHash},
		Update: types.UpdateConfig{Enabled: true, CheckInterval: 0, AutoApply: false},
	}

	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()

	mv := new(atomic.Value)
	mv.Store(string(types.ModeNormal))

	// Real gameDB to satisfy the B2 requireDB check (mirrors the
	// pattern used by api_docs_test.go and admin_test.go).
	gameDB, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open test DB: %v", err)
	}
	defer gameDB.Close()

	router := SetupRouter(mv, t.TempDir(), gameDB, cfg, sessionStore, nil, nil, nil, nil, nil)

	// Pass 1: chi.Walk verifies the routes are mounted (the
	// B2 fix removed the gates, so they're always there).
	registered := make(map[string]bool)
	chi.Walk(router, func(method string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		normalized := strings.ReplaceAll(route, "/*/", "/")
		for strings.Contains(normalized, "//") {
			normalized = strings.ReplaceAll(normalized, "//", "/")
		}
		key := method + " " + normalized
		registered[key] = true
		return nil
	})

	wantRoutes := []string{
		"GET /admin/api/games",
		"GET /admin/api/admin/settings",
		"GET /admin/api/scripts/games",
		"GET /admin/api/scripts/_ping",
		"GET /admin/api/network-status",
	}
	for _, r := range wantRoutes {
		if !registered[r] {
			t.Errorf("AC3: route %q NOT registered (B2 regression: gate reintroduced?)", r)
		}
	}

	// Pass 2: verify one representative route per group reaches
	// the handler (not 404 / 503) when deps are present.
	loginBody := []byte(`{"username":"admin","password":"hunter2"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("Accept", "application/json")
	loginW := httptest.NewRecorder()
	router.ServeHTTP(loginW, loginReq)
	if got := loginW.Code; got != http.StatusOK {
		t.Fatalf("AC3: login status = %d, want 200 (response: %s)", got, loginW.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, c := range loginW.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("AC3: login did not return a session cookie")
	}

	// (a) GET /admin/api/games with real gameDB → 200 (empty list,
	// but a real 200 — not 404, not 503).
	gamesReq := httptest.NewRequest(http.MethodGet, "/admin/api/games", nil)
	gamesReq.AddCookie(sessionCookie)
	gamesW := httptest.NewRecorder()
	router.ServeHTTP(gamesW, gamesReq)
	if got := gamesW.Code; got != http.StatusOK {
		t.Errorf("AC3: GET /admin/api/games status = %d, want 200 (real gameDB present, no B2 regression)\nbody: %s",
			got, gamesW.Body.String())
	}
}
