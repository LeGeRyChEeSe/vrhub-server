package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// newStatsTestHandler builds a minimal AdminHandler for the stats
// endpoint tests. sessionStore is non-nil (the route is behind
// SessionAuthMiddleware in production); the tests call the handler
// directly so they don't go through the middleware.
func newStatsTestHandler(t *testing.T, d *db.DB) *AdminHandler {
	t.Helper()
	tmpDir := t.TempDir()
	ss := auth.NewSessionStore(context.Background())
	t.Cleanup(ss.Stop)
	return NewAdminHandler(tmpDir, nil, d, ss, &types.Config{DataDir: tmpDir})
}

// TestMain-style registration: ensure the stats HTML renderer is
// wired before any test that hits ui.AdminStatsHTML(). (In
// production SetupRouter calls RegisterStatsHTMLRenderer; in this
// test file we drive the handler directly so we register
// ourselves once.)
//
// Story X: HandleStatsPageGET is REMOVED (the /admin/stats URL is
// now a 302 redirect to /admin/#/stats). The renderer is still
// registered so tests that exercise ui.AdminStatsHTML() directly
// (e.g. for the SPA shell payload) still work.
func init() {
	RegisterStatsHTMLRenderer()
}

// TestHandleStatsGET_PowerMode_ReturnsSortedStats is the AC2 happy
// path: 3 games with distinct download counts come back ordered by
// count DESC, with all required fields populated.
func TestHandleStatsGET_PowerMode_ReturnsSortedStats(t *testing.T) {
	d, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// Insert 3 games with distinct counts.
	entries := []struct {
		hash  string
		pkg   string
		count int64
	}{
		{"stats0000000000000000000000000000a1", "com.g1", 5},
		{"stats0000000000000000000000000000a2", "com.g2", 10},
		{"stats0000000000000000000000000000a3", "com.g3", 0}, // never downloaded
	}
	for _, e := range entries {
		if err := d.InsertGame(types.GameEntry{
			ReleaseName: e.pkg, GameName: e.pkg, PackageName: e.pkg,
			VersionCode: 1, SizeBytes: 100, OBBSizeBytes: 50, Hash: e.hash, Exposed: true,
		}); err != nil {
			t.Fatalf("insert %s: %v", e.pkg, err)
		}
		for i := int64(0); i < e.count; i++ {
			if err := d.IncrementDownloadStats(e.hash, 100); err != nil {
				t.Fatalf("increment %s: %v", e.pkg, err)
			}
		}
	}

	h := newStatsTestHandler(t, d)

	req := httptest.NewRequest("GET", "/admin/api/stats", nil)
	req.AddCookie(&http.Cookie{Name: "vrhub-mode", Value: "power"})
	rec := httptest.NewRecorder()

	h.HandleStatsGET(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp struct {
		Data struct {
			Stats []map[string]interface{} `json:"stats"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Stats) != 3 {
		t.Fatalf("stats length = %d, want 3", len(resp.Data.Stats))
	}
	// Order: count=10, count=5, count=0.
	wantHashes := []string{
		"stats0000000000000000000000000000a2",
		"stats0000000000000000000000000000a1",
		"stats0000000000000000000000000000a3",
	}
	wantCounts := []float64{10, 5, 0}
	for i, s := range resp.Data.Stats {
		if s["hash"] != wantHashes[i] {
			t.Errorf("position %d: hash = %v, want %v", i, s["hash"], wantHashes[i])
		}
		if s["download_count"] != wantCounts[i] {
			t.Errorf("position %d: download_count = %v, want %v", i, s["download_count"], wantCounts[i])
		}
		// Required fields per AC2 contract.
		for _, f := range []string{"hash", "game_name", "package_name", "download_count", "last_download_at", "total_bandwidth_bytes", "game_file_size"} {
			if _, ok := s[f]; !ok {
				t.Errorf("position %d: missing field %q", i, f)
			}
		}
	}
	// game_file_size = size_bytes + obb_size_bytes = 100 + 50 = 150.
	if resp.Data.Stats[0]["game_file_size"] != float64(150) {
		t.Errorf("game_file_size = %v, want 150", resp.Data.Stats[0]["game_file_size"])
	}
}

// TestHandleStatsGET_MichelMode_404 verifies the AC4 contract: the
// handler returns 404 when the vrhub-mode cookie is "michel".
func TestHandleStatsGET_MichelMode_404(t *testing.T) {
	d, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	h := newStatsTestHandler(t, d)

	req := httptest.NewRequest("GET", "/admin/api/stats", nil)
	req.AddCookie(&http.Cookie{Name: "vrhub-mode", Value: "michel"})
	rec := httptest.NewRecorder()

	h.HandleStatsGET(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (Michel mode 404 gate)", rec.Code)
	}
}

// TestHandleStatsGET_NoModeCookie_404: missing cookie is also
// "not Power mode" per the isPowerMode implementation.
func TestHandleStatsGET_NoModeCookie_404(t *testing.T) {
	d, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	h := newStatsTestHandler(t, d)

	req := httptest.NewRequest("GET", "/admin/api/stats", nil)
	// No cookie set.
	rec := httptest.NewRecorder()

	h.HandleStatsGET(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no cookie)", rec.Code)
	}
}

// TestHandleStatsGET_NoGames_EmptyArray: an empty DB returns
// {"data": {"stats": []}}, NOT a 404 and NOT a null body. The JS
// render code uses .forEach on the array.
func TestHandleStatsGET_NoGames_EmptyArray(t *testing.T) {
	d, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	h := newStatsTestHandler(t, d)

	req := httptest.NewRequest("GET", "/admin/api/stats", nil)
	req.AddCookie(&http.Cookie{Name: "vrhub-mode", Value: "power"})
	rec := httptest.NewRecorder()

	h.HandleStatsGET(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Verify the body is a valid JSON object with stats: [].
	var resp struct {
		Data struct {
			Stats []map[string]interface{} `json:"stats"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Stats == nil {
		t.Error("stats = nil, want [] (empty array, not null)")
	}
	if len(resp.Data.Stats) != 0 {
		t.Errorf("len(stats) = %d, want 0", len(resp.Data.Stats))
	}
}

// TestHandleStatsPageGET_PowerMode_200 and
// TestHandleStatsPageGET_MichelMode_404 are REMOVED in Story X
// (UI/UX refonte, 2026-06-10). The /admin/stats URL is now a 302
// redirect to /admin/#/stats; the HTML page handler
// (HandleStatsPageGET) is gone. The SPA shell's #section-stats
// renders the table client-side via renderStatsTable() in
// admin.js.

// TestStats_PersistAcrossRestart is the AC8 contract: stats written
// to the DB survive a close + reopen.
func TestStats_PersistAcrossRestart(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	d1, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db 1: %v", err)
	}
	hash := "persist0000000000000000000000000abc"
	if err := d1.InsertGame(types.GameEntry{
		ReleaseName: "com.persist", GameName: "Persist", PackageName: "com.persist",
		VersionCode: 1, SizeBytes: 1024, Hash: hash, Exposed: true,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	now := time.Now().Unix()
	for i := 0; i < 7; i++ {
		if err := d1.IncrementDownloadStats(hash, 512); err != nil {
			t.Fatalf("increment: %v", err)
		}
	}
	if err := d1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen and verify the values are preserved.
	d2, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db 2: %v", err)
	}
	defer d2.Close()

	stats, err := d2.GetStatsForHash(hash)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if stats.DownloadCount != 7 {
		t.Errorf("DownloadCount = %d, want 7 (preserved across restart)", stats.DownloadCount)
	}
	if stats.TotalBandwidthBytes != 7*512 {
		t.Errorf("TotalBandwidthBytes = %d, want %d", stats.TotalBandwidthBytes, 7*512)
	}
	if stats.LastDownloadAt < now-1 {
		t.Errorf("LastDownloadAt = %d, want >= %d (preserved across restart)", stats.LastDownloadAt, now-1)
	}
}
