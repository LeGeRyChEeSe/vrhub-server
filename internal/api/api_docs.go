package api

import (
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/ui"
)

// RegisterDocsHTMLRenderer wires the docs HTML renderer into
// internal/ui. Called from `SetupRouter` (router.go) so the renderer
// is set before any request can hit `/admin/docs`.
//
// Why this is in `SetupRouter` (not in a package init()):
//   - Tests that import `internal/ui` without `internal/api` would
//     otherwise see `ui.AdminDocsHTML() == nil` and silently get an
//     empty 200 body (the very security contract being defended).
//   - Package init() ordering is implicit and surprising; SetupRouter
//     is an explicit entry point.
//
// The closure captures `endpointCatalog` and `adminDocsHTMLBytes`
// from this file; the indirection lets the UI package expose the
// page (Task 3.2: `ui.AdminDocsHTML()`) without importing
// internal/api (which would be a cycle: api already imports ui for
// AdminHTML/AdminCSS/AdminJS).
//
// Story 6.6 Task 3.2.
func RegisterDocsHTMLRenderer() {
	ui.SetAdminDocsRenderer(func() []byte {
		return adminDocsHTMLBytes(endpointCatalog)
	})
}

// EndpointDoc is the documentation entry for a single admin API
// endpoint. Fields are the canonical reference for what callers can
// expect; the `TestEndpointCatalog_AllRoutersReachable` test asserts
// each entry maps to a real route in the chi router.
type EndpointDoc struct {
	Method         string `json:"method"`
	Path           string `json:"path"`
	Auth           string `json:"auth"` // "session" | "api_key" | "public"
	Description    string `json:"description"`
	RequestSchema  string `json:"request_schema,omitempty"`
	ResponseSchema string `json:"response_schema,omitempty"`
	ExampleCurl    string `json:"example_curl"`
}

// endpointCatalog is the single source of truth for the API surface.
// New endpoints MUST add an entry here or the regression test
// `TestEndpointCatalog_AllRoutersReachable` will fail at CI time.
// Story 6.6 Task 1.1.
var endpointCatalog = []EndpointDoc{
	// ----- Public API (no auth) -----
	// Note: the public API exposes a chi catch-all route at
	// `/{hash}/*` that serves directory listings AND file downloads.
	// The catalog breaks it into 4 distinct operator-facing URLs
	// (the catch-all can match any of them) plus a separate
	// `/meta.7z` route for the meta-archive (which is a different
	// content type, hence its own route registration).
	{
		Method: "GET", Path: "/meta.7z", Auth: "public",
		Description: "Meta-archive: 7z archive of all games (gzip'd). VRHub client compatibility endpoint.",
		ExampleCurl: `curl -o meta.7z http://HOST:PORT/meta.7z`,
	},
	{
		Method: "GET", Path: "/{hash}/", Auth: "public",
		Description: "Directory listing for a specific game hash. HTML format.",
		ExampleCurl: `curl http://HOST:PORT/abc123def456789012345678abcdef00/`,
	},
	{
		Method: "GET", Path: "/{hash}/{package}/", Auth: "public",
		Description: "Directory listing for a specific package within a game hash.",
		ExampleCurl: `curl http://HOST:PORT/abc123def456789012345678abcdef00/com.example.game/`,
	},
	{
		Method: "GET", Path: "/{hash}/{package}/{file}", Auth: "public",
		Description: "File download with Range support (HTTP 206 partial content).",
		ExampleCurl: `curl -OJ http://HOST:PORT/abc123def456789012345678abcdef00/com.example.game/base.apk`,
	},

	// ----- Session-protected (human operator via web login) -----
	{
		Method: "POST", Path: "/admin/api/auth/login", Auth: "session",
		Description:    "Authenticate with username + password, set vrhub_session cookie, 302 to /admin/.",
		RequestSchema:  `{"username":"admin","password":"..."}`,
		ResponseSchema: `302 redirect to /admin/ on success, 401 on failure`,
		ExampleCurl:    `curl -c cookies.txt -X POST -H "Content-Type: application/json" -d '{"username":"admin","password":"adminpass"}' http://HOST:PORT/admin/api/auth/login`,
	},
	{
		Method: "POST", Path: "/admin/api/auth/logout", Auth: "session",
		Description: "Clear the session cookie. Idempotent — 204 even if not logged in.",
		ExampleCurl: `curl -b cookies.txt -c cookies.txt -X POST http://HOST:PORT/admin/api/auth/logout`,
	},
	{
		Method: "GET", Path: "/admin/api/games", Auth: "session",
		Description:    "List all games (paginated internally).",
		ResponseSchema: `{"data": {"games": [...], "count": N}}`,
		ExampleCurl:    `curl -b cookies.txt http://HOST:PORT/admin/api/games`,
	},
	{
		Method: "GET", Path: "/admin/api/games/{releaseName}/corruption-status", Auth: "session",
		Description:    "Get the corruption status of a specific game.",
		ResponseSchema: `{"data": {"game_id": N, "release_name": "...", "corrupted": bool, "corruption_reason": "..."}}`,
		ExampleCurl:    `curl -b cookies.txt http://HOST:PORT/admin/api/games/com.example.game/corruption-status`,
	},
	{
		Method: "POST", Path: "/admin/api/games/{releaseName}/revalidate", Auth: "session",
		Description:    "Manually re-validate a game (re-run APK + OBB validation).",
		ResponseSchema: `{"data": {"message": "Re-validation complete. ..."}}`,
		ExampleCurl:    `curl -b cookies.txt -X POST http://HOST:PORT/admin/api/games/com.example.game/revalidate`,
	},
	{
		Method: "PATCH", Path: "/admin/api/games/{releaseName}/exposed", Auth: "session",
		Description:   "Toggle the exposed boolean (include/exclude from public meta).",
		RequestSchema: `{"exposed": bool}`,
		ExampleCurl:   `curl -b cookies.txt -X PATCH -H "Content-Type: application/json" -d '{"exposed":false}' http://HOST:PORT/admin/api/games/com.example.game/exposed`,
	},
	{
		Method: "DELETE", Path: "/admin/api/games/{releaseName}", Auth: "session",
		Description: "Remove a game from the database (does NOT delete files).",
		ExampleCurl: `curl -b cookies.txt -X DELETE http://HOST:PORT/admin/api/games/com.example.game`,
	},
	{
		Method: "POST", Path: "/admin/api/games/rescan", Auth: "session",
		Description:    "Trigger a full directory rescan (re-imports APKs).",
		ResponseSchema: `{"data": {"files_scanned": N, "games_added": N, "games_removed": N, "total_size_bytes": N}}`,
		ExampleCurl:    `curl -b cookies.txt -X POST http://HOST:PORT/admin/api/games/rescan`,
	},
	{
		Method: "POST", Path: "/admin/api/trailers/resolve", Auth: "session",
		Description:    "Resolve trailers for all games in the background (Story 11.3). With a YouTube API key set, resolves a specific video per game; without one, games keep their YouTube search-link fallback.",
		ResponseSchema: `{"status": "resolving", "youtube_api_key_set": bool, "message": "..."}`,
		ExampleCurl:    `curl -b cookies.txt -X POST http://HOST:PORT/admin/api/trailers/resolve`,
	},
	{
		Method: "GET", Path: "/admin/api/update/status", Auth: "session",
		Description:    "Server health + update checker status.",
		ResponseSchema: `{"data": {"running": bool, "update_state": "...", "current_version": "..."}}`,
		ExampleCurl:    `curl -b cookies.txt http://HOST:PORT/admin/api/update/status`,
	},
	{
		Method: "POST", Path: "/admin/api/update/apply", Auth: "session",
		Description: "Trigger an update check + apply if auto-apply enabled.",
		ExampleCurl: `curl -b cookies.txt -X POST http://HOST:PORT/admin/api/update/apply`,
	},
	{
		Method: "POST", Path: "/admin/api/update/reset", Auth: "session",
		Description: "Reset the update state machine (clear stuck Running/Failed).",
		ExampleCurl: `curl -b cookies.txt -X POST http://HOST:PORT/admin/api/update/reset`,
	},
	{
		Method: "POST", Path: "/admin/api/update/restart", Auth: "session",
		Description: "Trigger an immediate server restart after a staged update (restart-pending state).",
		ExampleCurl: `curl -b cookies.txt -X POST http://HOST:PORT/admin/api/update/restart`,
	},
	{
		Method: "GET", Path: "/admin/api/update/changelog", Auth: "session",
		Description:    "Fetch the last GitHub releases for the configured repo (tag, body, html_url).",
		ResponseSchema: `{"data": [{"tag": "v0.1.2", "version": "0.1.2", "body": "...", "html_url": "..."}]}`,
		ExampleCurl:    `curl -b cookies.txt http://HOST:PORT/admin/api/update/changelog`,
	},

	// ----- Settings (Story 6.3, session-protected) -----
	{
		Method: "GET", Path: "/admin/api/admin/settings", Auth: "session",
		Description:    "Read the current server, update, and metadata config. When called with Accept: application/json the response includes a precomputed base_uri and — after a successful login — the admin password plaintext (memory-only, audit-logged at Warn level). Production deployments MUST run behind HTTPS; over HTTP the password is observable on the wire.",
		ResponseSchema: `{"data": {"server": {...}, "update": {...}, "metadata": {...}, "base_uri": "http://host:port/", "password": "..."}}`,
		ExampleCurl:    `curl -b cookies.txt -H "Accept: application/json" http://HOST:PORT/admin/api/admin/settings`,
	},
	{
		Method: "PUT", Path: "/admin/api/admin/settings", Auth: "session",
		Description:   "Update config. Rejects malformed input (port range, SSRF on metadata.url, etc.).",
		RequestSchema: `{"server": {"host": "0.0.0.0", "port": 8080}, "update": {"enabled": true, "auto_apply": false}, "metadata": {"refresh_interval": "24h", "url": "https://github.com/..."}}`,
		ExampleCurl:   `curl -b cookies.txt -X PUT -H "Content-Type: application/json" -d '{"server":{"host":"0.0.0.0","port":39457},"update":{"enabled":true,"auto_apply":false}}' http://HOST:PORT/admin/api/admin/settings`,
	},
	{
		Method: "POST", Path: "/admin/api/admin/api-key/regenerate", Auth: "session",
		Description:    "Generate a new admin API key. Returns the plaintext ONCE in the response; the plaintext is then zeroed from memory after the first /api-key reveal.",
		ResponseSchema: `{"data": {"api_key_plaintext": "64-hex-chars", "api_key_hint": "abcd...wxyz", "message": "Save it now — it will not be shown again."}}`,
		ExampleCurl:    `curl -b cookies.txt -X POST http://HOST:PORT/admin/api/admin/api-key/regenerate`,
	},
	{
		Method: "GET", Path: "/admin/api/admin/api-key", Auth: "session",
		Description:    "Reveal the API key plaintext (only available after a regenerate; zeroed after first reveal).",
		ResponseSchema: `{"data": {"api_key_plaintext": "..."}} or 404 KEY_NOT_AVAILABLE`,
		ExampleCurl:    `curl -b cookies.txt http://HOST:PORT/admin/api/admin/api-key`,
	},
	{
		Method: "POST", Path: "/admin/api/admin/change-password", Auth: "session",
		Description:   "Change the admin login password. Requires the old password and a new password (min 4 chars).",
		RequestSchema: `{"old_password": "...", "new_password": "..."}`,
		ExampleCurl:   `curl -b cookies.txt -X POST -H "Content-Type: application/json" -d '{"old_password":"old","new_password":"new"}' http://HOST:PORT/admin/api/admin/change-password`,
	},

	// ----- Script-friendly (X-API-Key, Story 6.4-6.5) -----
	{
		Method: "GET", Path: "/admin/api/scripts/games", Auth: "api_key",
		Description:    "List all games. Same handler as /admin/api/games; alias for scripts.",
		ResponseSchema: `{"data": {"games": [...], "count": N}}`,
		ExampleCurl:    `curl -H "X-API-Key: YOUR_KEY" http://HOST:PORT/admin/api/scripts/games`,
	},
	{
		Method: "DELETE", Path: "/admin/api/scripts/games/{releaseName}", Auth: "api_key",
		Description: "Remove a game from the database (does NOT delete files).",
		ExampleCurl: `curl -X DELETE -H "X-API-Key: YOUR_KEY" http://HOST:PORT/admin/api/scripts/games/com.example.game`,
	},
	{
		Method: "PATCH", Path: "/admin/api/scripts/games/{releaseName}/exposed", Auth: "api_key",
		Description:   "Toggle the exposed boolean.",
		RequestSchema: `{"exposed": bool}`,
		ExampleCurl:   `curl -X PATCH -H "Content-Type: application/json" -H "X-API-Key: YOUR_KEY" -d '{"exposed":false}' http://HOST:PORT/admin/api/scripts/games/com.example.game/exposed`,
	},
	{
		Method: "PATCH", Path: "/admin/api/scripts/games/{releaseName}", Auth: "api_key",
		Description:   "Update game metadata (e.g. release_name, game_name, version_code, version_name). Currently returns 501 — stub for future metadata-editing features. The TestEndpointCatalog_AllRoutersReachable drift gate guarantees this entry stays in sync with the router; do NOT remove without also removing the route.",
		RequestSchema: `{"game_name": "string", "version_code": N, "version_name": "string", "release_name": "string"}`,
		ExampleCurl:   `curl -X PATCH -H "Content-Type: application/json" -H "X-API-Key: YOUR_KEY" -d '{"game_name":"New Name","version_code":42}' http://HOST:PORT/admin/api/scripts/games/com.example.game`,
	},
	{
		Method: "POST", Path: "/admin/api/scripts/apps", Auth: "api_key",
		Description:    "Trigger a full directory rescan (alias of /admin/api/games/rescan).",
		ResponseSchema: `{"data": {"files_scanned": N, "games_added": N, "games_removed": N, "total_size_bytes": N}}`,
		ExampleCurl:    `curl -X POST -H "X-API-Key: YOUR_KEY" http://HOST:PORT/admin/api/scripts/apps`,
	},
	{
		Method: "GET", Path: "/admin/api/scripts/config", Auth: "api_key",
		Description:    "Read the sanitized config (passwords + API key EXCLUDED).",
		ResponseSchema: `{"data": {"server": {...}, "update": {...}, "metadata": {...}, "database": {...}}}`,
		ExampleCurl:    `curl -H "X-API-Key: YOUR_KEY" http://HOST:PORT/admin/api/scripts/config`,
	},
	{
		Method: "PUT", Path: "/admin/api/scripts/config", Auth: "api_key",
		Description:   "Update config. Same validation as /admin/api/admin/settings (port range, SSRF, etc.).",
		RequestSchema: `{"server": {...}, "update": {...}, "metadata": {...}}`,
		ExampleCurl:   `curl -X PUT -H "Content-Type: application/json" -H "X-API-Key: YOUR_KEY" -d '{"server":{"host":"0.0.0.0","port":39457}}' http://HOST:PORT/admin/api/scripts/config`,
	},
	{
		Method: "POST", Path: "/admin/api/scripts/backup", Auth: "api_key",
		Description:    "Download a ZIP archive of config.toml + manifest.json. Also persists a copy to {data-dir}/backups/.",
		ResponseSchema: `application/zip binary`,
		ExampleCurl:    `curl -o backup-$(date +%Y%m%d).zip -H "X-API-Key: YOUR_KEY" -X POST http://HOST:PORT/admin/api/scripts/backup`,
	},
	{
		Method: "POST", Path: "/admin/api/scripts/restore", Auth: "api_key",
		Description:    "Restore config.toml and/or vrhub.db from a backup zip (multipart form-data, field 'file'). Atomic: tmp + rename. Server restart required for changes to take effect.",
		ResponseSchema: `{"data": {"restored": ["config.toml","vrhub.db"], "message": "..."}}`,
		ExampleCurl:    `curl -X POST -H "X-API-Key: YOUR_KEY" -F "file=@backup.zip" http://HOST:PORT/admin/api/scripts/restore`,
	},
	{
		Method: "GET", Path: "/admin/api/scripts/status", Auth: "api_key",
		Description:    "Server health (alias of /admin/api/update/status for script-friendly access).",
		ResponseSchema: `{"data": {"running": bool, "update_state": "...", "current_version": "..."}}`,
		ExampleCurl:    `curl -H "X-API-Key: YOUR_KEY" http://HOST:PORT/admin/api/scripts/status`,
	},
	{
		Method: "POST", Path: "/admin/api/scripts/update/apply", Auth: "api_key",
		Description: "Trigger an update check + apply if auto-apply enabled (alias of /admin/api/update/apply).",
		ExampleCurl: `curl -X POST -H "X-API-Key: YOUR_KEY" http://HOST:PORT/admin/api/scripts/update/apply`,
	},
	{
		Method: "GET", Path: "/admin/api/scripts/_ping", Auth: "api_key",
		Description:    "Health-check endpoint. Returns 200 if the X-API-Key is valid; useful for monitoring tools.",
		ResponseSchema: `{"data": {"authenticated": true, "via": "api_key"}}`,
		ExampleCurl:    `curl -H "X-API-Key: YOUR_KEY" http://HOST:PORT/admin/api/scripts/_ping`,
	},

	// ----- Documentation -----
	{
		Method: "GET", Path: "/admin/api/docs", Auth: "session",
		Description:    "THIS endpoint — returns the full API catalog as JSON. Power User only (Michel mode → 404).",
		ResponseSchema: `{"data": {"endpoints": [...], "generated_at": "..."}}`,
		ExampleCurl:    `curl -b cookies.txt 'http://HOST:PORT/admin/api/docs?mode=power'`,
	},
	{
		Method: "GET", Path: "/admin/docs", Auth: "session",
		Description:    "HTML browsable version of the API documentation. Power User only (Michel mode → 404).",
		ResponseSchema: `text/html`,
		ExampleCurl:    `curl -b cookies.txt 'http://HOST:PORT/admin/docs?mode=power'`,
	},

	// ----- Story 7.5: usage statistics -----
	//
	// Both endpoints are session-protected and mode-gated (Michel
	// mode → 404, like monitoring/docs/settings). The stats page
	// data flows as JSON; the HTML page is a thin shell that the
	// client-side admin.js fills in after fetch.
	{
		Method: "GET", Path: "/admin/api/stats", Auth: "session",
		Description:    "Per-game download statistics (count, bandwidth, last download, file size). Power User only (Michel mode → 404). Sorted by download count DESC.",
		ResponseSchema: `{"data": {"stats": [{"hash": "<md5>", "game_name": "...", "package_name": "...", "download_count": 42, "last_download_at": 1749379200, "total_bandwidth_bytes": 12345, "game_file_size": 150}]}}`,
		ExampleCurl:    `curl -b cookies.txt -H "Cookie: vrhub-mode=power" http://HOST:PORT/admin/api/stats`,
	},
	{
		Method: "GET", Path: "/admin/stats", Auth: "session",
		Description:    "HTML usage-statistics page (admin shell chrome + JS-rendered table). Power User only (Michel mode → 404).",
		ResponseSchema: `text/html`,
		ExampleCurl:    `curl -b cookies.txt -H "Cookie: vrhub-mode=power" http://HOST:PORT/admin/stats`,
	},

	// ----- Story 7.6: network reachability -----
	//
	// Single session-protected GET, NOT mode-gated (reachability
	// info is useful to both Michel and Power users — Michel needs
	// to know if the network is down while diagnosing a "metadata
	// won't refresh" complaint). Returns 503 NOT_CONFIGURED when
	// the NetworkChecker is not wired.
	{
		Method: "GET", Path: "/admin/api/network-status", Auth: "session",
		Description:    "Reachability snapshot of the two external services (GitHub, MetaMetadata). Updated every 60s by the background NetworkChecker. NOT mode-gated (read-only, useful to both modes).",
		ResponseSchema: `{"data": {"github": "ok|degraded|offline|unknown", "metadata": "ok|degraded|offline|unknown", "checked_at": 1749379200, "all_ok": bool}}`,
		ExampleCurl:    `curl -b cookies.txt http://HOST:PORT/admin/api/network-status`,
	},
}

// HandleAPIDocsGET returns the endpoint catalog as JSON. Story 6.6
// Task 2.1.
func (h *AdminHandler) HandleAPIDocsGET(w http.ResponseWriter, r *http.Request) {
	// AC2: Michel mode returns 404. Detected via the same
	// `?mode=power` query param that Story 6-1 uses for mode
	// forcing. The shell always sets this on Power User pages.
	// Subtask 4.2 deviation: the spec says "handled by chi's default
	// not-found handler" but this endpoint serves a JSON API; a JSON
	// 404 body is more useful to API clients. The status code IS 404.
	if !clientOptsIntoPowerMode(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"API docs are Power User only","code":"MODE_MICHEL_NOT_ALLOWED"}}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": map[string]interface{}{
			"endpoints":    endpointCatalog,
			"generated_at": time.Now().UTC().Format(time.RFC3339),
		},
	})
}

// HandleDocsPageGET is REMOVED in Story X (UI/UX refonte,
// 2026-06-10). The /admin/docs URL is now a 302 redirect to
// /admin/#/api-docs (see router.go). The SPA fetches the JSON
// catalog from /admin/api/docs and renders the accordion
// client-side via renderDocsCatalog() in admin.js. The
// adminDocsHTMLBytes() helper below is kept as a defense-in-depth
// fallback (callable from tests and curl users that bypass the
// SPA redirect).

// clientOptsIntoPowerMode returns true if the request explicitly opts
// into Power User mode via the `?mode=power` query param OR a custom
// `X-Power-Mode: 1` header.
//
// ⚠️ THIS IS A UI SIGNAL, NOT AN AUTHORIZATION CHECK. ⚠️
//
// The function name was previously `isPowerUserRequest` which
// misleadingly suggested an auth decision. It is not. This is purely
// client-supplied; the caller MUST combine it with a real auth check
// (session cookie or X-API-Key middleware). The `?mode=power` query
// param alone is sufficient to flip the handler to Power User mode
// (the only protection is the upstream auth middleware). Operators
// with bookmarked or chat-shared links to `/admin/docs?mode=power`
// will see the docs page; this is the intended UX.
//
// Story 6-1 introduced the `?mode=power` query param (for the admin
// shell to force Power mode for testing). Story 6.6 added the
// `X-Power-Mode: 1` header as a programmatic alternative for scripts
// that don't want to expose the param in URLs.
func clientOptsIntoPowerMode(r *http.Request) bool {
	if r.URL.Query().Get("mode") == "power" {
		return true
	}
	if r.Header.Get("X-Power-Mode") == "1" {
		return true
	}
	return false
}

// adminDocsHTMLBytes renders the catalog as a static HTML page.
// Self-contained (no external CSS/JS deps for the basic structure);
// the admin shell's `admin.js` adds the "Copy curl" button handler.
//
// This is a hand-rolled HTML generator (not html/template) to keep
// the docs page entirely in one file with no template parsing
// overhead. The structure is:
//   - <h1> API Reference
//   - <h2> Public API (no auth)
//   - <h2> Session-protected (human operator)
//   - <h2> X-API-Key protected (scripts)
//   - <table> per group, one row per endpoint
func adminDocsHTMLBytes(catalog []EndpointDoc) []byte {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>API Reference — VRHub Server</title>`)
	b.WriteString(`<style>
body { font-family: -apple-system, sans-serif; max-width: 1200px; margin: 2rem auto; padding: 0 1rem; color: #222; }
h1 { border-bottom: 2px solid #444; padding-bottom: 0.5rem; }
h2 { margin-top: 2rem; color: #2a4d8f; }
table { width: 100%; border-collapse: collapse; margin-top: 0.5rem; }
th, td { padding: 0.5rem; text-align: left; border-bottom: 1px solid #eee; vertical-align: top; }
th { background: #f4f6f8; }
code { background: #f0f0f0; padding: 0.1rem 0.3rem; border-radius: 3px; font-size: 0.9em; }
pre { background: #f0f0f0; padding: 0.5rem; border-radius: 4px; overflow-x: auto; }
.method-GET { color: #2563eb; }
.method-POST { color: #16a34a; }
.method-PUT { color: #ea580c; }
.method-PATCH { color: #9333ea; }
.method-DELETE { color: #dc2626; }
.auth-session { background: #dbeafe; color: #1e40af; padding: 0.1rem 0.4rem; border-radius: 3px; font-size: 0.85em; }
.auth-api_key { background: #dcfce7; color: #14532d; padding: 0.1rem 0.4rem; border-radius: 3px; font-size: 0.85em; }
.auth-public { background: #f4f4f5; color: #52525b; padding: 0.1rem 0.4rem; border-radius: 3px; font-size: 0.85em; }
.copy-btn { background: #2a4d8f; color: #fff; border: 0; padding: 0.3rem 0.6rem; border-radius: 3px; cursor: pointer; font-size: 0.85em; }
.copy-btn:hover { background: #1e3a6e; }
details { margin-top: 0.3rem; }
summary { cursor: pointer; color: #666; }
</style></head><body>`)
	b.WriteString(`<h1>API Reference</h1>`)
	b.WriteString(`<p>VRHub Server admin REST API. Endpoints grouped by auth type. Click an endpoint to expand request/response schemas and a copy-able curl example.</p>`)

	groups := []struct {
		Title string
		Auth  string
	}{
		{"Public API (no auth)", "public"},
		{"Session-protected (human operator via web login)", "session"},
		{"X-API-Key protected (scripts and integrations)", "api_key"},
	}

	for _, g := range groups {
		b.WriteString("<h2>")
		b.WriteString(htmlEscape(g.Title))
		b.WriteString("</h2>")
		b.WriteString("<table>")
		b.WriteString("<tr><th>Method</th><th>Path</th><th>Auth</th><th>Description</th><th></th></tr>")
		for _, ep := range catalog {
			if ep.Auth != g.Auth {
				continue
			}
			b.WriteString("<tr>")
			b.WriteString("<td><strong class=\"method-")
			b.WriteString(ep.Method)
			b.WriteString("\">")
			b.WriteString(ep.Method)
			b.WriteString("</strong></td>")
			b.WriteString("<td><code>")
			b.WriteString(htmlEscape(ep.Path))
			b.WriteString("</code></td>")
			b.WriteString("<td><span class=\"auth-")
			b.WriteString(ep.Auth)
			b.WriteString("\">")
			b.WriteString(ep.Auth)
			b.WriteString("</span></td>")
			b.WriteString("<td>")
			b.WriteString(htmlEscape(ep.Description))
			b.WriteString("<details><summary>details</summary>")
			if ep.RequestSchema != "" {
				b.WriteString("<div><strong>Request:</strong><pre>")
				b.WriteString(htmlEscape(ep.RequestSchema))
				b.WriteString("</pre></div>")
			}
			if ep.ResponseSchema != "" {
				b.WriteString("<div><strong>Response:</strong><pre>")
				b.WriteString(htmlEscape(ep.ResponseSchema))
				b.WriteString("</pre></div>")
			}
			b.WriteString("<div><strong>curl:</strong><pre class=\"curl-example\">")
			b.WriteString(htmlEscape(ep.ExampleCurl))
			b.WriteString("</pre><button class=\"copy-btn\">Copy</button></div>")
			b.WriteString("</details></td>")
			b.WriteString("</tr>")
		}
		b.WriteString("</table>")
	}

	b.WriteString(`<script>
// Copy-curl buttons: read the sibling <pre class="curl-example">'s
// textContent (auto-decoded by the browser) and write it to the
// clipboard. The previous design used a "data-copy=..." attribute
// that double-encoded the curl (HTML-escape on output, getAttribute
// decodes once on read) — a curl containing "&" would end up as
// "&amp;" in the clipboard. textContent round-trips correctly.
document.addEventListener('click', function(e) {
    if (!e.target.classList || !e.target.classList.contains('copy-btn')) return;
    var pre = e.target.parentNode.querySelector('pre.curl-example');
    var text = pre ? (pre.textContent || '') : '';
    if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(function() {
            e.target.textContent = 'Copied!';
            setTimeout(function() { e.target.textContent = 'Copy'; }, 1500);
        });
    } else {
        // Fallback for very old browsers.
        var ta = document.createElement('textarea');
        ta.value = text; document.body.appendChild(ta); ta.select();
        try { document.execCommand('copy'); } catch (e) {}
        document.body.removeChild(ta);
    }
});
</script>`)
	b.WriteString("</body></html>")
	return []byte(b.String())
}

// htmlEscape escapes a string for safe insertion into HTML
// (text content AND attribute values). Wraps the standard library
// `html.EscapeString` so all 5 special chars are handled (`& < > " '`).
// Defense-in-depth: the catalog values are developer-controlled, but
// the escape is centralized so any future user-supplied value is
// safe by default.
func htmlEscape(s string) string {
	return html.EscapeString(s)
}
