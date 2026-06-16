package api

import (
	"net/http"
	"strings"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/ui"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/update"
)

// RegisterStatsHTMLRenderer wires the /admin/stats HTML renderer into
// internal/ui. Called from SetupRouter (router.go) so the renderer is
// set before any request can hit /admin/stats.
//
// Why this mirrors RegisterDocsHTMLRenderer (Story 6.6) and is NOT
// a package init():
//   - Tests that import internal/ui without internal/api would
//     otherwise see ui.AdminStatsHTML() == nil and silently get an
//     empty 200 body (the very contract being defended).
//   - Package init() ordering is implicit and surprising; SetupRouter
//     is an explicit entry point.
//
// Story 7.5 T3 (Subtask 3.2).
func RegisterStatsHTMLRenderer() {
	ui.SetAdminStatsRenderer(adminStatsHTMLBytes)
}

// adminStatsHTMLBytes renders the /admin/stats page. The page is a
// thin shell — the actual table is filled in client-side by admin.js
// after it fetches /admin/api/stats. This keeps the server response
// small and avoids server-side HTML escaping for the dynamic game
// names (the JS uses textContent, see admin.js renderStatsTable).
//
// The page is the same admin shell as /admin and /admin/docs (it
// reuses /admin/static/admin.css and /admin/static/admin.js) so the
// sidebar, mode toggle, and i18n are inherited. The renderer is
// hand-rolled (no html/template) to keep the page in one file and
// avoid a template parser dependency for a 60-line page.
func adminStatsHTMLBytes() []byte {
	v := update.CurrentVersion.String()
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>VRHub Server - Statistics</title>
<link rel="stylesheet" href="/admin/static/admin.css?v=` + v + `">
</head>
<body class="mode-power">
<div class="container">
<aside class="sidebar" id="sidebar">
<div class="sidebar-brand">VRHub Server</div>
<a href="/admin/dashboard" class="sidebar-link">Dashboard</a>
<div class="sidebar-category">Monitoring</div>
<a href="/admin/monitoring" class="sidebar-link">Live Monitoring</a>
<div class="sidebar-category">Statistics</div>
<span class="sidebar-link active" aria-disabled="true">Usage Statistics</span>
</aside>

<section class="main-content" role="region" aria-label="Statistics content">
<h1 data-i18n="stats_title">Usage Statistics</h1>
<p class="text-muted" data-i18n="stats_intro">Per-game download counts, bandwidth, and last-seen timestamp. Sorted by download count.</p>

<div id="stats-table" class="card">
<p data-i18n="stats_loading">Loading…</p>
</div>
</section>
</div>

<script src="/admin/static/admin.js?v=` + v + `"></script>
</body>
</html>`)
	return []byte(b.String())
}

// HandleStatsGET returns the per-game download stats as JSON.
//
// Story 7.5 T3: mode-gated at the handler level — Michel mode gets
// 404 (consistent with /admin/monitoring, /admin/docs, /admin/settings).
// The mode is read from the vrhub-mode cookie (set by the admin
// shell) via AdminHandler.isPowerMode; the route itself is always
// registered, so the 404 contract is per-request inside this handler.
//
// Response shape (per AC2):
//
//	{"data": {"stats": [
//	    {
//	        "hash": "<md5>",
//	        "game_name": "...",
//	        "package_name": "...",
//	        "download_count": 42,
//	        "last_download_at": 1749379200,
//	        "total_bandwidth_bytes": 123456789,
//	        "game_file_size": 12345
//	    },
//	    ...
//	]}}
//
// Sorted by download_count DESC, last_download_at DESC (handled by
// ListGameStats). Includes all games (corrupted or not) — a user
// can still attempt to download a corrupted file; the 404 happens
// before the stats increment so a corrupted game is included with
// download_count=0.
func (h *AdminHandler) HandleStatsGET(w http.ResponseWriter, r *http.Request) {
	if !h.isPowerMode(r) {
		// Michel mode → 404 (consistent with monitoring.go).
		http.NotFound(w, r)
		return
	}
	if h.DB == nil {
		// No DB wired (test mode) → empty stats list, not 500.
		// 503-like body so the client knows it's a server-side
		// problem and not "no games yet".
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"stats database not available","code":"STATS_UNAVAILABLE"}}`))
		return
	}

	stats, err := h.DB.ListGameStats()
	if err != nil {
		// Surface a structured error to the operator; the UI renders
		// a generic "Failed to load" message.
		writeError(w, http.StatusInternalServerError, "Failed to list stats: "+err.Error(), "STATS_LIST_FAILED")
		return
	}
	if stats == nil {
		// Always emit a JSON array, never `null` — the JS render code
		// uses `data.stats.forEach(...)` which would throw on null.
		stats = []db.GameStats{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": map[string]interface{}{
			"stats": stats,
		},
	})
}

// HandleStatsPageGET is REMOVED in Story X (UI/UX refonte,
// 2026-06-10). The /admin/stats URL is now a 302 redirect to
// /admin/#/stats (see router.go). The SPA fetches the JSON
// payload from /admin/api/stats and renders the table
// client-side via renderStatsTable() in admin.js. The
// adminStatsHTMLBytes() helper above is kept as a
// defense-in-depth fallback (callable from tests and curl users
// that bypass the SPA redirect).
