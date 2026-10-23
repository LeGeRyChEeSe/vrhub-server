package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// GET /admin/api/scripts/_ping is a pure reachability probe: it must
// return 200 regardless of API-key state (setup mode included) but it
// must NOT claim "authenticated":true,"via":"api_key" when no key was
// presented. The old payload was a false positive for monitoring tools:
// a broken/missing API key still looked "authenticated".

func newPingRouter(t *testing.T, cfg *types.Config) http.Handler {
	t.Helper()
	sessionStore := auth.NewSessionStore(context.Background())
	t.Cleanup(sessionStore.Stop)
	mv := new(atomic.Value)
	mv.Store(string(types.ModeNormal))
	return SetupRouter(mv, t.TempDir(), nil, cfg, sessionStore, nil, nil, nil, nil, nil)
}

func TestScriptsPing_NoAPIKey_ReturnsNeutralPayload(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", APIKeyHash: "seeded-hash-not-used"},
	}
	router := newPingRouter(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/_ping", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (reachability probe, no key required)", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "authenticated") {
		t.Errorf("body = %q, must not claim authentication when no API key was presented", body)
	}
	if strings.Contains(body, "api_key") {
		t.Errorf("body = %q, must not claim via=api_key without a key", body)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("response missing data field: %s", body)
	}
	if ok, _ := data["ok"].(bool); !ok {
		t.Errorf("data.ok = %v, want true", data["ok"])
	}
	if ping, _ := data["ping"].(bool); !ping {
		t.Errorf("data.ping = %v, want true", data["ping"])
	}
}

func TestScriptsPing_ValidAPIKey_Still200(t *testing.T) {
	plaintext, hash, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", APIKeyHash: hash},
	}
	router := newPingRouter(t, cfg)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/_ping", nil)
	req.Header.Set("X-API-Key", plaintext)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a valid API key\nbody: %s", w.Code, w.Body.String())
	}
}
