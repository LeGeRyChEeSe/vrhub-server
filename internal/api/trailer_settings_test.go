package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestSanitizeConfig_TrailerLanguageNotKey verifies AC4 / the secret contract:
// SanitizeConfig surfaces the trailer language and a has_youtube_api_key
// boolean, but NEVER the API key itself.
func TestSanitizeConfig_TrailerLanguageNotKey(t *testing.T) {
	cfg := &types.Config{}
	cfg.Trailer.Language = "fr"
	cfg.Trailer.YouTubeAPIKey = "super-secret"

	out := SanitizeConfig(cfg)
	trailer, ok := out["trailer"].(map[string]interface{})
	if !ok {
		t.Fatalf("sanitized config missing trailer section: %v", out["trailer"])
	}
	if trailer["language"] != "fr" {
		t.Errorf("trailer.language = %v, want \"fr\"", trailer["language"])
	}
	if trailer["has_youtube_api_key"] != true {
		t.Errorf("has_youtube_api_key = %v, want true", trailer["has_youtube_api_key"])
	}
	// The raw key must not appear anywhere in the sanitized trailer map.
	if _, leaked := trailer["youtube_api_key"]; leaked {
		t.Error("SanitizeConfig leaked youtube_api_key")
	}
}

// TestHandleSettingsPUT_TrailerLanguage verifies AC4: a valid trailer language
// round-trips into the in-memory config via the settings PUT endpoint, and an
// API key supplied in the request is stored but preserved (non-empty only).
func TestHandleSettingsPUT_TrailerLanguage(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}
	cfg.Trailer.Language = "en"
	h, _ := newSettingsTestHandler(t, cfg)

	session := h.SessionStore.Create("admin")
	body, _ := json.Marshal(map[string]interface{}{
		"server":  map[string]interface{}{"host": "127.0.0.1", "port": 8080},
		"trailer": map[string]interface{}{"language": "pt-BR", "youtube_api_key": "new-key"},
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
	if h.Config.Trailer.Language != "pt-BR" {
		t.Errorf("Trailer.Language = %q, want \"pt-BR\"", h.Config.Trailer.Language)
	}
	if h.Config.Trailer.YouTubeAPIKey != "new-key" {
		t.Errorf("Trailer.YouTubeAPIKey = %q, want \"new-key\"", h.Config.Trailer.YouTubeAPIKey)
	}
}

// TestHandleSettingsPUT_TrailerLanguageInvalid rejects junk language codes.
func TestHandleSettingsPUT_TrailerLanguageInvalid(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}
	h, _ := newSettingsTestHandler(t, cfg)

	session := h.SessionStore.Create("admin")
	body, _ := json.Marshal(map[string]interface{}{
		"server":  map[string]interface{}{"host": "127.0.0.1", "port": 8080},
		"trailer": map[string]interface{}{"language": "not a valid code!!"},
	})
	req := httptest.NewRequest(http.MethodPut, "/admin/api/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	req = req.WithContext(contextWithSession(req.Context(), session))
	w := httptest.NewRecorder()
	h.HandleSettingsPUT(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid language\nbody: %s", w.Code, w.Body.String())
	}
}

// TestHandleSettingsPUT_TrailerKeyPreservedWhenEmpty verifies the secret
// preservation rule: an empty youtube_api_key in the request does NOT wipe an
// existing key (mirrors the github_token behaviour).
func TestHandleSettingsPUT_TrailerKeyPreservedWhenEmpty(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}
	cfg.Trailer.YouTubeAPIKey = "existing-key"
	h, _ := newSettingsTestHandler(t, cfg)

	session := h.SessionStore.Create("admin")
	body, _ := json.Marshal(map[string]interface{}{
		"server":  map[string]interface{}{"host": "127.0.0.1", "port": 8080},
		"trailer": map[string]interface{}{"language": "en"}, // no key field
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
	if h.Config.Trailer.YouTubeAPIKey != "existing-key" {
		t.Errorf("YouTubeAPIKey = %q, want \"existing-key\" (preserved)", h.Config.Trailer.YouTubeAPIKey)
	}
}
