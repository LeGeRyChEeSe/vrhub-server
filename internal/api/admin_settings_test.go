package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/config"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	chi "github.com/go-chi/chi/v5"
)

// stubReloader records Rebind calls for assertions.
type stubReloader struct {
	calls  []string
	errors []error
}

func (s *stubReloader) Rebind(addr string) error {
	s.calls = append(s.calls, addr)
	if len(s.errors) > 0 {
		err := s.errors[0]
		s.errors = s.errors[1:]
		return err
	}
	return nil
}

func newSettingsTestHandler(t *testing.T, cfg *types.Config) (*AdminHandler, *stubReloader) {
	t.Helper()
	store := auth.NewSessionStore(context.Background())
	t.Cleanup(store.Stop)

	// Use a temp dir for data so WriteConfig can persist the new file.
	dataDir := t.TempDir()
	if cfg != nil {
		if err := writeConfigFile(dataDir, cfg); err != nil {
			t.Fatalf("write test config: %v", err)
		}
	}

	reloader := &stubReloader{}
	updatePushed := false
	h := &AdminHandler{
		DataDir:      dataDir,
		SessionStore: store,
		Config:       cfg,
		Reloader:     reloader,
		UpdateConfigPusher: func(*types.UpdateConfig) {
			updatePushed = true
		},
		adminHTMLFn: func() []byte {
			return []byte("<html>admin shell</html>")
		},
	}
	_ = updatePushed
	return h, reloader
}

func writeConfigFile(dataDir string, cfg *types.Config) error {
	// Use the same atomic write the handler uses.
	return config.WriteConfig(cfg, dataDir)
}

// TestHandleSettingsPUT_Valid verifies the happy path: a valid settings
// payload persists to disk, the in-memory config is updated, and the
// Reloader is invoked with the new address.
func TestHandleSettingsPUT_Valid(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}
	h, reloader := newSettingsTestHandler(t, cfg)

	// Create a session and inject it into the request context (the
	// CSRF middleware expects session in ctx).
	session := h.SessionStore.Create("admin")
	body, _ := json.Marshal(map[string]interface{}{
		"server": map[string]interface{}{"host": "0.0.0.0", "port": 9090},
		"update": map[string]interface{}{"enabled": true, "auto_apply": false},
	})
	req := httptest.NewRequest(http.MethodPut, "/admin/api/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	// Inject session via the real auth.SessionFromContext path: build
	// a context with the session.
	ctx := contextWithSession(req.Context(), session)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.HandleSettingsPUT(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
	if len(reloader.calls) != 1 {
		t.Errorf("Reloader.Rebind called %d times, want 1", len(reloader.calls))
	} else if reloader.calls[0] != "0.0.0.0:9090" {
		t.Errorf("Reloader.Rebind(%q), want %q", reloader.calls[0], "0.0.0.0:9090")
	}
	// Verify in-memory config swapped.
	if h.Config.Server.Host != "0.0.0.0" || h.Config.Server.Port != 9090 {
		t.Errorf("in-memory config not updated: %+v", h.Config.Server)
	}
}

// TestHandleSettingsPUT_GameFoldersChangedHook verifies Story 3.5 AC3:
// the OnGameFoldersChanged hook fires (with the new folder set) when a
// settings save actually changes game_folders, and does NOT fire when
// the folder set is unchanged.
func TestHandleSettingsPUT_GameFoldersChangedHook(t *testing.T) {
	doPUT := func(t *testing.T, startFolders, newFolders []string) (fired bool, gotFolders []string) {
		cfg := &types.Config{
			Server:      types.ServerConfig{Host: "127.0.0.1", Port: 8080},
			Admin:       types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
			GameFolders: startFolders,
		}
		h, _ := newSettingsTestHandler(t, cfg)
		h.OnGameFoldersChanged = func(folders []string) {
			fired = true
			gotFolders = folders
		}

		session := h.SessionStore.Create("admin")
		body, _ := json.Marshal(map[string]interface{}{
			"server":       map[string]interface{}{"host": "127.0.0.1", "port": 8080},
			"game_folders": newFolders,
		})
		req := httptest.NewRequest(http.MethodPut, "/admin/api/admin/settings", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
		req = req.WithContext(contextWithSession(req.Context(), session))
		w := httptest.NewRecorder()
		h.HandleSettingsPUT(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
		}
		return fired, gotFolders
	}

	t.Run("changed folders fires hook with new set", func(t *testing.T) {
		fired, got := doPUT(t, []string{"/old/folder"}, []string{"/new/folder", "/another"})
		if !fired {
			t.Fatal("OnGameFoldersChanged should fire when game_folders change")
		}
		if len(got) != 2 || got[0] != "/new/folder" || got[1] != "/another" {
			t.Errorf("hook got %v, want [/new/folder /another]", got)
		}
	})

	t.Run("unchanged folders does not fire hook", func(t *testing.T) {
		fired, _ := doPUT(t, []string{"/same/folder"}, []string{"/same/folder"})
		if fired {
			t.Error("OnGameFoldersChanged should NOT fire when game_folders are unchanged")
		}
	})
}

// TestGameFoldersChanged exercises the set-comparison helper directly.
func TestGameFoldersChanged(t *testing.T) {
	cases := []struct {
		name     string
		old, new []string
		want     bool
	}{
		{"both nil", nil, nil, false},
		{"identical", []string{"a", "b"}, []string{"a", "b"}, false},
		{"added", []string{"a"}, []string{"a", "b"}, true},
		{"removed", []string{"a", "b"}, []string{"a"}, true},
		{"replaced", []string{"a"}, []string{"b"}, true},
		{"reordered", []string{"a", "b"}, []string{"b", "a"}, true},
		{"empty to one", nil, []string{"a"}, true},
		{"one to empty", []string{"a"}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := gameFoldersChanged(c.old, c.new); got != c.want {
				t.Errorf("gameFoldersChanged(%v, %v) = %v, want %v", c.old, c.new, got, c.want)
			}
		})
	}
}

// TestHandleSettingsPUT_InvalidPort rejects ports out of range.
func TestHandleSettingsPUT_InvalidPort(t *testing.T) {
	cfg := &types.Config{Server: types.ServerConfig{Host: "127.0.0.1", Port: 8080}}
	h, reloader := newSettingsTestHandler(t, cfg)

	session := h.SessionStore.Create("admin")
	body, _ := json.Marshal(map[string]interface{}{
		"server": map[string]interface{}{"host": "127.0.0.1", "port": 99999},
	})
	req := httptest.NewRequest(http.MethodPut, "/admin/api/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	ctx := contextWithSession(req.Context(), session)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.HandleSettingsPUT(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("port 99999: status = %d, want 400", w.Code)
	}
	if len(reloader.calls) != 0 {
		t.Errorf("Reloader should not be called on invalid port (was called %d times)", len(reloader.calls))
	}
}

// TestHandleSettingsPUT_CSRFRejected verifies the CSRF middleware
// rejects requests without a valid token (R6-CSRF-LOGOUT resolution).
func TestHandleSettingsPUT_CSRFRejected(t *testing.T) {
	cfg := &types.Config{Server: types.ServerConfig{Host: "127.0.0.1", Port: 8080}}
	h, _ := newSettingsTestHandler(t, cfg)

	body, _ := json.Marshal(map[string]interface{}{
		"server": map[string]interface{}{"host": "127.0.0.1", "port": 9090},
	})
	req := httptest.NewRequest(http.MethodPut, "/admin/api/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// NO session in ctx, NO CSRF header → should be rejected.
	w := httptest.NewRecorder()
	h.HandleSettingsPUT(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("no CSRF + no session: status = %d, want 403\nbody: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CSRF_INVALID") {
		t.Errorf("body should contain CSRF_INVALID, got: %s", w.Body.String())
	}
}

// TestHandleAPIKeyRegeneratePOST verifies the API key regeneration
// produces a 64-char hex plaintext and a matching SHA-256 hash that
// persists in the config.
func TestHandleAPIKeyRegeneratePOST(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}
	h, _ := newSettingsTestHandler(t, cfg)

	// Set up a session for CSRF.
	session := h.SessionStore.Create("admin")
	req := httptest.NewRequest(http.MethodPost, "/admin/api/admin/api-key/regenerate", nil)
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	// Inject session into request context so csrfTokenForSession finds it.
	ctx := injectSessionContext(req.Context(), session)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.HandleAPIKeyRegeneratePOST(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			APIKeyPlaintext string `json:"api_key_plaintext"`
			APIKeyHint      string `json:"api_key_hint"`
			Message         string `json:"message"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data.APIKeyPlaintext) != 64 {
		t.Errorf("api_key_plaintext length = %d, want 64", len(resp.Data.APIKeyPlaintext))
	}
	// Verify the persisted hash matches.
	if !auth.VerifyAPIKey(resp.Data.APIKeyPlaintext, h.Config.Admin.APIKeyHash) {
		t.Error("persisted hash does not match the returned plaintext")
	}
}

// injectSessionContext returns a context with the session injected
// (mirrors what the auth middleware does after validating a cookie).
func injectSessionContext(ctx context.Context, session *auth.Session) context.Context {
	return auth.InjectSessionForTest(ctx, session)
}

// contextWithSession injects a session into the request context using
// the same key the auth.SessionFromContext uses (via
// auth.InjectSessionForTest, the test-only injection helper).
func contextWithSession(ctx context.Context, session *auth.Session) context.Context {
	return auth.InjectSessionForTest(ctx, session)
}

// Ensure imports are used (avoid compile errors if a future edit
// removes a usage).
var _ = db.ErrGameNotFound
var _ = atomic.LoadInt32
var _ chi.RouteParams

// TestHandleSettingsGET_JSONIncludesPlaintextPassword (Story 9.6,
// AC1/AC2): the JSON branch of HandleSettingsGET returns the
// sanitized config plus a precomputed base_uri and the admin
// password plaintext (when populated by a successful login).
//
// The test does NOT hardcode the actual password value (security
// best practice — test sources are usually world-readable). It
// asserts only that the field is present, non-empty, and matches
// the value of testAdminPasswordHash's plaintext. The plaintext
// is "adminpass" (set in admin_auth_test.go's testAdminPasswordHash
// closure) — but we deliberately do NOT compare against the literal
// string; we only assert that the field is non-empty and well-formed.
func TestHandleSettingsGET_JSONIncludesPlaintextPassword(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 9300},
		Admin: types.AdminConfig{
			Username:          "admin",
			PasswordHash:      testAdminPasswordHash,
			PasswordPlaintext: "test-plaintext-not-the-real-one", // placeholder for the assertion
		},
	}
	h, _ := newSettingsTestHandler(t, cfg)

	session := h.SessionStore.Create("admin")
	req := httptest.NewRequest(http.MethodGet, "/admin/api/admin/settings", nil)
	req.Header.Set("Accept", "application/json")
	ctx := contextWithSession(req.Context(), session)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.HandleSettingsGET(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json (JSON branch)", ct)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	var resp struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, w.Body.String())
	}
	if resp.Data == nil {
		t.Fatal("response data is nil")
	}

	// base_uri must be set and well-formed (http://127.0.0.1:9300/).
	baseURI, _ := resp.Data["base_uri"].(string)
	wantBase := "http://127.0.0.1:9300/"
	if baseURI != wantBase {
		t.Errorf("base_uri = %q, want %q", baseURI, wantBase)
	}

	// password plaintext MUST be present and non-empty (AC1).
	pwd, ok := resp.Data["password"].(string)
	if !ok {
		t.Fatal("response data is missing the plaintext password field (AC1)")
	}
	if pwd == "" {
		t.Error("plaintext password field is empty (AC1 — field should be populated after login)")
	}
	// Assert it matches what we put in cfg. We deliberately avoid
	// hardcoding the literal "adminpass" string in the test source.
	if pwd != "test-plaintext-not-the-real-one" {
		t.Errorf("plaintext password mismatch (got %d chars, want 36)", len(pwd))
	}

	// Sanity: the sanitized config must NOT include the password hash
	// (R6-AC-CONFIG-SECRETS — the hash belongs in PUT-only flows, never
	// in a GET response).
	if _, hasHash := resp.Data["password_hash"]; hasHash {
		t.Error("response leaked password_hash (R6-AC-CONFIG-SECRETS violation)")
	}
	adminSection, hasAdmin := resp.Data["admin"]
	if hasAdmin && adminSection != nil {
		t.Errorf("response should not include an admin section; got %v", adminSection)
	}
}

// TestHandleSettingsGET_JSONOmitsPasswordWhenNotLoggedIn (Story 9.6):
// when no successful login has populated the in-memory plaintext
// (e.g. an API-key-authenticated script hits the endpoint with a
// session from a different auth path, or the server restarted and
// the in-memory cache is cold), the response must omit the password
// field rather than send an empty string (which the JS would render
// as a zero-length bullet mask and confuse the operator).
func TestHandleSettingsGET_JSONOmitsPasswordWhenNotLoggedIn(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 9300},
		Admin: types.AdminConfig{
			Username:     "admin",
			PasswordHash: testAdminPasswordHash,
			// PasswordPlaintext intentionally empty.
		},
	}
	h, _ := newSettingsTestHandler(t, cfg)

	session := h.SessionStore.Create("admin")
	req := httptest.NewRequest(http.MethodGet, "/admin/api/admin/settings", nil)
	req.Header.Set("Accept", "application/json")
	ctx := contextWithSession(req.Context(), session)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.HandleSettingsGET(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, hasPwd := resp.Data["password"]; hasPwd {
		t.Errorf("response should omit the password field when no login has populated it; got %v", resp.Data["password"])
	}
	// base_uri should still be present.
	if _, hasURI := resp.Data["base_uri"]; !hasURI {
		t.Error("base_uri should always be present in the JSON response")
	}
}

// TestHandleSettingsGET_HTMLBranchUnchanged (Story 9.6): the HTML
// branch (no Accept header) must continue to serve the admin shell —
// the existing /admin/settings page and the Settings sidebar link
// depend on it. A regression that flips the default branch would
// break Power-mode navigation.
func TestHandleSettingsGET_HTMLBranchUnchanged(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}
	h, _ := newSettingsTestHandler(t, cfg)

	session := h.SessionStore.Create("admin")
	req := httptest.NewRequest(http.MethodGet, "/admin/api/admin/settings", nil)
	// No Accept header → HTML branch.
	ctx := contextWithSession(req.Context(), session)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.HandleSettingsGET(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html (HTML branch — no Accept header)", ct)
	}
	// The stub adminHTMLFn returns "<html>admin shell</html>".
	body := w.Body.String()
	if !strings.Contains(body, "admin shell") {
		t.Errorf("HTML body should contain the admin shell stub; got %q", body)
	}
}

// TestHandleAuthLoginPOST_PopulatesPasswordPlaintext (Story 9.6):
// a successful JSON login must cache the plaintext password in
// cfg.Admin.PasswordPlaintext so the dashboard widget can reveal
// it via the subsequent GET /admin/api/admin/settings call.
func TestHandleAuthLoginPOST_PopulatesPasswordPlaintext(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}
	h, _ := newSettingsTestHandler(t, cfg)
	// Confirm the cold state: no plaintext cached.
	if h.Config.Admin.PasswordPlaintext != "" {
		t.Fatalf("precondition: cold config should have empty PasswordPlaintext, got %q", h.Config.Admin.PasswordPlaintext)
	}

	// POST /admin/api/auth/login with valid creds.
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "adminpass"})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	h.HandleAuthLoginPOST(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}

	// The in-memory config should now have the plaintext cached.
	// Read under the read lock (the same contract resolveConfig
	// uses).
	h.configMu.RLock()
	got := h.Config.Admin.PasswordPlaintext
	h.configMu.RUnlock()
	if got == "" {
		t.Error("PasswordPlaintext not populated after successful login (Story 9.6)")
	}
	if got != "adminpass" {
		// We compare against the literal here only because the
		// matching testAdminPasswordHash closure uses "adminpass"
		// — keeping the test self-explanatory. The hash itself is
		// the source of truth and is used by bcrypt below.
		t.Errorf("PasswordPlaintext = %q, want %q", got, "adminpass")
	}
}
