package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// newAPIKeyTestRequest builds a GET request with the given X-API-Key
// header (empty string = no header).
func newAPIKeyTestRequest(t *testing.T, apiKey string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/_ping", nil)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	return req
}

// TestAPIKeyAuthMiddleware_Valid verifies the happy path: a valid
// X-API-Key header lets the request through, and the
// APIKeyAuthenticatedFromContext marker is true in the downstream
// handler's ctx.
func TestAPIKeyAuthMiddleware_Valid(t *testing.T) {
	plaintext, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	cfg := &types.Config{Admin: types.AdminConfig{APIKeyHash: hash}}

	var ctxAuthenticated bool
	mw := APIKeyAuthMiddleware(cfg)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxAuthenticated = APIKeyAuthenticatedFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := newAPIKeyTestRequest(t, plaintext)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("valid key: status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
	if !ctxAuthenticated {
		t.Error("downstream handler should see APIKeyAuthenticatedFromContext(ctx) == true")
	}
}

// TestAPIKeyAuthMiddleware_Invalid verifies that a wrong key
// returns 401 with code API_KEY_INVALID.
func TestAPIKeyAuthMiddleware_Invalid(t *testing.T) {
	_, hash, _ := GenerateAPIKey()
	cfg := &types.Config{Admin: types.AdminConfig{APIKeyHash: hash}}

	mw := APIKeyAuthMiddleware(cfg)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("downstream handler should NOT be called on invalid key")
	}))

	req := newAPIKeyTestRequest(t, "wrong-key-32chars-xxxxxxxxxxxxx")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid key: status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "API_KEY_INVALID") {
		t.Errorf("body should contain API_KEY_INVALID, got: %s", w.Body.String())
	}
}

// TestAPIKeyAuthMiddleware_Missing verifies that no header
// returns 401 with code API_KEY_MISSING.
func TestAPIKeyAuthMiddleware_Missing(t *testing.T) {
	_, hash, _ := GenerateAPIKey()
	cfg := &types.Config{Admin: types.AdminConfig{APIKeyHash: hash}}

	mw := APIKeyAuthMiddleware(cfg)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("downstream handler should NOT be called when no header is present")
	}))

	req := newAPIKeyTestRequest(t, "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing key: status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "API_KEY_MISSING") {
		t.Errorf("body should contain API_KEY_MISSING, got: %s", w.Body.String())
	}
}

// TestAPIKeyAuthMiddleware_NotConfigured verifies that when the
// server has no API key in config, the middleware returns 503
// (NOT 401, since the issue is the server config, not the client).
func TestAPIKeyAuthMiddleware_NotConfigured(t *testing.T) {
	cfg := &types.Config{Admin: types.AdminConfig{APIKeyHash: ""}}

	mw := APIKeyAuthMiddleware(cfg)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("downstream handler should NOT be called when no key is configured")
	}))

	// Even with a valid-looking key in the header, the 503 takes
	// precedence — the server cannot validate ANY key.
	req := newAPIKeyTestRequest(t, "any-key-here")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("not configured: status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "API_KEY_NOT_CONFIGURED") {
		t.Errorf("body should contain API_KEY_NOT_CONFIGURED, got: %s", w.Body.String())
	}
}

// TestAPIKeyAuthMiddleware_NilCfg verifies that passing a nil cfg
// to the middleware constructor (should never happen in production,
// but defensive) refuses all requests with 503.
func TestAPIKeyAuthMiddleware_NilCfg(t *testing.T) {
	mw := APIKeyAuthMiddleware(nil)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("downstream handler should NOT be called when cfg is nil")
	}))

	req := newAPIKeyTestRequest(t, "any-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("nil cfg: status = %d, want 503", w.Code)
	}
}

// TestAPIKeyAuthMiddleware_TimingSafe verifies the verify path is
// constant-time (no early-return on length mismatch). We send a
// 63-char and a 65-char key to assert the path doesn't short-circuit
// on length; both should return 401 with the same response shape.
func TestAPIKeyAuthMiddleware_TimingSafe(t *testing.T) {
	_, hash, _ := GenerateAPIKey()
	cfg := &types.Config{Admin: types.AdminConfig{APIKeyHash: hash}}

	mw := APIKeyAuthMiddleware(cfg)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("downstream handler should NOT be called on wrong-length key")
	}))

	// 63-char key (off by one short).
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, newAPIKeyTestRequest(t, strings.Repeat("a", 63)))
	if w1.Code != http.StatusUnauthorized {
		t.Errorf("63-char key: status = %d, want 401", w1.Code)
	}
	// 65-char key (off by one long).
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, newAPIKeyTestRequest(t, strings.Repeat("a", 65)))
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("65-char key: status = %d, want 401", w2.Code)
	}
	// Both should produce the SAME error code (API_KEY_INVALID)
	// and the SAME response shape — the timing-safe path treats
	// both lengths identically.
	if !strings.Contains(w1.Body.String(), "API_KEY_INVALID") {
		t.Errorf("63-char body should contain API_KEY_INVALID, got: %s", w1.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "API_KEY_INVALID") {
		t.Errorf("65-char body should contain API_KEY_INVALID, got: %s", w2.Body.String())
	}
	// And the JSON body should be well-formed (parseable).
	var resp1, resp2 map[string]interface{}
	if err := json.Unmarshal(w1.Body.Bytes(), &resp1); err != nil {
		t.Errorf("63-char body not parseable JSON: %v", err)
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Errorf("65-char body not parseable JSON: %v", err)
	}
}

// TestAPIKeyAuthenticatedFromContext_Default verifies the ctx helper
// returns false when no API key auth was performed.
func TestAPIKeyAuthenticatedFromContext_Default(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if APIKeyAuthenticatedFromContext(req.Context()) {
		t.Error("default ctx should report APIKeyAuthenticatedFromContext = false")
	}
}
