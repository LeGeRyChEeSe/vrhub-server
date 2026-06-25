package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestHandleTrailersResolvePOST_NoDB503 verifies the requireDB guard: with no
// game database the resolve endpoint returns 503 rather than panicking.
func TestHandleTrailersResolvePOST_NoDB503(t *testing.T) {
	cfg := &types.Config{Admin: types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash}}
	h, _ := newSettingsTestHandler(t, cfg) // h.DB is nil

	req := httptest.NewRequest(http.MethodPost, "/admin/api/trailers/resolve", nil)
	w := httptest.NewRecorder()
	h.HandleTrailersResolvePOST(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (nil DB)", w.Code)
	}
}

// TestHandleTrailersResolvePOST_Accepted verifies the happy path: with a DB the
// endpoint returns 202 and reports whether a YouTube API key is configured. An
// empty DB means the background resolver finds no games and returns immediately.
func TestHandleTrailersResolvePOST_Accepted(t *testing.T) {
	cfg := &types.Config{Admin: types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash}}
	cfg.Trailer.Language = "en" // no API key on purpose

	h, _ := newSettingsTestHandler(t, cfg)
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	h.DB = database

	req := httptest.NewRequest(http.MethodPost, "/admin/api/trailers/resolve", nil)
	w := httptest.NewRecorder()
	h.HandleTrailersResolvePOST(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "resolving" {
		t.Errorf("status = %v, want resolving", resp["status"])
	}
	if resp["youtube_api_key_set"] != false {
		t.Errorf("youtube_api_key_set = %v, want false (no key configured)", resp["youtube_api_key_set"])
	}
}
