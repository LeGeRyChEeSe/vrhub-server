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

// TestSanitizeConfig_TrailerLanguage verifies SanitizeConfig surfaces the
// trailer language (and nothing YouTube-API-related — that feature was removed).
func TestSanitizeConfig_TrailerLanguage(t *testing.T) {
	cfg := &types.Config{}
	cfg.Trailer.Language = "fr"

	out := SanitizeConfig(cfg)
	trailer, ok := out["trailer"].(map[string]interface{})
	if !ok {
		t.Fatalf("sanitized config missing trailer section: %v", out["trailer"])
	}
	if trailer["language"] != "fr" {
		t.Errorf("trailer.language = %v, want \"fr\"", trailer["language"])
	}
	if _, present := trailer["youtube_api_key"]; present {
		t.Error("trailer map should not contain youtube_api_key")
	}
	if _, present := trailer["has_youtube_api_key"]; present {
		t.Error("trailer map should not contain has_youtube_api_key")
	}
}

// TestHandleSettingsPUT_TrailerLanguage verifies a valid trailer language
// round-trips into the in-memory config via the settings PUT endpoint.
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
		"trailer": map[string]interface{}{"language": "pt-BR"},
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
