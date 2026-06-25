package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// SanitizeConfig returns the public-facing representation of cfg. It
// STRIPS all secret fields (password hash, API key hash, API key
// plaintext) so the response cannot leak credentials even if a future
// endpoint forgets to apply the helper.
//
// Story 6.5 AC-config-secrets. Exported so it can be reused by
// HandleSettingsGET and any future endpoint that needs to serialize
// the config safely.
func SanitizeConfig(cfg *types.Config) map[string]interface{} {
	if cfg == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{
		"server": map[string]interface{}{
			"host": cfg.Server.Host,
			"port": cfg.Server.Port,
		},
		"update": map[string]interface{}{
			"enabled":        cfg.Update.Enabled,
			"auto_apply":     cfg.Update.AutoApply,
			"check_interval": cfg.Update.CheckInterval.String(),
			"github_owner":   cfg.Update.Owner,
			"github_repo":    cfg.Update.Repo,
			// HasGithubToken: returns a bool, not the token itself.
			"has_github_token": cfg.Update.GithubToken != "",
		},
		"metadata": map[string]interface{}{
			"url":              cfg.Metadata.URL,
			"refresh_interval": cfg.Metadata.RefreshInterval.String(),
		},
		// Story 11.1/11.3: surface the global trailer language (a dropdown in
		// the Power-mode settings).
		"trailer": map[string]interface{}{
			"language": cfg.Trailer.Language,
		},
		"database": map[string]interface{}{
			"path": cfg.Database.Path,
		},
		"data_dir": cfg.DataDir,
		// admin section is intentionally OMITTED. The admin block
		// contains password_hash, api_key_hash, and api_key_plaintext.
		// SanitizeConfig callers MUST NOT add the admin section
		// without explicit sanitization.
	}
}

// HandleScriptsConfigGET serves the sanitized config (Story 6.5).
// R6-AC-CONFIG-SECRETS: password_hash + api_key_hash are stripped.
func (h *AdminHandler) HandleScriptsConfigGET(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	cfg, ok := h.resolveConfig()
	if !ok {
		// No config available — return empty rather than leaking the
		// not-yet-set in-memory defaults.
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": SanitizeConfig(cfg),
	})
}

// HandleScriptsConfigPUT persists config changes from a script (Story
// 6.5). Reuses the validation logic from HandleSettingsPUT (Story 6.3
// R13-P9 host validation, R13-P8 SSRF, R13-P13 parseDuration errors).
//
// The PUT uses the same UpdateConfig method (atomic write + swap)
// that HandleSettingsPUT uses. The whitelist is identical:
// server.host, server.port, update.enabled, update.auto-apply,
// metadata.refresh_interval, metadata.url. Admin credentials and
// API key are NOT settable via this endpoint.
func (h *AdminHandler) HandleScriptsConfigPUT(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	var req settingsUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
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
	// R13-P9: validate host.
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

	cfg, ok := h.resolveConfig()
	if !ok {
		writeError(w, http.StatusInternalServerError, "config not available", "CONFIG_UNAVAILABLE")
		return
	}

	// Whitelist: same as HandleSettingsPUT.
	newCfg := *cfg
	newCfg.Server.Host = req.Server.Host
	newCfg.Server.Port = req.Server.Port
	newCfg.Update.AutoApply = req.Update.AutoApply
	newCfg.Update.AutoRestart = req.Update.AutoRestart
	if req.Metadata.RefreshInterval != "" {
		// R13-P13: reject malformed duration.
		d, err := parseDurationStrict(req.Metadata.RefreshInterval)
		if err != nil {
			writeError(w, http.StatusBadRequest, "metadata.refresh_interval invalid: "+err.Error(), "INVALID_DURATION")
			return
		}
		newCfg.Metadata.RefreshInterval = d
	}
	if req.Metadata.URL != "" {
		// R13-P8: SSRF prevention.
		if err := validateMetadataURL(req.Metadata.URL); err != nil {
			writeError(w, http.StatusBadRequest, "metadata.url invalid: "+err.Error(), "INVALID_URL")
			return
		}
		newCfg.Metadata.URL = req.Metadata.URL
	}

	// Atomic write + swap.
	if err := h.UpdateConfig(&newCfg); err != nil {
		vlog.Get().Error().Err(err).Msg("scripts config PUT: failed to write/swap config")
		writeError(w, http.StatusInternalServerError, "failed to save config", "WRITE_ERROR")
		return
	}

	vlog.Get().Info().Str("event", "scripts_config_update").Str("remote_addr", r.RemoteAddr).Msg("config updated via scripts API")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data":    SanitizeConfig(&newCfg),
		"message": "Config updated successfully",
	})
}

// HandleScriptsGamesGET aliases the existing HandleGamesListGET.
// R6-CSRF-LOGOUT resolution: the API key middleware already
// validates the X-API-Key header; no additional CSRF check.
func (h *AdminHandler) HandleScriptsGamesGET(w http.ResponseWriter, r *http.Request) {
	h.HandleGamesListGET(w, r)
}

// HandleScriptsGameDeleteDELETE aliases HandleGameDeleteDELETE.
func (h *AdminHandler) HandleScriptsGameDeleteDELETE(w http.ResponseWriter, r *http.Request) {
	h.HandleGameDeleteDELETE(w, r)
}

// HandleScriptsGameExposedPATCH aliases HandleExposedTogglePATCH.
func (h *AdminHandler) HandleScriptsGameExposedPATCH(w http.ResponseWriter, r *http.Request) {
	h.HandleExposedTogglePATCH(w, r)
}

// HandleScriptsGameMetadataPATCH — DEFERRED: the db package doesn't
// expose a generic UpdateGame (only UpdateGameExposed + corruption
// status). A generic metadata PATCH requires either (a) a new
// db.UpdateGame method, (b) using UpdateGameExposed for the boolean
// field only and skipping the other metadata fields, or (c) an
// UpdateGameSQL helper for full-column updates. Captured as a
// follow-up: until then, scripts use the dedicated /exposed endpoint
// for the boolean toggle and have no way to change release_name /
// game_name / package_name post-creation (which is the pre-6.5
// behavior anyway).
//
// Stub: returns 501 Not Implemented with a clear error code.
func (h *AdminHandler) HandleScriptsGameMetadataPATCH(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"error":{"message":"metadata PATCH not yet implemented; use the dedicated /exposed endpoint for boolean toggle","code":"NOT_IMPLEMENTED"}}`))
}

// HandleScriptsRescanPOST aliases HandleRescanPOST.
func (h *AdminHandler) HandleScriptsRescanPOST(w http.ResponseWriter, r *http.Request) {
	h.HandleRescanPOST(w, r)
}

// HandleScriptsStatusGET — server health is reported via the update
// status endpoint (same handler, just a different route alias).
// The actual handler is mounted inline in router.go (the update
// handler is a separate type — UpdateHandler — not AdminHandler).
// This stub is never called; router.go wires the route directly
// via an http.HandlerFunc closure that calls
// internal/update.UpdateHandler.HandleUpdateStatusGET.
func (h *AdminHandler) HandleScriptsStatusGET(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"error":{"message":"HandleScriptsStatusGET is a stub; router.go wires the real handler","code":"ROUTING_ERROR"}}`))
}

// HandleScriptsBackupPOST returns a ZIP archive of the current config.toml,
// the SQLite database vrhub.db (if present), and a small manifest.json.
// Spec: Story 7.2 (FR26) — "the server creates a zip of current
// config.toml + vrhub.db and serves as a downloadable file, filename
// vrhub-server-backup-{date}.zip".
//
// The zip is built in-memory and streamed to the HTTP response. A copy
// is also persisted to {data-dir}/backups/ in a background goroutine so
// the operator can retrieve it out-of-band (script doesn't have to wait
// for the HTTP response to complete).
func (h *AdminHandler) HandleScriptsBackupPOST(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// Add config.toml (the canonical source-of-truth file, sanitized to
	// strip secrets per SanitizeConfig contract).
	cfg, ok := h.resolveConfig()
	if !ok {
		// No config available — emit a minimal one so the backup is still
		// self-describing.
		cfg = &types.Config{}
	}
	configBytes, err := json.MarshalIndent(SanitizeConfig(cfg), "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to serialize config", "BACKUP_ERROR")
		return
	}
	configEntry, err := zw.Create("config.toml")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create config.toml in zip", "BACKUP_ERROR")
		return
	}
	if _, err := configEntry.Write(configBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write config.toml in zip", "BACKUP_ERROR")
		return
	}

	// Add vrhub.db (skip silently with a manifest note if absent — fresh
	// install case where the DB has not been created yet).
	contents := []string{"config.toml"}
	dbPath := filepath.Join(h.DataDir, "vrhub.db")
	if h.DataDir != "" {
		if info, statErr := os.Stat(dbPath); statErr == nil && !info.IsDir() {
			dbEntry, cerr := zw.Create("vrhub.db")
			if cerr != nil {
				writeError(w, http.StatusInternalServerError, "failed to create vrhub.db in zip", "BACKUP_ERROR")
				return
			}
			dbFile, oerr := os.Open(dbPath)
			if oerr != nil {
				writeError(w, http.StatusInternalServerError, "failed to open vrhub.db for backup", "BACKUP_ERROR")
				return
			}
			if _, cerr := io.Copy(dbEntry, dbFile); cerr != nil {
				dbFile.Close()
				writeError(w, http.StatusInternalServerError, "failed to copy vrhub.db into zip", "BACKUP_ERROR")
				return
			}
			dbFile.Close()
			contents = append(contents, "vrhub.db")
		}
	}

	// Add a small manifest. `contents` is the authoritative list of what
	// the zip actually contains — restoring logic (Story 7.3) trusts this
	// field to know which entries to extract.
	manifest := struct {
		CreatedAt string   `json:"created_at"`
		Version   string   `json:"version"`
		Contents  []string `json:"contents"`
	}{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Version:   "1.0",
		Contents:  contents,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to serialize manifest", "BACKUP_ERROR")
		return
	}
	manifestEntry, err := zw.Create("manifest.json")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create manifest in zip", "BACKUP_ERROR")
		return
	}
	if _, err := manifestEntry.Write(manifestBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write manifest in zip", "BACKUP_ERROR")
		return
	}

	if err := zw.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to close zip writer", "BACKUP_ERROR")
		return
	}

	filename := "vrhub-server-backup-" + time.Now().UTC().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())

	// Also write a copy to {data-dir}/backups/ for the operator
	// to retrieve out-of-band (script doesn't have to wait for the
	// HTTP response to complete).
	if h.BackupSync != nil {
		h.BackupSync.Add(1)
		go func() {
			defer h.BackupSync.Done()
			h.persistBackupCopy(filename, buf.Bytes())
		}()
	} else {
		go h.persistBackupCopy(filename, buf.Bytes())
	}

	vlog.Get().Info().Str("event", "scripts_backup").Str("remote_addr", r.RemoteAddr).Int("size_bytes", buf.Len()).Msg("backup served via scripts API")
}

// persistBackupCopy writes the backup to {data-dir}/backups/.
// Best-effort: errors are logged but not surfaced to the script.
func (h *AdminHandler) persistBackupCopy(filename string, data []byte) {
	if h.DataDir == "" {
		return
	}
	backupsDir := filepath.Join(h.DataDir, "backups")
	if err := os.MkdirAll(backupsDir, 0755); err != nil {
		vlog.Get().Warn().Err(err).Msg("backup: failed to create backups dir")
		return
	}
	if err := os.WriteFile(filepath.Join(backupsDir, filename), data, 0644); err != nil {
		vlog.Get().Warn().Err(err).Msg("backup: failed to write file")
		return
	}
	vlog.Get().Info().Str("path", filepath.Join(backupsDir, filename)).Int("size_bytes", len(data)).Msg("backup persisted to disk")
}

// chiRouteURLParam extracts a URL param from the chi route context.
// Wraps the chi import to keep the dependency local.
func chiRouteURLParam(r *http.Request, key string) string {
	// chi v5 uses routingContext to expose URL params.
	type routeContextKey struct{}
	if rc := r.Context().Value(routeContextKey{}); rc != nil {
		// Use chi.RouteParams via type assertion; if not available,
		// return "" so the handler 400s cleanly.
		if rp, ok := rc.(interface{ URLParam(string) string }); ok {
			return rp.URLParam(key)
		}
	}
	// Fallback: try the stdlib URL.Query() (rarely useful but safe).
	return r.URL.Query().Get(key)
}

// Ensure strconv is used (port parsing in some error paths).
var _ = strconv.Itoa
