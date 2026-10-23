package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// headerCountingWriter is a response-recorder variant that counts WriteHeader
// invocations. net/http logs "superfluous response.WriteHeader call" when a
// handler writes headers twice; ResponseRecorder silently swallows the second
// call, so the count is asserted explicitly here.
type headerCountingWriter struct {
	http.ResponseWriter
	headerWrites int
}

func (w *headerCountingWriter) WriteHeader(code int) {
	w.headerWrites++
	w.ResponseWriter.WriteHeader(code)
}

// TestScriptsConfigGET_SingleHeaderWrite locks the header contract of
// GET /admin/api/scripts/config. The handler used to call w.WriteHeader(200)
// and then delegate to writeJSON (which writes status + Content-Type itself),
// producing "superfluous response.WriteHeader call" on stderr for every
// scripts-poll config fetch (observed in the 0.1.6 self-test logs).
func TestScriptsConfigGET_SingleHeaderWrite(t *testing.T) {
	h := &AdminHandler{
		Config: &types.Config{
			Admin: types.AdminConfig{PasswordHash: "x"},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/config", nil)
	rec := httptest.NewRecorder()
	w := &headerCountingWriter{ResponseWriter: rec}

	h.HandleScriptsConfigGET(w, req)

	if w.headerWrites != 1 {
		t.Fatalf("expected exactly 1 WriteHeader call, got %d (duplicate WriteHeader before writeJSON)", w.headerWrites)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var payload struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload.Data == nil {
		t.Fatal("data envelope missing")
	}
}

// TestScriptsConfigGET_NoConfig_SingleHeaderWrite covers the empty-config
// fallback branch — the early WriteHeader(200) duplicated writeJSON there too.
func TestScriptsConfigGET_NoConfig_SingleHeaderWrite(t *testing.T) {
	h := &AdminHandler{DataDir: t.TempDir()}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/scripts/config", nil)
	rec := httptest.NewRecorder()
	w := &headerCountingWriter{ResponseWriter: rec}

	h.HandleScriptsConfigGET(w, req)

	if w.headerWrites != 1 {
		t.Fatalf("expected exactly 1 WriteHeader call, got %d", w.headerWrites)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
}
