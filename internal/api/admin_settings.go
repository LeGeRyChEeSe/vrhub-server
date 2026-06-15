package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	"golang.org/x/crypto/bcrypt"
)

// Reloader is the interface the AdminHandler uses to live-rebind the
// HTTP listener when server.host or server.port changes via the
// settings page. The implementation lives in cmd/server/main.go;
// tests provide a stub.
//
// Story 6-3 Task 4.3.
type Reloader interface {
	Rebind(addr string) error
}

// UpdateConfigPusher is the interface the AdminHandler uses to push
// new update-checker config when update.enabled or update.auto-apply
// change via the settings page. The implementation lives in
// internal/update.
//
// Story 6-3 Task 5.1.
type UpdateConfigPusher func(*types.UpdateConfig)

// settingsUpdateRequest is the JSON body for PUT /admin/api/admin/settings.
// We use an explicit whitelist of fields to prevent mass-assignment
// (a malicious client setting admin.password_hash or api_key_hash).
// Story 6-3 AC1.
type settingsUpdateRequest struct {
	Server struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"server"`
	// Update block: auto_apply, auto_restart, check_interval, github_token, owner, repo.
	// The enabled field is intentionally absent — update checking is always on.
	Update struct {
		AutoApply     bool   `json:"auto_apply"`
		AutoRestart   bool   `json:"auto_restart"`
		CheckInterval string `json:"check_interval"`
		GithubToken   string `json:"github_token"`
		Owner         string `json:"owner"`
		Repo          string `json:"repo"`
	} `json:"update"`
	Metadata struct {
		RefreshInterval string `json:"refresh_interval"`
		URL             string `json:"url"`
	} `json:"metadata"`
	// Story 9.8 follow-up: allow changing the archive password and
	// game folders from the settings page.
	ArchivePassword string   `json:"archive_password,omitempty"`
	GameFolders     []string `json:"game_folders,omitempty"`
}

// settingsUpdateResponse is the JSON returned on successful settings save.
type settingsUpdateResponse struct {
	Data map[string]interface{} `json:"data"`
}

// apiKeyRegenerateResponse is the JSON returned on API key regenerate.
// The plaintext is shown ONCE; subsequent reads return only the masked
// representation.
type apiKeyRegenerateResponse struct {
	Data struct {
		APIKeyPlaintext string `json:"api_key_plaintext"`
		APIKeyHint      string `json:"api_key_hint"`
		Message         string `json:"message"`
	} `json:"data"`
}

// HandleSettingsGET serves the settings page HTML OR the settings
// JSON depending on the request's Accept header. The route is
// registered in router.go inside the protected-router mount (so the
// SessionAuthMiddleware applies).
//
//   - HTML clients (no Accept / Accept: text/html): 200 with the
//     admin shell HTML; the JS in the shell detects the URL hash and
//     can hit this same endpoint to re-fetch the JSON (Story 1.8 +
//     R13-P15 settings form).
//   - JSON clients (Accept: application/json, XHR per the
//     auth.IsJSONRequest contract): 200 with the sanitized config
//     payload, including baseUri and — when available — the admin
//     password plaintext cached in cfg.Admin.PasswordPlaintext
//     (populated on successful login, Story 9.6).
//
// Story 6-3 Task 1.3 + Subtask 1.5; Story 9.6 (dashboard config
// widget): the JS in admin.js's fetchConfig() now hits this endpoint
// instead of the non-existent /admin/api/config, and builds
// baseUri + populates the password-reveal widget from the JSON
// response.
func (h *AdminHandler) HandleSettingsGET(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	// Content-negotiate. JSON branch returns sanitized config + the
	// plaintext password (memory-only, populated by successful login).
	if auth.IsJSONRequest(r) {
		h.handleSettingsJSON(w, r)
		return
	}

	// HTML branch: serve the admin shell. R13-P4: the adminHTMLFn
	// field is set by main.go via SetAdminHTML. If nil (test wiring
	// or before main.go wired it), return a stub shell with a clear
	// "settings page" hint so operators don't see a blank page.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// Story 1.8 follow-up: use the CSRF-injecting variant so the
	// admin shell HTML carries the per-session CSRF token in a
	// <meta> tag. Without this, the logout button (and any other
	// state-changing request) cannot pass validateCSRF and ends up
	// 403ing.
	html := h.adminShellBytesWithCSRF(r)
	if len(html) == 0 {
		// Fallback: emit a minimal HTML page that at least tells the
		// operator the settings endpoint is reachable. R13-P4.
		html = []byte(`<!DOCTYPE html><html><body><h1>Admin Settings</h1>` +
			`<p>Admin shell HTML provider not wired. Set adminHandler.SetAdminHTML in main.go.</p>` +
			`</body></html>`)
	}
	w.Write(html)
}

// handleSettingsJSON serves the JSON branch of HandleSettingsGET.
// Returns the sanitized config (SanitizeConfig), augmented with a
// precomputed `base_uri` field (`http://<host>:<port>/` for typical
// loopback, no scheme guess for non-loopback hosts) and — when
// available in the in-memory config — the admin password plaintext.
//
// SECURITY (Story 9.6 decision 2026-06-10, Subtask 1.3): exposing
// the plaintext password via GET is a deliberate, user-approved
// tradeoff so the dashboard widget can reveal the password. The
// endpoint is session-protected (SessionAuthMiddleware in
// router.go) AND every read of the password is audit-logged at
// Warn level (the log entry includes the session ID prefix and the
// requesting user, but NEVER the password value itself).
//
// Production deployments should run behind HTTPS; over plaintext
// HTTP the password is observable on the wire.
func (h *AdminHandler) handleSettingsJSON(w http.ResponseWriter, r *http.Request) {
	cfg, ok := h.resolveConfig()
	if !ok {
		// Same as HandleScriptsConfigGET: no config → empty data
		// (not an error; the widget will just show its default "—").
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{},
		})
		return
	}

	// Audit trail: every time we expose the password, log it at
	// Warn level. The log carries the session ID prefix and the
	// username, but NEVER the password itself (security: a log
	// aggregator or shipping service that retains logs longer than
	// the in-memory cache should not be able to recover the
	// plaintext).
	if sess, ok := auth.SessionFromContext(r.Context()); ok && sess != nil {
		vlog.Get().Warn().
			Str("event", "admin_password_reveal").
			Str("session_id_prefix", sessionIDPrefix(sess.ID)).
			Str("username", sess.Username).
			Msg("admin password plaintext exposed via /admin/api/admin/settings")
	} else {
		// No session in ctx (shouldn't happen — the route is behind
		// SessionAuthMiddleware — but log defensively so the audit
		// trail is consistent).
		vlog.Get().Warn().
			Str("event", "admin_password_reveal").
			Str("session_id_prefix", "anonymous").
			Msg("admin password plaintext exposed via /admin/api/admin/settings (no session in ctx)")
	}

	// Build the response. Start from the sanitized map (no
	// password_hash, no api_key_hash, no api_key_plaintext per
	// SanitizeConfig's R6-AC-CONFIG-SECRETS contract) and add the
	// baseUri + the (deliberately exposed) password plaintext.
	data := SanitizeConfig(cfg)

	// Precompute baseUri: the dashboard widget displays it.
	// Build a URL of the shape `http://host:port/`. For a non-IP
	// host, the operator will typically be on HTTPS in production;
	// we pick http for loopback and document the override path
	// (the Power-mode Settings page has a real form to change
	// host/port, and the catalog docstring is the canonical
	// reference).
	host := cfg.Server.Host
	port := cfg.Server.Port
	scheme := "http"
	// For the dashboard base_uri we show the LAN IP so the operator
	// can copy-paste it into the VRHub client. Loopback is only
	// suitable for local admin access.
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = getOutboundIP()
	}
	data["base_uri"] = scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"

	// Plaintext password: only included when a successful login
	// has populated the in-memory field. If the operator has not
	// logged in via the UI (e.g. they hit this endpoint via an
	// API-key-authenticated script), the field stays empty and
	// the JSON omits it (omitempty below).
	if cfg.Admin.PasswordPlaintext != "" {
		data["password"] = cfg.Admin.PasswordPlaintext
	}

	// Story 9.8: archive password is exposed here so the header
	// chip and the dashboard widget can reveal it. Like the admin
	// password above, this is a deliberate user-approved tradeoff.
	// The value is audit-logged at Warn level whenever it is read.
	if cfg.Admin.ArchivePassword != "" {
		if sess, ok := auth.SessionFromContext(r.Context()); ok && sess != nil {
			vlog.Get().Warn().
				Str("event", "archive_password_reveal").
				Str("session_id_prefix", sessionIDPrefix(sess.ID)).
				Str("username", sess.Username).
				Msg("archive password revealed via admin settings endpoint")
		} else {
			vlog.Get().Warn().
				Str("event", "archive_password_reveal").
				Str("session_id_prefix", "anonymous").
				Msg("archive password revealed via admin settings endpoint (no session in ctx)")
		}
		data["archive_password"] = cfg.Admin.ArchivePassword
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": data,
	})
}

// HandleSettingsPUT persists settings changes. Validates the CSRF token
// (resolves 6-2 R6-CSRF-LOGOUT defer), then writes the new config to
// disk atomically and updates the in-memory config pointer (R11-HIGH-1).
//
// Story 6-3 Task 1.3 + Task 4.2.
func (h *AdminHandler) HandleSettingsPUT(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	// R6-CSRF-LOGOUT resolution: validate CSRF token. The token is
	// generated per-session and stored in a non-HttpOnly
	// `vrhub_csrf` cookie. The form submits it in `X-CSRF-Token`
	// header (or in the request body as `_csrf_token`). Constant-time
	// compare against the expected token for the current session.
	if !h.validateCSRF(r) {
		writeError(w, http.StatusForbidden, "CSRF token invalid or missing", "CSRF_INVALID")
		return
	}

	var req settingsUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		// R13-P13 / R12-3 follow-up: distinguish MaxBytesError from
		// generic JSON error so the client gets actionable feedback.
		if _, ok := err.(*http.MaxBytesError); ok {
			writeError(w, http.StatusBadRequest, "request body too large", "BODY_TOO_LARGE")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body", "INVALID_INPUT")
		return
	}

	// Validate port range.
	if req.Server.Port < 1 || req.Server.Port > 65535 {
		writeError(w, http.StatusBadRequest, "server.port must be 1-65535", "INVALID_INPUT")
		return
	}
	// R13-P9: validate host is a valid IP literal or RFC 1123 hostname,
	// length-capped. The Rebind string concat (host + ":" + port) is
	// NOT safe for IPv6 or hosts that already contain a colon.
	if req.Server.Host == "" {
		writeError(w, http.StatusBadRequest, "server.host is required", "INVALID_INPUT")
		return
	}
	if len(req.Server.Host) > 253 {
		writeError(w, http.StatusBadRequest, "server.host too long (max 253)", "INVALID_INPUT")
		return
	}
	if !isValidServerHost(req.Server.Host) {
		writeError(w, http.StatusBadRequest, "server.host must be a valid IP or hostname", "INVALID_INPUT")
		return
	}

	// Resolve current config (RLock fast path).
	cfg, ok := h.resolveConfig()
	if !ok {
		writeError(w, http.StatusInternalServerError, "config not available", "CONFIG_UNAVAILABLE")
		return
	}

	// Whitelist: only the fields listed in AC1 can be updated via this
	// endpoint. Admin credentials (username, password_hash) and
	// API key hash are managed via dedicated endpoints.
	newCfg := *cfg // value copy
	newCfg.Server.Host = req.Server.Host
	newCfg.Server.Port = req.Server.Port
	newCfg.Update.AutoApply = req.Update.AutoApply
	newCfg.Update.AutoRestart = req.Update.AutoRestart
	// M-01 (review 2026-06-11): apply the four new update fields
	// when present in the payload. Empty strings preserve the
	// current value (matches the metadata block's behavior below).
	if req.Update.CheckInterval != "" {
		d, err := parseDurationStrict(req.Update.CheckInterval)
		if err != nil {
			writeError(w, http.StatusBadRequest, "update.check_interval invalid: "+err.Error(), "INVALID_DURATION")
			return
		}
		newCfg.Update.CheckInterval = d
	}
	if req.Update.GithubToken != "" {
		newCfg.Update.GithubToken = req.Update.GithubToken
	}
	if req.Update.Owner != "" {
		newCfg.Update.Owner = req.Update.Owner
	}
	if req.Update.Repo != "" {
		newCfg.Update.Repo = req.Update.Repo
	}
	if req.Metadata.RefreshInterval != "" {
		// R13-P13: reject malformed duration with 400 instead of
		// silently dropping the change.
		d, err := parseDurationStrict(req.Metadata.RefreshInterval)
		if err != nil {
			writeError(w, http.StatusBadRequest, "metadata.refresh_interval invalid: "+err.Error(), "INVALID_DURATION")
			return
		}
		newCfg.Metadata.RefreshInterval = d
	}
	if req.Metadata.URL != "" {
		// R13-P8: validate metadata URL to prevent SSRF (cloud metadata,
		// loopback probing, file://, gopher://, etc.).
		if err := validateMetadataURL(req.Metadata.URL); err != nil {
			writeError(w, http.StatusBadRequest, "metadata.url invalid: "+err.Error(), "INVALID_URL")
			return
		}
		newCfg.Metadata.URL = req.Metadata.URL
	}

	// Story 9.8 follow-up: archive password and game folders.
	if req.ArchivePassword != "" {
		if len(req.ArchivePassword) < 8 {
			writeError(w, http.StatusBadRequest, "archive_password must be at least 8 characters", "INVALID_INPUT")
			return
		}
		newCfg.Admin.ArchivePassword = req.ArchivePassword
	}
	if req.GameFolders != nil {
		newCfg.GameFolders = req.GameFolders
	}

	// R13-P1: Persist to disk atomically AND atomically swap the
	// in-memory pointer under the write lock. The previous code
	// released the resolveConfig RLock between the disk read and the
	// pointer write, opening a TOCTOU window where two concurrent
	// PUTs could each persist their own disk and swap their own
	// pointer, leaving memory and disk in divergent states. The new
	// UpdateConfig method holds the write lock for the entire
	// read-modify-write-swap sequence.
	if err := h.UpdateConfig(&newCfg); err != nil {
		vlog.Get().Error().Err(err).Msg("settings: failed to write/swap config")
		writeError(w, http.StatusInternalServerError, "failed to save settings", "WRITE_ERROR")
		return
	}

	// R13-P9: use net.JoinHostPort for safe IPv6 / colon-aware rebind addr.
	// R13-P10: include rebind_status in the response so the UI sees a
	// failed rebind (instead of "success" while the listener is on the
	// old address).
	hostChanged := newCfg.Server.Host != cfg.Server.Host
	portChanged := newCfg.Server.Port != cfg.Server.Port
	rebindStatus := "skipped"
	if (hostChanged || portChanged) && h.Reloader != nil {
		newAddr := net.JoinHostPort(newCfg.Server.Host, strconv.Itoa(newCfg.Server.Port))
		if err := h.Reloader.Rebind(newAddr); err != nil {
			vlog.Get().Error().Err(err).Str("new_addr", newAddr).Msg("settings: live rebind failed; old listener still active")
			rebindStatus = "failed"
		} else {
			rebindStatus = "ok"
		}
	}

	// R13-P11: only push to the update checker if update-related
	// fields actually changed. Avoids noise + DoS amplifier.
	if h.UpdateConfigPusher != nil &&
		(newCfg.Update.AutoApply != cfg.Update.AutoApply ||
			newCfg.Update.AutoRestart != cfg.Update.AutoRestart ||
			newCfg.Update.CheckInterval != cfg.Update.CheckInterval ||
			newCfg.Update.GithubToken != cfg.Update.GithubToken ||
			newCfg.Update.Owner != cfg.Update.Owner ||
			newCfg.Update.Repo != cfg.Update.Repo) {
		h.UpdateConfigPusher(&newCfg.Update)
	}

	vlog.Get().Info().
		Str("host", newCfg.Server.Host).
		Int("port", newCfg.Server.Port).
		Bool("auto_apply", newCfg.Update.AutoApply).
		Bool("auto_restart", newCfg.Update.AutoRestart).
		Str("rebind_status", rebindStatus).
		Msg("settings saved")

	resp := settingsUpdateResponse{
		Data: map[string]interface{}{
			"server": map[string]interface{}{
				"host": newCfg.Server.Host,
				"port": newCfg.Server.Port,
			},
			"update": map[string]interface{}{
				"auto_apply":   newCfg.Update.AutoApply,
				"auto_restart": newCfg.Update.AutoRestart,
			},
			"rebind_status": rebindStatus,
			"message":       "Settings saved successfully",
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleAPIKeyRegeneratePOST regenerates the admin API key. Returns the
// new plaintext ONCE in the response body; the hash is persisted to
// config.toml. The previous key is invalidated immediately (the old
// hash is replaced; any subsequent VerifyAPIKey with the old plaintext
// will fail).
//
// Story 6-3 Task 3.3 + AC3.
func (h *AdminHandler) HandleAPIKeyRegeneratePOST(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	// R6-CSRF-LOGOUT resolution: CSRF check.
	if !h.validateCSRF(r) {
		writeError(w, http.StatusForbidden, "CSRF token invalid or missing", "CSRF_INVALID")
		return
	}

	plaintext, hash, err := auth.GenerateAPIKey()
	if err != nil {
		vlog.Get().Error().Err(err).Msg("api key regenerate: generation failed")
		writeError(w, http.StatusInternalServerError, "failed to generate key", "KEYGEN_ERROR")
		return
	}

	// Resolve current config and update the APIKeyHash.
	cfg, ok := h.resolveConfig()
	if !ok {
		writeError(w, http.StatusInternalServerError, "config not available", "CONFIG_UNAVAILABLE")
		return
	}
	newCfg := *cfg
	newCfg.Admin.APIKeyHash = hash
	newCfg.Admin.APIKeyPlaintext = plaintext

	// R13-P1/P12: persist + atomically swap via the real UpdateConfig method.
	if err := h.UpdateConfig(&newCfg); err != nil {
		vlog.Get().Error().Err(err).Msg("api key regenerate: write failed")
		writeError(w, http.StatusInternalServerError, "failed to save key", "WRITE_ERROR")
		return
	}

	// Audit log: never log the plaintext, just a hash of the first 8
	// bytes (operator can correlate without leaking the key).
	hint := plaintext[:4] + "..." + plaintext[len(plaintext)-4:]
	vlog.Get().Info().Str("event", "api_key_regenerate").Str("key_hint", hint).Msg("admin API key regenerated")

	resp := apiKeyRegenerateResponse{}
	resp.Data.APIKeyPlaintext = plaintext
	resp.Data.APIKeyHint = hint
	resp.Data.Message = "New API key generated. Save it now — it will not be shown again."
	writeJSON(w, http.StatusOK, resp)
}

// HandleAPIKeyRevealGET returns the plaintext of the current API key.
// R13-P6: requires CSRF (so a CSRF attacker's page can't read the
// plaintext). R13-P14: the plaintext is zeroed out from the in-memory
// config after the first successful reveal — the spec's "one-time"
// guarantee is enforced (operator must regenerate to read again).
//
// Story 6-3 Subtask 6.5.
func (h *AdminHandler) HandleAPIKeyRevealGET(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	// R13-P6: CSRF check. Reveal is a state-changing operation (it
	// nils the in-memory plaintext), so it MUST be CSRF-protected.
	if !h.validateCSRF(r) {
		writeError(w, http.StatusForbidden, "CSRF token invalid or missing", "CSRF_INVALID")
		return
	}

	cfg, ok := h.resolveConfig()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "config not available", "CONFIG_UNAVAILABLE")
		return
	}
	if cfg.Admin.APIKeyPlaintext == "" {
		// R13-D9: log the no-plaintext path for forensic visibility.
		vlog.Get().Info().Msg("api key reveal attempted; no plaintext in memory")
		writeError(w, http.StatusNotFound, "API key plaintext not available; regenerate to get a new one", "KEY_NOT_AVAILABLE")
		return
	}

	// R13-P14: zero out the in-memory plaintext under the write lock
	// so a subsequent reveal returns 404. The hash in APIKeyHash is
	// unchanged — the key is still valid for auth.
	plaintext := cfg.Admin.APIKeyPlaintext
	h.configMu.Lock()
	if h.Config != nil {
		// Replace the cfg with a copy that has the plaintext cleared.
		cleared := *h.Config
		cleared.Admin.APIKeyPlaintext = ""
		h.Config = &cleared
	}
	h.configMu.Unlock()

	vlog.Get().Info().Str("event", "api_key_reveal").Msg("admin API key revealed (plaintext now cleared from memory)")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": map[string]string{
			"api_key_plaintext": plaintext,
		},
	})
}

// HandleChangePasswordPOST handles POST /admin/api/admin/change-password.
// The operator must provide the old password (verified against the current
// bcrypt hash) and the new password (min 4 chars). The new hash is persisted
// to config.toml and the in-memory config is updated atomically.
func (h *AdminHandler) HandleChangePasswordPOST(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	if !h.validateCSRF(r) {
		writeError(w, http.StatusForbidden, "CSRF token invalid or missing", "CSRF_INVALID")
		return
	}

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "INVALID_INPUT")
		return
	}

	if req.OldPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "old_password and new_password are required", "INVALID_INPUT")
		return
	}
	if len(req.NewPassword) < 4 {
		writeError(w, http.StatusBadRequest, "new_password must be at least 4 characters", "INVALID_INPUT")
		return
	}

	cfg, ok := h.resolveConfig()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "config not available", "CONFIG_UNAVAILABLE")
		return
	}

	if !auth.ValidatePassword(cfg.Admin.PasswordHash, req.OldPassword) {
		vlog.Get().Warn().Str("event", "password_change_failed").Msg("old password mismatch")
		writeError(w, http.StatusUnauthorized, "old password is incorrect", "INVALID_CREDENTIALS")
		return
	}

	newHash, hashErr := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if hashErr != nil {
		vlog.Get().Error().Err(hashErr).Msg("password change: hash failed")
		writeError(w, http.StatusInternalServerError, "failed to hash new password", "HASH_ERROR")
		return
	}

	newCfg := *cfg
	newCfg.Admin.PasswordHash = string(newHash)
	newCfg.Admin.PasswordPlaintext = req.NewPassword

	if err := h.UpdateConfig(&newCfg); err != nil {
		vlog.Get().Error().Err(err).Msg("password change: save failed")
		writeError(w, http.StatusInternalServerError, "failed to save new password", "WRITE_ERROR")
		return
	}

	vlog.Get().Info().Str("event", "password_changed").Msg("admin password changed successfully")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": map[string]string{
			"message": "Password changed successfully",
		},
	})
}

// validateCSRF extracts the CSRF token from the request and compares
// it against the expected token for the current session. Returns true
// on match. Story 6-3 (resolves R6-CSRF-LOGOUT).
//
// R13-P7: tries the X-CSRF-Token header FIRST (the standard for
// JSON requests). Falls back to the `_csrf_token` form field, but
// ONLY when the request is form-urlencoded — for JSON requests the
// ParseForm call would consume the body and break the subsequent
// json.NewDecoder read.
//
// The expected token is derived from the session ID via a server-side
// HMAC secret (auth.CSRFSecret) so the auth secret (cookie value)
// and the anti-CSRF secret (header value) are NOT the same string
// (R13-P5). This means an XSS that reads document.cookie (or a future
// subdomain-set non-HttpOnly cookie) gets only the auth credential,
// not the anti-CSRF token.
func (h *AdminHandler) validateCSRF(r *http.Request) bool {
	expected, _ := h.csrfTokenForSession(r)
	if expected == "" {
		// No session / no token: fail closed.
		return false
	}
	// Try header first (works for both JSON and form requests).
	presented := r.Header.Get("X-CSRF-Token")
	if presented == "" {
		// R13-P7: only call ParseForm if the request is form-urlencoded.
		// For JSON requests, ParseForm may consume the body, breaking
		// the subsequent json.NewDecoder read. By checking the
		// Content-Type, we only call ParseForm when the client
		// actually posted a form.
		ct := strings.ToLower(r.Header.Get("Content-Type"))
		if strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			_ = r.ParseForm()
			presented = r.PostFormValue("_csrf_token")
		}
	}
	if presented == "" {
		return false
	}
	return constantTimeStringEq(expected, presented)
}

// csrfTokenForSession retrieves the expected CSRF token for the current
// request's session. The token is HMAC(sess.ID, server-secret) using
// the server's CSRFSecret. R13-P5: separate secret from the
// session ID so a leaked cookie value does not yield the CSRF
// token directly.
//
// Lookup strategy: prefer the session from request context
// (populated by SessionAuthMiddleware on protected routes), and
// fall back to reading the session from the cookie directly
// (the logout endpoint isn't behind the session middleware —
// it must accept expired-session logouts, and an attacker
// cannot forge the CSRF token without the server's CSRFSecret).
func (h *AdminHandler) csrfTokenForSession(r *http.Request) (string, bool) {
	if sess, ok := auth.SessionFromContext(r.Context()); ok && sess != nil {
		return auth.CSRFTokenForSession(sess.ID), true
	}
	// Fallback: read the cookie and look up the session in the store.
	id, ok := auth.ReadSessionCookie(r)
	if !ok || id == "" {
		return "", false
	}
	if h.SessionStore == nil {
		return "", false
	}
	sess := h.SessionStore.Get(id)
	if sess == nil {
		return "", false
	}
	return auth.CSRFTokenForSession(sess.ID), true
}

// constantTimeStringEq is a tiny constant-time string compare wrapper
// (avoids the subtle.ConstantTimeCompare 0/1 return convention).
func constantTimeStringEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	v := 0
	for i := 0; i < len(a); i++ {
		v |= int(a[i]) ^ int(b[i])
	}
	return v == 0
}

// adminShellBytes returns the admin HTML shell bytes. Wraps the
// existing ui.AdminHTML so HandleSettingsGET doesn't need to import
// the ui package directly.
//
// Defensive nil-check: in test wiring (and in any code path that
// constructs AdminHandler without going through NewAdminHandler +
// SetAdminHTML), adminHTMLFn may be nil. Calling a nil func panics;
// returning an empty byte slice here lets HandleSettingsGET fall
// through to its own placeholder HTML (R13-P4). This is what makes
// TestSetupRouter_LoginRoute_NotProtected pass without panicking when
// it GETs /admin/login in normal mode without a session.
func (h *AdminHandler) adminShellBytes() []byte {
	if h.adminHTMLFn == nil {
		return nil
	}
	return h.adminHTMLFn()
}

// adminShellBytesWithCSRF returns the admin shell HTML with the
// per-session CSRF token injected into the <meta name="csrf-token">
// tag. Story 1.8 follow-up (live session 2026-06-08): the previous
// logout button sent a placeholder X-CSRF-Token header, the server
// returned 403, and the JS fallback tried to clear the HttpOnly
// session cookie via document.cookie (which is impossible from JS).
// The user was stuck. The proper fix: render the real CSRF token in
// the HTML at page load so the JS can include it in state-changing
// requests.
//
// When the request has no session (e.g. /admin/login for an
// unauthenticated user), the placeholder is replaced with an empty
// string — validateCSRF fails closed for state-changing endpoints,
// which is the correct behaviour (no session = no privileged
// action allowed).
func (h *AdminHandler) adminShellBytesWithCSRF(r *http.Request) []byte {
	html := h.adminShellBytes()
	if len(html) == 0 {
		return html
	}
	token, _ := h.csrfTokenForSession(r)
	// htmlEscapeString lives in public.go (same package) — CSRF
	// tokens are hex strings (hmac + sha256) so escaping is mostly
	// defensive, but we escape anyway in case the algorithm
	// changes.
	safeToken := htmlEscapeString(token)
	return []byte(strings.Replace(string(html),
		`content="__VRHUB_CSRF_TOKEN__"`,
		`content="`+safeToken+`"`,
		1))
}

// parseDuration parses a Go duration string. Returns the parsed
// time.Duration, or an error if the string is malformed. Empty
// string returns an error (callers handle the default via the
// existing-config value).
func parseDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}

// parseDurationStrict is the strict variant: no default fallback, any
// non-empty input is passed to time.ParseDuration. Empty input is
// rejected with a clear error. R13-P13.
func parseDurationStrict(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("duration cannot be empty")
	}
	return time.ParseDuration(s)
}

// isValidServerHost validates a server.host value for the settings
// page. Accepts IPv4, IPv6 (in bracket form or as a colon-separated
// string parseable by net.ParseIP), or RFC 1123 hostnames. R13-P9.
func isValidServerHost(host string) bool {
	// Strip a single set of surrounding brackets (the form may submit
	// `[::1]` or `::1`; both should be accepted).
	h := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if ip := net.ParseIP(h); ip != nil {
		return true
	}
	// RFC 1123 hostname: labels of 1-63 chars, alphanum + hyphen,
	// separated by dots, total length ≤ 253.
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for i, c := range label {
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-'
			if !ok {
				return false
			}
			if c == '-' && (i == 0 || i == len(label)-1) {
				return false
			}
		}
	}
	return true
}

// validateMetadataURL prevents SSRF (R13-P8): rejects non-https schemes
// and hosts not in the allowlist. The metadata source is
// GitHub-hosted per architecture §1 (MetaMetadata GitHub repo).
func validateMetadataURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme must be https, got %q", u.Scheme)
	}
	allowedHosts := map[string]bool{
		"github.com":                    true,
		"raw.githubusercontent.com":     true,
		"api.github.com":                true,
		"objects.githubusercontent.com": true,
	}
	host := u.Hostname()
	if !allowedHosts[host] {
		return fmt.Errorf("host %q not in allowlist (github.com, raw.githubusercontent.com, api.github.com)", host)
	}
	return nil
}
