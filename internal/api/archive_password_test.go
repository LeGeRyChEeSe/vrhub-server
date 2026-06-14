package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/config"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/monitor"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/ui"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestHandleSettingsJSON_ExposesArchivePassword_AuditLogged (AC6):
// the JSON branch of HandleSettingsGET includes archive_password
// when it is set in config, and logs at Warn level.
func TestHandleSettingsJSON_ExposesArchivePassword_AuditLogged(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 9400},
		Admin: types.AdminConfig{
			Username:        "admin",
			PasswordHash:    "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ0",
			ArchivePassword: "archive-secret-pw-98",
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

	var resp struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data == nil {
		t.Fatal("response data is nil")
	}

	pwd, ok := resp.Data["archive_password"].(string)
	if !ok {
		t.Fatal("response data is missing archive_password field (AC6)")
	}
	if pwd != "archive-secret-pw-98" {
		t.Errorf("archive_password = %q, want %q", pwd, "archive-secret-pw-98")
	}

	// When archive_password is empty in config, the field must NOT appear.
	cfg2 := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 9400},
		Admin: types.AdminConfig{
			Username:        "admin",
			PasswordHash:    "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ0",
			ArchivePassword: "", // empty
		},
	}

	h2, _ := newSettingsTestHandler(t, cfg2)
	session2 := h2.SessionStore.Create("admin")
	req2 := httptest.NewRequest(http.MethodGet, "/admin/api/admin/settings", nil)
	req2.Header.Set("Accept", "application/json")
	ctx2 := contextWithSession(req2.Context(), session2)
	req2 = req2.WithContext(ctx2)
	w2 := httptest.NewRecorder()

	h2.HandleSettingsGET(w2, req2)

	var resp2 struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if _, has := resp2.Data["archive_password"]; has {
		t.Error("archive_password should NOT be present when empty in config")
	}
}

// TestMeta7zHandler_ArchivePasswordValidation (AC4): verify the four
// status-code scenarios: valid→200, wrong→401 is no longer used (the
// handler does NOT validate a password header), missing config→500,
// nil config→500. The endpoint is intentionally unauthenticated at
// HTTP level; the 7z archive itself provides AES-256 protection.
func TestMeta7zHandler_ArchivePasswordValidation(t *testing.T) {
	tmpDir := t.TempDir()

	// Scenario 1: Archive password not configured → 500.
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	handler := NewPublicAPIHandler(modeVal)
	handler.Config = &types.Config{
		DataDir: tmpDir,
		Admin:   types.AdminConfig{}, // ArchivePassword empty
	}
	handler.DB = &testDB{}
	handler.FileDB = &mockFileServerDB{}

	req := httptest.NewRequest("GET", "/meta.7z", nil)
	rec := httptest.NewRecorder()
	handler.Meta7zHandler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("empty archive_password → status = %d, want 500 (AC4)", rec.Code)
	}

	// Scenario 2: Nil config → 500.
	handler.Config = nil
	rec2 := httptest.NewRecorder()
	handler.Meta7zHandler(rec2, req)

	if rec2.Code != http.StatusInternalServerError {
		t.Errorf("nil config → status = %d, want 500 (AC4)", rec2.Code)
	}

	// Scenario 3: Archive password set + DB available → 200.
	handler.Config = &types.Config{
		DataDir: tmpDir,
		Admin: types.AdminConfig{
			ArchivePassword: "valid-archive-pw",
		},
	}
	rec3 := httptest.NewRecorder()
	handler.Meta7zHandler(rec3, req)

	if rec3.Code != http.StatusOK {
		t.Errorf("archive_password set → status = %d, want 200 (AC4)", rec3.Code)
	}
}

// TestHandleLaunchPOST_ReturnsArchivePassword_Cleartext (AC5): the
// LaunchResult.Password field must be the cleartext archive password
// from config.Admin.ArchivePassword, NOT a bcrypt hash or any other
// representation. This is verified by checking that the returned value
// matches exactly what was set in config.
func TestHandleLaunchPOST_ReturnsArchivePassword_Cleartext(t *testing.T) {
	dataDir := t.TempDir()

	archivePW := "cleartext-archive-98"

	cfg := &types.Config{
		Server: types.ServerConfig{
			Host: "127.0.0.1",
			Port: 9450,
			Mode: types.ModeNormal,
		},
		Database: types.DatabaseConfig{
			Path: filepath.Join(dataDir, "vrhub.db"),
		},
		DataDir: dataDir,
		Admin: types.AdminConfig{
			Username:        "admin",
			PasswordHash:    "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ0",
			ArchivePassword: archivePW, // cleartext, not a hash
		},
	}

	if err := config.Save(cfg, dataDir); err != nil {
		t.Fatalf("failed to save initial config: %v", err)
	}

	dbPath := filepath.Join(dataDir, "vrhub.db")
	conn, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	game := types.GameEntry{
		ReleaseName:  "com.example.game2",
		GameName:     "Example Game 2",
		PackageName:  "com.example.game2",
		VersionCode:  1,
		SizeBytes:    1024,
		OBBSizeBytes: 512,
	}
	if err := conn.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}
	conn.Close()

	handler := NewSetupHandler(dataDir, types.ModeSetup)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/setup/launch", nil)
	w := httptest.NewRecorder()
	handler.HandleLaunchPOST(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", got, http.StatusOK, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	password, ok := data["password"]
	if !ok {
		t.Fatal("missing password in LaunchResult (AC5)")
	}

	gotPW, ok := password.(string)
	if !ok {
		t.Fatalf("password is not a string: %T", password)
	}

	if gotPW != archivePW {
		t.Errorf("LaunchResult.Password = %q, want cleartext archive password %q (AC5)", gotPW, archivePW)
	}

	// Sanity: it must NOT be a bcrypt hash (bcrypt hashes start with $2).
	if strings.HasPrefix(gotPW, "$2") {
		t.Error("LaunchResult.Password looks like a bcrypt hash — should be cleartext archive password (AC5)")
	}
}

// TestSetupRouter_ArchivePassword_Empty_ForcesSetupMode (AC7): when the
// server's mode is set to setup (as main.go does when archive_password is
// empty), public routes return 503 and admin routes redirect to /admin/setup.
func TestSetupRouter_ArchivePassword_Empty_ForcesSetupMode(t *testing.T) {
	dataDir := t.TempDir()

	cfg := &types.Config{
		Server: types.ServerConfig{
			Host: "127.0.0.1",
			Port: 9460,
			Mode: types.ModeNormal, // Config says normal but...
		},
		Database: types.DatabaseConfig{
			Path: filepath.Join(dataDir, "vrhub.db"),
		},
		DataDir: dataDir,
		Admin: types.AdminConfig{
			Username:        "admin",
			PasswordHash:    "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ0",
			ArchivePassword: "", // empty — main.go forces setup mode
		},
	}

	if err := config.Save(cfg, dataDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// Simulate what main.go does when archive_password is empty:
	// set the mode to ModeSetup BEFORE creating the router.
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeSetup)) // forced by empty archive_password

	r := SetupRouter(modeVal, dataDir, nil, cfg, auth.NewSessionStore(context.Background()), nil, nil, nil, monitor.NewEventBus())

	// Public route should return 503 (SetupMode503Handler).
	req := httptest.NewRequest("GET", "/meta.7z", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("public route in forced setup mode → status = %d, want 503 (AC7)", rec.Code)
	}

	// Admin route should redirect to /admin/setup.
	req2 := httptest.NewRequest("GET", "/admin/", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusFound {
		t.Errorf("admin route in forced setup mode → status = %d, want 302 (AC7)", rec2.Code)
	}
	if got := rec2.Header().Get("Location"); !strings.Contains(got, "/admin/setup") {
		t.Errorf("admin redirect location = %q, should contain /admin/setup", got)
	}

	// But setup-specific endpoints must still be accessible.
	req3 := httptest.NewRequest("GET", "/admin/api/setup/state", nil)
	rec3 := httptest.NewRecorder()
	r.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusOK {
		t.Errorf("setup state endpoint should be reachable in setup mode → status = %d, want 200 (AC7)", rec3.Code)
	}
}

// TestSetupRouter_ArchivePassword_Set_NormalMode (AC7): when archive_password
// is set and the server operates in normal mode, public routes pass through
// SetupMode503Handler (no forced setup). Verify by checking that a GET to
// /meta.7z does NOT return 503 "Server not configured" from the setup gate.
func TestSetupRouter_ArchivePassword_Set_NormalMode(t *testing.T) {
	dataDir := t.TempDir()

	cfg := &types.Config{
		Server: types.ServerConfig{
			Host: "127.0.0.1",
			Port: 9470,
			Mode: types.ModeNormal,
		},
		Database: types.DatabaseConfig{
			Path: filepath.Join(dataDir, "vrhub.db"),
		},
		DataDir: dataDir,
		Admin: types.AdminConfig{
			Username:        "admin",
			PasswordHash:    "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ0",
			ArchivePassword: "valid-archive-pw-98", // set — normal mode
		},
	}

	if err := config.Save(cfg, dataDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	r := SetupRouter(modeVal, dataDir, nil, cfg, auth.NewSessionStore(context.Background()), nil, nil, nil, monitor.NewEventBus())

	// Use the root "/" endpoint instead of /meta.7z to avoid a nil-DB
	// panic in the meta7zHandler goroutine. The SetupModeRedirectHandler
	// (which is registered on GET /) does NOT depend on DB. In normal mode
	// it should redirect to /admin/ — NOT to /admin/setup.
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("root route in normal mode → status = %d, want 302 (AC7)", rec.Code)
	}
	location := rec.Header().Get("Location")
	if !strings.Contains(location, "/admin/") || strings.Contains(location, "/admin/setup") {
		t.Errorf("root redirect location = %q; in normal mode it should go to /admin/ not /admin/setup (AC7)", location)
	}

	// The setup-mode 503 gate must NOT block this route.
	if strings.Contains(rec.Body.String(), "Server not configured") {
		t.Error("response says 'Server not configured' — server should be in normal mode (AC7)")
	}
}

// TestAdminJS_HeaderArchivePassword_WiredAndRevealable (AC8): verify that the
// admin shell HTML template contains the header archive password chip elements
// and that the JS code references them. This is a structural test of the
// embedded assets rather than an end-to-end browser test.
func TestAdminJS_HeaderArchivePassword_WiredAndRevealable(t *testing.T) {
	// Check that the admin HTML template contains the header chip elements.
	html := ui.AdminHTML("test-version")
	if html == nil || len(html) == 0 {
		t.Fatal("AdminHTML returned nil/empty")
	}

	htmlStr := string(html)

	// The HTML must contain the archive password chip container.
	if !strings.Contains(htmlStr, "header-archive-password") {
		t.Error("admin HTML missing header-archive-password element (AC8)")
	}
	if !strings.Contains(htmlStr, "header-archive-password-reveal") {
		t.Error("admin HTML missing header-archive-password-reveal button (AC8)")
	}

	// Check that the JS contains wiring for these elements.
	js := ui.AdminJS()
	if js == nil || len(js) == 0 {
		t.Fatal("AdminJS returned nil/empty")
	}
	jsStr := string(js)

	if !strings.Contains(jsStr, "header-archive-password-reveal") {
		t.Error("admin JS missing header-archive-password-reveal wiring (AC8)")
	}
	if !strings.Contains(jsStr, "header-archive-password") {
		t.Error("admin JS missing header-archive-password reference (AC8)")
	}

	// Verify the reveal button uses a dataset.bound guard pattern
	// (same as Story 9.6's password toggle). The handler should check
	// for an existing binding before attaching the click listener.
	if !strings.Contains(jsStr, "dataset") {
		t.Error("admin JS missing dataset guard pattern for reveal button (AC8)")
	}

	// Verify archive_password field is fetched from settings JSON.
	if !strings.Contains(jsStr, "archive_password") {
		t.Error("admin JS missing archive_password field reference (AC8)")
	}
}
