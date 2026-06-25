package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/game"
	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/monitor"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/network"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/ui"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/update"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	chi "github.com/go-chi/chi/v5"
)

// writeJSON encodes v as JSON and writes it to w with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError writes a standardized error response.
func writeError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]string{
			"message": message,
			"code":    code,
		},
	})
}

// SetupModeMiddleware returns a middleware that redirects all /admin/* requests
// to /admin/setup when the server is in setup mode.
func SetupModeMiddleware(modeVal *atomic.Value) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mode := types.ServerMode(modeVal.Load().(string))
			if mode != types.ModeSetup {
				next.ServeHTTP(w, r)
				return
			}

			// Allow access to /admin/setup itself during setup mode.
			if r.URL.Path == "/admin/setup" || r.URL.Path == "/admin/setup/" {
				next.ServeHTTP(w, r)
				return
			}

			// Allow access to /admin/api/setup/* endpoints during setup mode.
			if strings.HasPrefix(r.URL.Path, "/admin/api/setup/") {
				next.ServeHTTP(w, r)
				return
			}

			// Story 1.6: allow /admin/static/* during setup mode so the
			// setup wizard page (which references /admin/static/admin.css,
			// /admin/static/admin.js, /admin/static/setup.css, and
			// /admin/static/setup.js) can load its CSS/JS. Without this,
			// the middleware would 302 the static asset requests back to
			// /admin/setup, breaking the wizard.
			if strings.HasPrefix(r.URL.Path, "/admin/static/") {
				next.ServeHTTP(w, r)
				return
			}

			// Redirect all other admin routes to the setup page.
			http.Redirect(w, r, "/admin/setup", http.StatusFound)
		})
	}
}

// SetupModeRedirectHandler returns a handler that redirects GET / to /admin/setup
// when in setup mode, or to /admin/ when in normal mode.
//
// Note: prior to 2026-06-08 this handler returned 404 in normal mode, which was
// a UX wart — most users bookmarked `http://host:port/` and hit a dead end.
// The redirect to /admin/ lands on the admin UI (login form for unauthenticated
// users, dashboard for authenticated users), which is the canonical entry
// point for normal-mode operation.
// SetupModeRedirectHandler handles GET / by routing the request to
// the right place based on (a) the server's setup mode and (b) what
// the client is.
//
// Browsers (Accept: text/html, no JSON preference) get the
// historical behaviour: a 302 redirect to /admin/ (normal mode) or
// /admin/setup (setup mode). The admin shell is the canonical entry
// point for human operators.
//
// API clients (Accept: application/json) get a different response
// because the historical behaviour breaks them: a JSON client follows
// the 302 to /admin/, which requires a session cookie, and the
// session middleware returns 401 — the client sees "Server returned
// error: 401" and the operator sees the access log showing a 401 on
// /admin/ with no obvious root cause. The VRHub Quest client (which
// identifies as User-Agent: rclone/v1.72.1) is the canonical case:
// it always sends Accept: application/json on every request, so any
// ping/test/probe against the server root lands in this trap.
//
// For JSON clients in NORMAL mode we serve the same payload as
// /config.json — that's the public catalog config (baseUri +
// password) and is the only "API root" resource the public protocol
// exposes. The handler renders it inline rather than redirecting so
// the client doesn't have to follow a second hop to learn the
// server's address.
//
// For JSON clients in SETUP mode we keep the 302 to /admin/setup:
// the wizard has no /config.json semantics yet (the archive password
// isn't set), and the JSON client will get a clear 401 from the
// wizard routes when it tries to drive them, which is the expected
// "server not configured" signal.
func SetupModeRedirectHandler(modeVal *atomic.Value) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode := types.ServerMode(modeVal.Load().(string))
		if mode == types.ModeSetup {
			// Wizard is the right destination regardless of client
			// type — no /config.json semantics yet.
			http.Redirect(w, r, "/admin/setup", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/admin/", http.StatusFound)
	}
}

// isJSONClient reports whether the request looks like an API client
// (not a browser). We mirror the accept-header logic from
// auth.IsJSONRequest: an Accept that prefers application/json, or an
// Accept: */* with an X-Requested-With marker, or a "looks like a
// CLI tool" User-Agent (rclone is the only one we know about — the
// VRHub Quest app sends rclone/v1.72.1).
//
// Kept local to the api package to avoid a circular import with
// internal/auth.
func isJSONClient(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		return true
	}
	if strings.HasPrefix(strings.ToLower(r.UserAgent()), "rclone/") {
		// rclone is command-line only — never a human on a
		// browser. The VRHub Quest app impersonates rclone, which
		// means we have to whitelist it explicitly to avoid
		// breaking the only real client that hits this endpoint.
		return true
	}
	return false
}

// rootHandlerWithPublicHandler returns a / handler that captures
// the live public handler so it can render /config.json inline for
// JSON clients (see SetupModeRedirectHandler docstring). The
// publicHandler arg is the *PublicAPIHandler created by
// MountPublicRoutes; passing it via closure is the cleanest way
// without adding a field to the router struct.
func rootHandlerWithPublicHandler(modeVal *atomic.Value, publicHandler *PublicAPIHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode := types.ServerMode(modeVal.Load().(string))
		if mode == types.ModeSetup {
			http.Redirect(w, r, "/admin/setup", http.StatusFound)
			return
		}
		if isJSONClient(r) {
			handleConfigJSONRoot(w, r, publicHandler)
			return
		}
		http.Redirect(w, r, "/admin/", http.StatusFound)
	}
}

// handleConfigJSONRoot serves the same JSON payload as /config.json
// from the server root. Read-only — does NOT touch the cfg field
// directly, so the call is safe even when publicHandler is nil
// (e.g. in tests or pre-launch setup wiring).
//
// We delegate to publicHandler.HandleClientConfigGET so the JSON
// shape stays in sync with the /config.json endpoint (the public
// handler is the single source of truth for the response struct).
func handleConfigJSONRoot(w http.ResponseWriter, r *http.Request, publicHandler *PublicAPIHandler) {
	if publicHandler == nil {
		// Defensive: a fully-wired SetupRouter always passes the
		// public handler. If we get here with nil it means the
		// router was constructed by a test that forgot to wire it.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"server not configured","code":"NOT_CONFIGURED"}}`))
		return
	}
	publicHandler.HandleClientConfigGET(w, r)
}

// SetupMode503Handler returns a handler that serves 503 for public API routes
// when in setup mode.
func SetupMode503Handler(modeVal *atomic.Value) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mode := types.ServerMode(modeVal.Load().(string))
			if mode == types.ModeSetup {
				http.Error(w, "Server not configured", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SetupRouter creates a Chi router with setup mode handling and session-based authentication.
//
// Story 6-3 R13-P2: the optional `reloader` and `updatePusher` are
// wired into the AdminHandler by main.go after construction. The
// handlers are optional (nil = no-op) so tests can pass nil.
//
// Story 6.6: adds Power-User-only docs endpoints (`/admin/api/docs`
// JSON + `/admin/docs` HTML). Both are session-authenticated but the
// handler itself returns 404 when the request is not in Power User
// mode (detected via `?mode=power` query param or `X-Power-Mode: 1`
// header, mirroring Story 6-1's `?mode=power` shell convention). The
// route IS always registered; the 404 is per-request inside the
// handler. This avoids a special middleware that would have to
// inspect query params on every protected request.
//
// Story 6.6 R6.6-PATCH-2: the docs HTML renderer is registered here
// (NOT in a package init()) so the registration order is explicit
// and tests that import internal/ui without internal/api don't
// silently get a nil renderer.
//
// Story 7.6 T3: the optional `netChecker` is the background
// reachability checker for external services (GitHub +
// MetaMetadata). nil is acceptable in test wiring; the
// /admin/api/network-status handler returns 503 NOT_CONFIGURED
// when nil.
func SetupRouter(modeVal *atomic.Value, dataDir string, gameDB *db.DB, cfg *types.Config, sessionStore *auth.SessionStore, reloader Reloader, updatePusher UpdateConfigPusher, netChecker *network.Checker, monitorBus *monitor.EventBus) *chi.Mux {
	// Register the docs HTML renderer before any route handler can
	// call `ui.AdminDocsHTML()`. (R6.6-PATCH-2)
	RegisterDocsHTMLRenderer()

	// Story 7.5 T3: register the stats HTML renderer before any route
	// handler can call `ui.AdminStatsHTML()`. Mirrors the docs
	// pattern (R6.6-PATCH-2).
	RegisterStatsHTMLRenderer()

	// Story 1.6: register the setup wizard HTML renderer before any
	// route handler can call `ui.AdminSetupHTML()`. Mirrors the docs
	// and stats pattern (R6.6-PATCH-2).
	RegisterSetupHTMLRenderer()

	r := chi.NewRouter()

	// Access log: one Info line per HTTP request so the operator
	// can match client-side errors (e.g. "VRHub shows 401") with
	// the matching server-side log entry. Placed FIRST in the
	// middleware chain so it sees every request, including the
	// ones blocked by SetupModeMiddleware / SetupMode503Handler
	// further down the stack.
	r.Use(accessLogMiddleware)
	r.Use(MonitorMiddleware(monitorBus))

	// Public API routes protected by setup mode 503 handler.
	// Story 9.1 (B1): capture the PublicAPIHandler so setupAdminRouter
	// can wire a ConfigPropagator closure that refreshes its .Config
	// field after the setup→normal transition.
	publicRouter := chi.NewRouter()
	publicRouter.Use(SetupMode503Handler(modeVal))
	publicHandler := MountPublicRoutes(publicRouter, modeVal, gameDB, cfg)

	// Admin routes protected by setup mode middleware and session auth.
	r.Mount("/admin", setupAdminRouter(modeVal, dataDir, gameDB, cfg, sessionStore, reloader, updatePusher, netChecker, monitorBus, publicHandler))

	r.Mount("/", publicRouter)

	// GET / — redirect to setup (in setup mode) or serve a
	// content-type-aware root response in normal mode. The plain
	// SetupModeRedirectHandler is preserved for tests and for
	// callers that don't have a public handler; the inline version
	// below is what SetupRouter actually wires because it needs
	// access to the live public handler to render the JSON
	// payload for API clients (see handler docstring).
	r.Get("/", rootHandlerWithPublicHandler(modeVal, publicHandler))

	// GET /favicon.ico — reply 204 No Content in BOTH modes. Without an
	// explicit route the request falls through to the public API, which
	// returns 503 in setup mode (a console error in the browser) or a 404
	// in normal mode. We have no favicon asset, so 204 is the cleanest
	// "no icon" response that does not surface a console error. (F8)
	r.Get("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	return r
}

func setupAdminRouter(modeVal *atomic.Value, dataDir string, gameDB *db.DB, cfg *types.Config, sessionStore *auth.SessionStore, reloader Reloader, updatePusher UpdateConfigPusher, netChecker *network.Checker, monitorBus *monitor.EventBus, publicHandler *PublicAPIHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Use(SetupModeMiddleware(modeVal))

	// Setup wizard handler — created once and shared by /admin/setup
	// page (Story 1.6) and the /admin/api/setup/* endpoints below.
	setupHandler := NewSetupHandler(dataDir, types.ModeSetup)
	setupHandler.ModeVal = modeVal

	// Story 9.1 (B1): deferred-wire the ConfigPropagator closure.
	// It references adminHandler and updateHandler which are created
	// LATER in this function (in the sessionStore != nil and cfg != nil
	// branches respectively). The closure captures the local variable
	// names; on first call (after HandleLaunchPOST finishes writing
	// config.toml) the closures dereference the current values of
	// adminHandler and updateHandler.
	//
	// We use a pointer-to-pointer indirection (var adminRef **AdminHandler,
	// var updateRef **UpdateHandler) so the closure sees the LATEST
	// binding, not the value captured at declaration time. This is the
	// same pattern as admin.go's R10-RESOLVECONFIG-RACE but inverted:
	// here the values are filled in AFTER the closure is defined.
	var adminRef *AdminHandler
	var updateRef *UpdateHandler
	setupHandler.ConfigPropagator = func(newCfg *types.Config) {
		if newCfg == nil {
			return
		}
		if publicHandler != nil {
			publicHandler.Config = newCfg
		}
		if adminRef != nil {
			adminRef.Config = newCfg
		}
		if updateRef != nil {
			// Map types.UpdateConfig -> update.Config (different
			// package, identical field set; the field-name differences
			// are TOML tags, not Go field names).
			updateRef.UpdateConfig = update.Config{
				Enabled:       newCfg.Update.Enabled,
				CheckInterval: newCfg.Update.CheckInterval,
				AutoApply:     newCfg.Update.AutoApply,
				GithubToken:   newCfg.Update.GithubToken,
				Owner:         newCfg.Update.Owner,
				Repo:          newCfg.Update.Repo,
			}
		}
		// Story 6-3 R13-P11 / Task 5: push the new update-checker
		// config so the running checker sees the new owner/repo/etc.
		// The production wiring (main.go:newUpdateConfigPusher) is
		// best-effort: it logs but doesn't block. nil is OK in tests.
		if updatePusher != nil {
			updatePusher(&newCfg.Update)
		}

		// Story 9.2 (B2): late-bind the gameDB pointer.
		//
		// In setup mode, gameDB is nil at router construction (cmd/server/main.go
		// only calls db.Open when cfg != nil). After the launch transition,
		// config.toml has just been written, so we can derive the DB path from
		// newCfg.Database.Path and open the DB now. The resulting *db.DB is
		// assigned to:
		//   - adminHandler.DB  (game-route handlers read this)
		//   - adminHandler.Importer (rebuilt so rescan uses the live DB)
		//   - publicHandler.DB + publicHandler.FileDB (read for /meta.7z + file serving)
		//
		// Idempotency (R-B2-PATCH-1): the closure may be invoked twice
		// if the operator double-clicks launch (defensive). The pointer
		// assignment is a no-op on the second call (the existing
		// *db.DB is preserved), so we explicitly skip db.Open if EITHER
		// the original gameDB is set OR the admin handler's late-bound
		// DB is set. Re-opening the same SQLite file would produce a
		// second connection, which contends on the busy_timeout and
		// risks SQLITE_BUSY under load.
		//
		// R-B2-PATCH-2: explicit parentheses disambiguate the && / ||
		// precedence for human readers (Go's grammar makes && tighter
		// than ||, so the original code was correct, but the explicit
		// form is harder to misread).
		alreadyBound := gameDB != nil || (adminRef != nil && adminRef.DB != nil)
		if newCfg.Database.Path != "" && !alreadyBound {
			if opened, openErr := db.Open(newCfg.Database.Path); openErr == nil {
				vlog.Get().Info().Str("db_path", newCfg.Database.Path).Msg("setup: game DB opened on launch transition (B2 late-bind)")
				if publicHandler != nil {
					publicHandler.DB = opened
					// R-B2-PATCH-3: also late-bind StatsDB so the
					// /meta.7z + file download handlers record
					// download stats post-launch. Without this, the
					// public handler's stats counter stays nil and
					// per-game download counts are silently dropped.
					publicHandler.StatsDB = opened
					publicHandler.FileDB = opened
				}
				if adminRef != nil {
					adminRef.DB = opened
					adminRef.Importer = game.NewGameManager(opened, dataDir)
				}
			} else {
				vlog.Get().Warn().Err(openErr).Str("db_path", newCfg.Database.Path).Msg("setup: game DB late-bind failed; per-handler requireDB will return 503")
			}
		}

		vlog.Get().Info().Msg("setup: cfg propagated to public/admin/update handlers")
	}

	// GET /admin/setup — serve the setup wizard page (Story 1.6).
	// Replaces the 51-byte inline placeholder with a fully-styled
	// 4-step wizard. In normal mode, the handler redirects to /.
	r.Get("/setup", setupHandler.HandleSetupPageGET)

	// Static assets for the setup wizard (Story 1.6). The page
	// references /admin/static/setup.css and /admin/static/setup.js
	// in addition to the admin shell's admin.css / admin.js.
	r.Get("/static/setup.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ui.SetupCSS())
	})

	r.Get("/static/setup.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ui.SetupJS())
	})

	// POST /admin/api/setup/credentials — create admin credentials.
	r.Post("/api/setup/credentials", setupHandler.HandleCredentialsPOST)

	// POST /admin/api/setup/archive-password — set archive password (Story 9.8).
	r.Post("/api/setup/archive-password", setupHandler.HandleSetupArchivePasswordPOST)

	// POST /admin/api/setup/scan — scan folder for APK/OBB files.
	r.Post("/api/setup/scan", setupHandler.HandleScanPOST)

	// GET /admin/api/setup/state — server-side wizard state (Story 1.7 B1).
	// Returns {credentials_set, game_count} so the wizard JS can auto-skip
	// a step on page refresh instead of re-submitting credentials that
	// are already in config.toml (which would 409 CREDENTIALS_ALREADY_SET).
	r.Get("/api/setup/state", setupHandler.HandleSetupStateGET)

	// GET /admin/api/setup/review — review detected games.
	r.Get("/api/setup/review", setupHandler.HandleReviewGET)

	// POST /admin/api/setup/review — submit game exclusions.
	r.Post("/api/setup/review", setupHandler.HandleReviewPOST)

	// POST /admin/api/setup/launch — launch the server.
	r.Post("/api/setup/launch", setupHandler.HandleLaunchPOST)

	// Authentication routes (not protected by session middleware).
	//
	// R7-CRITICAL-SESSION-INIT: register login/logout whenever a session store exists.
	// The session store is now always created at startup (cmd/server/main.go), so the
	// route is registered even in setup mode (where the SetupModeMiddleware redirects
	// to /admin/setup) and in normal mode before config.toml is on disk (where the
	// handler reloads config from disk via config.Load(h.DataDir)). The cfg != nil
	// guard is intentionally removed — login must work after the setup→normal
	// transition where the in-memory cfg pointer at startup was nil.
	//
	// R10-TWO-HANDLERS: a SINGLE AdminHandler is now created and shared between
	// the auth route and all protected routes. The previous design constructed
	// two independent handlers each with their own Config pointer, so the
	// resolveConfig disk-load on the auth path didn't refresh the game-route
	// handler's config (and vice-versa). One handler = one config cache.
	//
	// R11-HIGH-4: a shared AdminHandler is ONLY created when a session store
	// exists. If sessionStore is nil (test-mode wiring or an operator-driven
	// misconfig), game routes are NOT mounted — the previous design would
	// have mounted them behind a middleware that pass-throughs when store is
	// nil, leaving /admin/api/games/* reachable without auth (rescan, exposed
	// toggle, delete — all data-mutating endpoints).
	//
	// The shared handler is created whenever EITHER the session store or the
	// game DB is non-nil — game routes need an admin handler too (for the
	// rescan, exposed toggle, etc.) and shouldn't lose their handler just
	// because the test wires sessionStore=nil. Auth routes are then registered
	// separately when sessionStore is non-nil, reusing the same shared handler.
	var adminHandler *AdminHandler
	switch {
	case sessionStore != nil:
		adminHandler = NewAdminHandler(dataDir, nil, gameDB, sessionStore, cfg)
		// B-02 (review 2026-06-11): wire the in-process monitor bus so
		// /admin/monitoring streams live events. The bus is created in
		// cmd/server/main.go and passed through SetupRouter; it can be
		// nil in test wiring.
		adminHandler.MonitorBus = monitorBus
		// Story 9.1 (B1): expose adminHandler to the deferred
		// ConfigPropagator closure above so the post-launch
		// propagation can refresh its .Config field.
		adminRef = adminHandler
		// Live session 2026-06-08: cmd/server/main.go never calls
		// SetAdminHTML, so adminHTMLFn stays nil and HandleSettingsGET
		// (/admin/login, /admin/dashboard, /admin/settings) emits a
		// placeholder HTML instead of the real admin shell. Wire it
		// here so every /admin/* route uses the same shell as the
		// /admin/ inline closure. R13-P4 was meant to be wired by
		// main.go but never was.
		adminHandler.SetAdminHTML(func() []byte {
			return ui.AdminHTML(update.CurrentVersion.String())
		})
		// R13-P2: wire Reloader + UpdateConfigPusher (production
		// wiring). nil is acceptable in test mode (defensive checks
		// in the handlers already skip if nil).
		if reloader != nil {
			adminHandler.Reloader = reloader
		}
		if updatePusher != nil {
			adminHandler.UpdateConfigPusher = updatePusher
		}
		// Story 7.6 T3: wire the NetworkChecker for the
		// /admin/api/network-status endpoint. nil is acceptable
		// (the handler returns 503 NOT_CONFIGURED in that case).
		if netChecker != nil {
			adminHandler.NetworkChecker = netChecker
		}
		// Wire callback so publicHandler.Config stays in sync when
		// admin settings (including host/port) are saved at runtime.
		if publicHandler != nil {
			adminHandler.OnConfigUpdated = func(newCfg *types.Config) {
				publicHandler.Config = newCfg
			}
		}
		r.Post("/api/auth/login", adminHandler.HandleAuthLoginPOST)
		r.Post("/api/auth/logout", adminHandler.HandleAuthLogoutPOST)

		// R13-P3 + live session 2026-06-08 bug fix: /admin/login MUST
		// be on the unprotected `r` (NOT on protectedRouter below).
		// The session middleware redirects unauthenticated HTML
		// clients to /admin/login — if /admin/login is itself behind
		// the middleware, we get a /admin/login → /admin/login
		// redirect loop (browser shows an empty page).
		//
		// Story 9.5 (B5): the /admin/login route now serves a
		// dedicated login page (ui.LoginHTML) that contains ONLY the
		// login card. Previously the same handler was used as the
		// /admin/settings page and it served the FULL admin shell —
		// the login form was a hidden div revealed by JS when
		// ?showLogin=1 was present. The user therefore saw a broken
		// dashboard behind the login form after the setup wizard
		// finished. The dedicated page fixes the UX (no shell, no
		// sidebar, no widgets) and is also a small XSS
		// defense-in-depth win: unauthenticated users no longer
		// receive the full shell HTML (sidebar links, dashboard
		// widgets, etc.) which an attacker controlling a
		// reflected-stored-XSS sink would have to weaponize.
		//
		// Registered here (next to the other auth routes on `r`,
		// BEFORE the protectedRouter Mount below) so chi matches
		// this unprotected route first.
		r.Get("/login", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(ui.LoginHTML(update.CurrentVersion.String()))
		})
	}
	// Note: the `case gameDB != nil` branch was removed. With sessionStore ==
	// nil, mounting game routes would expose them without auth. The protected
	// router requires sessionStore != nil (see below); game routes are
	// registered INSIDE the protected router, so a nil store prevents the
	// game routes from being served at all.

	// Protected routes: require valid session (skip auth for /setup paths).
	//
	// R10-AUTHHOST-MISMATCH: the middleware previously captured cfg.Server.Host
	// at router construction time. After a setup→normal transition (or any
	// operator-driven config change), the captured host went stale and the
	// Secure flag on Clear-Cookie mismatched the original Set-Cookie. The fix
	// is a hostGetter closure that re-reads the config on every request.
	//
	// R12-P5: the previous doc claimed "When no adminHandler exists (test-mode
	// wiring), the hostGetter returns """ — that case no longer exists.
	// With the R11-HIGH-4 gate (sessionStore != nil), the hostGetter closure
	// is only created when adminHandler is guaranteed non-nil (the switch
	// above always assigns adminHandler in the sessionStore != nil arm).
	// The `if adminHandler == nil` check inside hostGetter is now dead
	// defense-in-depth; the "" branch is unreachable in practice.
	//
	// R12-P6: the previous R11-MEDIUM-2 comment claimed the hostGetter was
	// lazily evaluated ("only resolved when a cookie IS present"). This
	// was a lie — the middleware calls hostFunc() unconditionally on
	// every request (see internal/auth/session.go:604-605), BEFORE the
	// cookie read. The cost is one RLock fast-path per request, which is
	// cheap. The doc has been corrected to reflect reality.
	//
	// R11-HIGH-4: protected router is only mounted when sessionStore != nil.
	// Otherwise game routes would be exposed without auth.
	if sessionStore != nil {
		hostGetter := func() string {
			if adminHandler == nil {
				return ""
			}
			cfg, ok := adminHandler.resolveConfig()
			if !ok {
				// R11-LOW-2: fail-closed on disk error. Returning "" would
				// classify as loopback (Secure=false), which on a production
				// HTTPS deployment with a transient disk error would emit a
				// non-Secure Clear-Cookie. Return "*" (which isLoopback treats
				// as non-loopback → Secure=true) so we never accidentally
				// downgrade a Secure cookie deletion.
				return "*"
			}
			return cfg.Server.Host
		}
		protectedRouter := chi.NewRouter()
		// Story 9.3 (B3): skip the session check for /admin/api/scripts/*
		// so script clients (cron, CI) can authenticate via X-API-Key
		// alone. The inner apiKeyRouter.Mount under protectedRouter
		// already wires APIKeyAuthMiddleware which enforces its own
		// 401 API_KEY_MISSING/INVALID contract, so the skip does NOT
		// create an auth-free hole. The prefix match is anchored to
		// "/admin/api/scripts/" (with trailing slash) so it does not
		// accidentally also match, e.g. /admin/api/scripts-foo
		// (defense-in-depth — the only path registered under
		// /api/scripts/ is the scripts sub-tree, but a future route
		// that merely STARTS WITH the same prefix would otherwise
		// inherit the auth-bypass).
		scriptsPathSkip := func(r *http.Request) bool {
			return strings.HasPrefix(r.URL.Path, "/admin/api/scripts/")
		}
		protectedRouter.Use(auth.SessionAuthMiddlewareWithHostFunc(sessionStore, hostGetter, scriptsPathSkip))

		// POST /admin/api/games/rescan — trigger full game directory rescan.
		//
		// Story 9.2 (B2): the `if gameDB != nil` guard was REMOVED. In setup
		// mode gameDB is nil at router construction, but the game routes MUST
		// be registered so they are reachable after the setup→normal
		// transition (the `gameDB` pointer is late-bound via the
		// ConfigPropagator below). Each game-route handler begins with a
		// `requireDB` nil-check that returns 503 SERVICE_UNAVAILABLE if the
		// DB is still nil at request time (operator-driven misconfig or
		// genuine race between the launch propagator and a fast click).
		if adminHandler != nil {
			// R10-TWO-HANDLERS: reuse the shared adminHandler; install
			// the game manager IF gameDB is available at construction
			// time. If nil, the late-bound DB installed by the
			// ConfigPropagator closure (B2 / 9.1) is picked up at
			// request time by the per-handler requireDB check.
			if gameDB != nil {
				gameManager := game.NewGameManager(gameDB, dataDir)
				adminHandler.Importer = gameManager
			}
			protectedRouter.Post("/api/games/rescan", adminHandler.HandleRescanPOST)

			// GET /admin/api/games/:releaseName/corruption-status — get corruption status of a game.
			protectedRouter.Get("/api/games/{releaseName}/corruption-status", adminHandler.HandleCorruptionStatusGET)

			// POST /admin/api/games/:releaseName/revalidate — manually re-validate a game.
			protectedRouter.Post("/api/games/{releaseName}/revalidate", adminHandler.HandleRevalidatePOST)

			// PATCH /admin/api/games/:releaseName/exposed — toggle game exposure status.
			protectedRouter.Patch("/api/games/{releaseName}/exposed", adminHandler.HandleExposedTogglePATCH)

			// GET /admin/api/games — list all games.
			protectedRouter.Get("/api/games", adminHandler.HandleGamesListGET)

			// DELETE /admin/api/games/:releaseName — remove game from database.
			protectedRouter.Delete("/api/games/{releaseName}", adminHandler.HandleGameDeleteDELETE)
		}

		// POST /admin/api/trailers/resolve — Story 11.3: kick off a background
		// trailer resolution pass for all games (best-effort; resolves specific
		// videos only when a YouTube API key is set). Guards on a nil DB at
		// request time via requireDB.
		protectedRouter.Post("/api/trailers/resolve", adminHandler.HandleTrailersResolvePOST)

		// Update endpoints (protected by session middleware — resolves Story 5-3 defer).
		//
		// Story 9.2 (B2): the `if cfg != nil` guard was REMOVED. The update
		// routes (and the dependent /admin/api/admin/* settings routes +
		// /admin/api/scripts/* CRUD routes) are ALWAYS registered when
		// sessionStore != nil. The updateHandler is still constructed from
		// the in-memory cfg at startup; if cfg was nil (setup mode), the
		// handler is constructed with update.DefaultConfig() defaults, and
		// the ConfigPropagator (B1) refreshes its .UpdateConfig field after
		// launch. Settings/API-key/scripts handlers rely on resolveConfig()
		// (Story 6-2 R10) which already disk-loads on demand.
		//
		// Decision: the `apiKeyRouter.Use(auth.APIKeyAuthMiddleware(cfg))`
		// was previously inside the `if cfg != nil` block. We now wrap it
		// in a defensive nil-check: API key auth requires a non-nil cfg
		// (cfg.Admin.APIKeyHash is the field it validates). In setup mode
		// (cfg==nil), the middleware is replaced by a 503-emitting handler
		// so /admin/api/scripts/* still 503 (not 401, not 404) consistently
		// with the rest of the admin API. The /api/scripts/_ping ping
		// endpoint is registered separately and returns 200 unconditionally
		// (no auth, no DB — pure reachability probe).
		if adminHandler != nil {
			updateCfg := update.DefaultConfig()
			if cfg != nil {
				// Periodic check is always enabled — never copy Enabled=false.
				// Apply per-field overrides only when the config has non-zero values
				// so DefaultConfig() fallbacks (Owner, Repo, CheckInterval) stay intact.
				if cfg.Update.CheckInterval > 0 {
					updateCfg.CheckInterval = cfg.Update.CheckInterval
				}
				if cfg.Update.GithubToken != "" {
					updateCfg.GithubToken = cfg.Update.GithubToken
				}
				if cfg.Update.Owner != "" {
					updateCfg.Owner = cfg.Update.Owner
				}
				if cfg.Update.Repo != "" {
					updateCfg.Repo = cfg.Update.Repo
				}
				updateCfg.AutoApply = cfg.Update.AutoApply
				updateCfg.AutoRestart = cfg.Update.AutoRestart
			}
			updateHandler := NewUpdateHandler(updateCfg, dataDir)
			// Wire the HTTP-shutdown callback so HandleUpdateRestartPOST
			// can close the listener before spawning the replacement process.
			// Uses a type assertion so the Reloader interface stays unchanged
			// and test mocks that only implement Rebind are unaffected.
			if reloader != nil {
				if s, ok := reloader.(interface {
					Shutdown(context.Context) error
				}); ok {
					updateHandler.ShutdownFn = s.Shutdown
				}
			}
			// Story 9.1 (B1): expose updateHandler to the deferred
			// ConfigPropagator closure so post-launch propagation
			// can refresh its .UpdateConfig (owner/repo/token/etc.).
			updateRef = updateHandler
			protectedRouter.Get("/api/update/status", updateHandler.HandleUpdateStatusGET)
			protectedRouter.Post("/api/update/apply", updateHandler.HandleUpdateApplyPOST)
			protectedRouter.Post("/api/update/reset", updateHandler.HandleUpdateResetPOST)
			protectedRouter.Post("/api/update/restart", updateHandler.HandleUpdateRestartPOST)
			protectedRouter.Get("/api/update/changelog", updateHandler.HandleChangelogGET)

			// R6-CSRF-LOGOUT resolution (Story 6-3): wrap the update
			// endpoints with the CSRF middleware too. (logout endpoint
			// gets its own middleware in HandleAuthLogoutPOST.)

			// Story 6-3 Task 1.3: settings + API key endpoints.
			protectedRouter.Get("/api/admin/settings", adminHandler.HandleSettingsGET)
			protectedRouter.Put("/api/admin/settings", adminHandler.HandleSettingsPUT)
			protectedRouter.Post("/api/admin/api-key/regenerate", adminHandler.HandleAPIKeyRegeneratePOST)
			protectedRouter.Get("/api/admin/api-key", adminHandler.HandleAPIKeyRevealGET)
			protectedRouter.Post("/api/admin/change-password", adminHandler.HandleChangePasswordPOST)

			// Story 6-4: API key middleware for script-friendly endpoints.
			// Mounted on a separate sub-router so it doesn't conflict
			// with the session-only /admin/api/admin/* routes (settings,
			// regenerate, reveal are human-only operations).
			//
			// Story 6-5: register CRUD endpoints on this sub-router.
			//
			// Story 9.2 (B2): the /api/scripts/_ping route is a pure
			// reachability probe and MUST work even when cfg is nil
			// (setup mode, pre-launch). The apiKeyRouter.Use middleware
			// applies to ALL routes in the subrouter, so we wrap the
			// CRUD sub-router in a small dispatcher that routes
			// GET /_ping directly (no middleware) and forwards
			// everything else to the auth-protected apiKeyRouter.
			// This keeps chi's single-Mount-per-path invariant and
			// preserves the AC2 contract: 503 on cfg-nil for the
			// auth-required routes, 200 on cfg-nil for _ping.
			apiKeyRouter := chi.NewRouter()
			if cfg != nil {
				apiKeyRouter.Use(auth.APIKeyAuthMiddleware(cfg))
			} else {
				// B2 fallback: cfg is nil in setup mode. The API-key
				// middleware requires a non-nil cfg (validates the hash).
				// Substitute a 503-emitting handler so /admin/api/scripts/*
				// is still registered (and discoverable) but every
				// request gets a clear 503 NOT_CONFIGURED instead of
				// being silently dropped (chi 404) or panicking. The
				// _ping reachability probe is routed AROUND this
				// middleware by the dispatcher below.
				apiKeyRouter.Use(func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						writeError(w, http.StatusServiceUnavailable, "API key authentication not yet configured (setup wizard in progress)", "NOT_CONFIGURED")
					})
				})
			}
			// Story 6.5: register the same update endpoints under the
			// scripts sub-router. The /api/scripts/status route is the
			// script-friendly alias of /api/update/status.
			apiKeyRouter.Get("/status", updateHandler.HandleUpdateStatusGET)
			apiKeyRouter.Post("/update/apply", updateHandler.HandleUpdateApplyPOST)
			// CRUD endpoints (Story 6.5)
			apiKeyRouter.Get("/games", adminHandler.HandleScriptsGamesGET)
			apiKeyRouter.Delete("/games/{releaseName}", adminHandler.HandleScriptsGameDeleteDELETE)
			apiKeyRouter.Patch("/games/{releaseName}/exposed", adminHandler.HandleScriptsGameExposedPATCH)
			apiKeyRouter.Patch("/games/{releaseName}", adminHandler.HandleScriptsGameMetadataPATCH)
			apiKeyRouter.Post("/apps", adminHandler.HandleScriptsRescanPOST)
			apiKeyRouter.Get("/config", adminHandler.HandleScriptsConfigGET)
			apiKeyRouter.Put("/config", adminHandler.HandleScriptsConfigPUT)
			apiKeyRouter.Post("/backup", adminHandler.HandleScriptsBackupPOST)
			apiKeyRouter.Post("/restore", adminHandler.HandleRestorePOST)

			// B2 dispatcher: GET /_ping bypasses the API-key middleware
			// (pure reachability probe, no auth, no DB, no cfg dep).
			// Every other path on /api/scripts/* is delegated to the
			// auth-protected apiKeyRouter.
			scriptsRouter := chi.NewRouter()
			scriptsRouter.Get("/_ping", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":{"authenticated":true,"via":"api_key"}}`))
			})
			scriptsRouter.Mount("/", apiKeyRouter)
			protectedRouter.Mount("/api/scripts", scriptsRouter)
		}

		// Story 6-3 Task 1.2: /admin/dashboard and /admin/settings page
		// routes (protected by SessionAuthMiddleware; resolve the
		// R11-DN-2 redirect deviation).
		//
		// R13-P3: also register /admin/login — the auth middleware
		// redirects unauthenticated HTML clients here, so the route
		// MUST exist. The login form is rendered by the admin shell
		// HTML; the JS hash-handler shows/hides the form based on
		// the URL hash.
		//
		// Live session 2026-06-08: /admin/login was previously
		// registered on protectedRouter (below), which created an
		// infinite redirect loop — the middleware redirects
		// unauthenticated requests to /admin/login, but /admin/login
		// was itself protected, so it redirected back to itself. The
		// login page MUST be public so the user can actually land on
		// it and POST credentials. Registration moved to the
		// unprotected `r` below this block.
		if adminHandler != nil {
			// Story X (UI/UX refonte, 2026-06-10): the admin shell is
			// a SPA with hash routing. All old /admin/<page> URLs now
			// redirect (302) to /admin/#/<route> so external bookmarks
			// keep working. The page handlers (HandleSettingsGET,
			// HandleGamesPageGET, HandleBackupPageGET, etc.) are
			// deleted — the SPA shell (mounted on /admin/) reads the
			// hash to know which section to show.
			//
			// Mapping (old path → new hash route):
			//   /admin/dashboard     → /admin/#/dashboard
			//   /admin/settings     → /admin/#/configuration
			//   /admin/games        → /admin/#/games
			//   /admin/backup       → /admin/#/backup
			//   /admin/docs         → /admin/#/api-docs
			//   /admin/stats        → /admin/#/stats
			//   /admin/monitoring   → NOT redirected (this is the SSE
			//                          endpoint consumed by
			//                          EventSource('/admin/monitoring')
			//                          from the #/monitoring hash
			//                          route; redirecting it would
			//                          break the live feed).
			redirectToSpaHash := func(target string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, "/admin/#"+target, http.StatusFound)
				}
			}
			protectedRouter.Get("/dashboard", redirectToSpaHash("/dashboard"))
			protectedRouter.Get("/settings", redirectToSpaHash("/configuration"))
			protectedRouter.Get("/games", redirectToSpaHash("/games"))
			protectedRouter.Get("/backup", redirectToSpaHash("/backup"))
			protectedRouter.Get("/docs", redirectToSpaHash("/api-docs"))
			protectedRouter.Get("/stats", redirectToSpaHash("/stats"))

			// /monitoring is NOT redirected — it's the SSE endpoint
			// consumed by EventSource('/admin/monitoring') from the
			// #/monitoring hash route. The handler 404s in Michel mode
			// (legacy mode-gate) and streams events in Power mode.
			protectedRouter.Get("/monitoring", adminHandler.HandleMonitoringSSE)

			// JSON data endpoints (consumed by the SPA). None of
			// these are redirected — they all return JSON, not HTML.
			protectedRouter.Get("/api/stats", adminHandler.HandleStatsGET)
			protectedRouter.Get("/api/docs", adminHandler.HandleAPIDocsGET)

			// Story 7.6 T1: network reachability endpoint.
			//
			//   GET /admin/api/network-status  — JSON: per-service
			//                                   status (GitHub,
			//                                   MetaMetadata) +
			//                                   checked_at + all_ok.
			//   NOT mode-gated: read-only info, useful to both
			//   Michel and Power users (Michel needs to know if
			//   the network is down while diagnosing "metadata
			//   won't refresh"). 503 if NetworkChecker is nil.
			//   Read-only GET, no CSRF.
			protectedRouter.Get("/api/network-status", adminHandler.HandleNetworkStatusGET)

			// Story 9.5 (B5): /admin/ (the root) requires a valid
			// session. The session middleware redirects
			// unauthenticated users to /admin/login. The shell is
			// the SPA — the JS reads the hash on load.
			protectedRouter.Get("/", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				html := adminHandler.adminShellBytesWithCSRF(r)
				if len(html) == 0 {
					// Defensive fallback (mirrors HandleSettingsGET).
					html = []byte(`<!DOCTYPE html><html><body><h1>VRHub Server</h1></body></html>`)
				}
				w.Write(html)
			})
		}

		r.Mount("/", protectedRouter)
	}

	// Story 9.5 (B5): the /admin/ root was previously registered
	// here on the unprotected `r` (serving the full admin shell to
	// anyone, with the login form revealed by JS). It has been
	// moved INSIDE the protectedRouter (see the `protectedRouter.Get("/",
	// ...)` block above), so unauthenticated requests now redirect
	// to /admin/login?showLogin=1 via the session middleware.

	// Serve static assets from embedded files (not protected).
	r.Get("/static/admin.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		w.Write(ui.AdminCSS())
	})

	r.Get("/static/admin.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		w.Write(ui.AdminJS())
	})

	// Story 9.5 (B5): the dedicated /admin/login page references
	// /admin/static/login.js for the form-submit glue. The asset is
	// served from the same unprotected static-asset block as
	// /admin/static/admin.css and /admin/static/admin.js so it is
	// reachable BEFORE the user authenticates.
	r.Get("/static/login.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ui.LoginJS())
	})

	// F6: self-hosted Andika WOFF2 fonts. admin.css/setup.css reference
	// these via @font-face so the brand font renders without the Google
	// Fonts CDN (the appliance may run fully offline). Served unprotected
	// (the font is needed on the login/setup pages before auth) with a long
	// immutable cache since the files are content-stable.
	r.Get("/static/fonts/{file}", func(w http.ResponseWriter, r *http.Request) {
		data := ui.Font(chi.URLParam(r, "file"))
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})

	return r
}
