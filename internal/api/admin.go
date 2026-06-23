package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/config"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/game"
	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/monitor"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/network"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	chi "github.com/go-chi/chi/v5"
)

// maxAdminBodySize caps the size of JSON request bodies on admin endpoints
// that decode an inbound payload. 4 KiB is comfortably above the size of
// every admin payload (toggle, login, revalidate) while still bounding
// the per-request memory cost to defeat trivial DoS via huge bodies.
//
// C-13 enforcement: every handler that calls json.NewDecoder(...).Decode
// or json.Unmarshal on r.Body MUST wrap r.Body with
// http.MaxBytesReader(w, r.Body, maxAdminBodySize) first, or
// inline-wrap the limit into the json.NewDecoder call. The structural
// regression test TestAdminEndpoints_BodySizeLimit guards against
// future drift. The current handlers return http.StatusBadRequest
// (400) on overflow — a deliberate spec deviation from RFC 7231
// §6.5.11 which prescribes 413; changing the status would break
// TestHandleAuthLoginPOST_OversizedBody and the form-urlencoded
// counterpart from story 6.2 R7-CRITICAL-FORM-LEAK. Logged as a
// follow-up if the spec alignment is ever required.
const maxAdminBodySize = 4096

// AdminHandler serves admin API endpoints for game management and authentication.
type AdminHandler struct {
	DataDir      string
	Importer     game.GameImporter
	DB           *db.DB
	SessionStore *auth.SessionStore
	Config       *types.Config
	// configMu protects the Config field (R10-RESOLVECONFIG-RACE). The login
	// and logout handlers read/write the pointer via resolveConfig; without
	// the mutex, two concurrent logins on a fresh install (where h.Config was
	// nil at startup) would race on the assignment and one of them would
	// observe a half-initialised config pointer.
	//
	// R11-HIGH-1: RWMutex (was Mutex) so the fast path (h.Config already
	// populated) takes RLock and the slow path (disk reload) takes Lock. With
	// a plain Mutex, every concurrent login on a fresh install blocks behind
	// the disk I/O of the first loader, including requests from the hostGetter
	// closure that runs on every protected request.
	configMu sync.RWMutex

	// Reloader live-rebinds the HTTP listener when server.host or
	// server.port changes via the settings page. Story 6-3 Task 4.3.
	// nil in test wiring (no live rebind desired).
	Reloader Reloader

	// UpdateConfigPusher pushes new update-checker config when
	// update.enabled / update.auto-apply change. Story 6-3 Task 5.1.
	// nil in test wiring.
	UpdateConfigPusher UpdateConfigPusher

	// MonitorBus is the in-process event bus for the admin monitoring
	// dashboard (Story 7.4). nil in test wiring; tests can construct a
	// bus via monitor.NewEventBus() and inject it.
	MonitorBus *monitor.EventBus

	// NetworkChecker is the background reachability checker for
	// external services (Story 7.6). nil in test wiring and in
	// setup mode; the handler returns 503 NOT_CONFIGURED when nil
	// so a misconfigured server doesn't silently report all_ok=true.
	NetworkChecker *network.Checker

	// BackupSync, when non-nil, is incremented before the backup
	// goroutine starts and decremented when it finishes. Tests can
	// wait on it to avoid race conditions with t.TempDir() cleanup.
	BackupSync *sync.WaitGroup

	// adminHTMLFn returns the admin shell HTML bytes. Wired in
	// cmd/server/main.go via NewAdminHandler; defaults to a no-op
	// stub in tests. Story 6-3.
	adminHTMLFn func() []byte

	// LoginRateLimiter throttles brute-force attempts on the login
	// endpoint. nil in test wiring (tests can opt-in via direct field
	// assignment). Production wires a real limiter via
	// NewAdminHandlerWithLoginRateLimiter (cmd/server/main.go).
	//
	// S-01 security fix: previously the login endpoint had no
	// throttling at all (deferred across R1/R2/R4/R5 of story 6-2).
	// Default policy: 5 attempts per 60-second sliding window per
	// (IP, account) pair. See internal/auth/ratelimit.go.
	LoginRateLimiter *auth.RateLimiter

	// OnConfigUpdated is called by UpdateConfig after a successful disk
	// write and in-memory swap, allowing other handlers (e.g.
	// PublicAPIHandler) to refresh their own Config pointer without
	// polling. nil in test wiring.
	OnConfigUpdated func(*types.Config)

	// OnGameFoldersChanged is called by HandleSettingsPUT after a
	// successful save when the game_folders set actually changed. The
	// production wiring (cmd/server/main.go) restarts the file watcher
	// on the new folder set without a server restart (Story 3.5 AC3).
	// nil in test wiring and setup mode.
	OnGameFoldersChanged func([]string)
}

// NewAdminHandler creates a new AdminHandler with optional session store and config.
func NewAdminHandler(dataDir string, importer game.GameImporter, database *db.DB, sessionStore *auth.SessionStore, cfg *types.Config) *AdminHandler {
	return &AdminHandler{
		DataDir:      dataDir,
		Importer:     importer,
		DB:           database,
		SessionStore: sessionStore,
		Config:       cfg,
		// Reloader + UpdateConfigPusher wired up in main.go after
		// construction.
		// R13-P4: adminHTMLFn default to nil — main.go (or tests)
		// must wire it explicitly via SetAdminHTML. The handler
		// HandleSettingsGET falls back to a placeholder byte slice
		// when nil so unauthenticated probes don't get empty bodies.
	}
}

// SetAdminHTML wires the admin shell HTML provider. Called by
// main.go after construction so the handler can serve the real
// admin shell HTML. R13-P4.
func (h *AdminHandler) SetAdminHTML(fn func() []byte) {
	h.adminHTMLFn = fn
}

// UpdateConfig atomically writes newCfg to disk AND swaps h.Config
// to point to newCfg. The write lock is held for the entire
// read-modify-write-swap sequence to prevent concurrent-PUT lost
// updates (R13-P1 / R13-P12). Story 6-3 Task 4.2.
//
// Callers should construct newCfg from the current h.Config (via
// resolveConfig) before calling this, since the swap is unconditional.
func (h *AdminHandler) UpdateConfig(newCfg *types.Config) error {
	if newCfg == nil {
		return fmt.Errorf("admin.UpdateConfig: nil cfg")
	}
	h.configMu.Lock()
	defer h.configMu.Unlock()
	if err := config.WriteConfig(newCfg, h.DataDir); err != nil {
		return err
	}
	h.Config = newCfg
	if h.OnConfigUpdated != nil {
		h.OnConfigUpdated(newCfg)
	}
	return nil
}

// PostLoginRedirect is the redirect destination after successful login.
// Story 6-3 (admin-settings-page) reverts this from the 6-2 temporary
// "/admin/" to the spec-literal "/admin/dashboard" (the R11-DN-2 decision
// is now resolved: /admin/dashboard is a real protected route registered
// in router.go).
const PostLoginRedirect = "/admin/dashboard"

// corruptionStatusResponse is the JSON response for corruption status endpoint.
type corruptionStatusResponse struct {
	GameID           int64  `json:"game_id"`
	ReleaseName      string `json:"release_name"`
	Corrupted        bool   `json:"corrupted"`
	CorruptionReason string `json:"corruption_reason,omitempty"`
	FilePath         string `json:"file_path"`
}

// revalidateResponse is the JSON response for revalidation endpoint.
type revalidateResponse struct {
	GameID      int64  `json:"game_id"`
	ReleaseName string `json:"release_name"`
	Corrupted   bool   `json:"corrupted"`
	Message     string `json:"message,omitempty"`
}

// requireDB writes a 503 SERVICE_UNAVAILABLE response and returns false
// when the handler's *db.DB pointer is nil. Otherwise returns true.
//
// Story 9.2 (B2): every game-route handler (corruption status, revalidate,
// exposed toggle, rescan, list, delete) begins with this check. The
// router now always mounts the game routes (the previous `gameDB != nil`
// gate was removed so setup→normal-transition requests don't 404), but
// the DB pointer may still be nil at request time in three cases:
//  1. Setup mode (SetupModeMiddleware redirects to /admin/setup, so
//     this branch is unreachable in practice — defense in depth).
//  2. Setup wizard in progress, /admin/api/games/* hits a pre-HandleLaunchPOST
//     edge (e.g. an authenticated admin is testing the route in a
//     browser tab while the wizard is mid-flight).
//  3. Genuine operator misconfig: db.Open failed at startup AND the
//     B2 late-bind in the ConfigPropagator closure also failed.
//
// The 503 is preferable to a nil-deref panic: it tells the client to
// retry (the B2 propagator runs on launch) and surfaces the issue in
// the admin shell's network error UI.
func (h *AdminHandler) requireDB(w http.ResponseWriter) bool {
	if h.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "game database not initialized (setup wizard in progress or DB open failed)", "DB_NOT_READY")
		return false
	}
	return true
}

// HandleCorruptionStatusGET handles GET /admin/api/games/:releaseName/corruption-status.
func (h *AdminHandler) HandleCorruptionStatusGET(w http.ResponseWriter, r *http.Request) {
	// Story 9.2 (B2): the game route is always registered (the previous
	// `gameDB != nil` gate was removed), so a nil h.DB at request time
	// returns 503 instead of panicking. See requireDB doc for the three
	// cases that can produce a nil DB here.
	if !h.requireDB(w) {
		return
	}
	// Note: URL param is named releaseName but it contains the package name (API contract)
	packageName := chi.URLParam(r, "releaseName")
	if packageName == "" {
		writeError(w, http.StatusBadRequest, "release_name is required", "INVALID_PARAM")
		return
	}

	gameEntry, err := h.DB.GetGameByPackage(packageName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "game not found: "+packageName, "GAME_NOT_FOUND")
		} else {
			vlog.Get().Error().Err(err).Str("package", packageName).Msg("database error while looking up game")
			writeError(w, http.StatusInternalServerError, "internal server error", "DATABASE_ERROR")
		}
		return
	}

	filePath := h.findGameFilePath(gameEntry.PackageName)

	resp := corruptionStatusResponse{
		GameID:           gameEntry.ID,
		ReleaseName:      gameEntry.ReleaseName,
		Corrupted:        gameEntry.Corrupted,
		CorruptionReason: gameEntry.CorruptionReason,
		FilePath:         filePath,
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": resp,
	})
}

// HandleRevalidatePOST handles POST /admin/api/games/:releaseName/revalidate.
func (h *AdminHandler) HandleRevalidatePOST(w http.ResponseWriter, r *http.Request) {
	// Story 9.2 (B2): nil-DB guard (see requireDB).
	if !h.requireDB(w) {
		return
	}
	// Note: URL param is named releaseName but it contains the package name (API contract)
	packageName := chi.URLParam(r, "releaseName")
	if packageName == "" {
		writeError(w, http.StatusBadRequest, "release_name is required", "INVALID_PARAM")
		return
	}

	gameEntry, err := h.DB.GetGameByPackage(packageName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "game not found: "+packageName, "GAME_NOT_FOUND")
		} else {
			vlog.Get().Error().Err(err).Str("package", packageName).Msg("database error while looking up game for revalidation")
			writeError(w, http.StatusInternalServerError, "internal server error", "DATABASE_ERROR")
		}
		return
	}

	filePath := h.findGameFilePath(gameEntry.PackageName)
	if filePath == "" {
		// Fix #9 (Round 11): Mark game as unexposed if file is missing instead of just returning error
		vlog.Get().Warn().Str("package", gameEntry.PackageName).Msg("game file not found during manual revalidation, marking as unexposed")
		if updateErr := h.DB.UpdateGameExposed(gameEntry.PackageName, false); updateErr != nil {
			vlog.Get().Error().Err(updateErr).Str("package", gameEntry.PackageName).Msg("failed to mark game as unexposed")
			writeError(w, http.StatusInternalServerError, "failed to update game exposure status", "DATABASE_ERROR")
			return
		}
		writeError(w, http.StatusBadRequest, "game file not found for package, marked as unexposed", "FILE_NOT_FOUND")
		return
	}

	// Stat the file first to get modification time
	info, statErr := os.Stat(filePath)
	if statErr != nil {
		vlog.Get().Warn().Str("package", gameEntry.PackageName).Err(statErr).Msg("failed to stat game file during manual revalidation")
		writeError(w, http.StatusInternalServerError, "failed to read game file metadata", "FILE_ERROR")
		return
	}

	// Re-run validation
	apkResult := game.ValidateAPK(filePath)

	var corrupted bool
	var reason string
	var message string
	var exposed bool
	var gameName string = gameEntry.GameName
	var versionCode int64 = gameEntry.VersionCode
	var sizeBytes int64 = gameEntry.SizeBytes
	var obbSizeBytes int64 = gameEntry.OBBSizeBytes
	var lastUpdated int64 = info.ModTime().Unix()

	if apkResult.Corrupted {
		corrupted = true
		reason = apkResult.CorruptionReason
		message = "Re-validation complete. Game is corrupted: " + reason
		exposed = false
		vlog.Get().Warn().Str("package", gameEntry.PackageName).Str("reason", reason).Msg("manual revalidation detected corruption")
	} else {
		// APK is valid — extract metadata first to get correct version code for OBB matching
		// Fix #11 (Round 15): Use disk APK's version code (not obsolete DB version) for OBB matching
		meta, metaErr := game.ExtractAPKMetadata(filePath)
		versionCodeForMatching := gameEntry.VersionCode
		if metaErr == nil && meta.PackageName != "" {
			versionCodeForMatching = meta.VersionCode
		}

		// Check OBB files (Fix #8: lowercase for case-insensitive matching)
		dir := filepath.Dir(filePath)
		allFiles, scanErr := game.ScanDirectory(dir)
		var obbCorrupted bool
		var obbReason string
		var obbSize int64

		// Fix #4 (Round 11): If directory scan fails, preserve existing OBB corruption status instead of clearing it
		if scanErr != nil {
			vlog.Get().Warn().Err(scanErr).Str("dir", dir).Msg("failed to scan directory for OBB files during revalidation")
			if gameEntry.CorruptionReason != "" && strings.Contains(gameEntry.CorruptionReason, "OBB:") {
				obbReason = gameEntry.CorruptionReason
				if !strings.Contains(gameEntry.CorruptionReason, "non-standard") {
					obbCorrupted = true
				}
			}
		} else {
			for _, f := range allFiles {
				if !f.IsAPK && game.IsOBBFile(f.Name) {
					vc, pkgName, ok := game.ExtractOBBPackageName(strings.ToLower(f.Name))
					// Fix #4 (Round 10): Match OBB files by version code to avoid cross-version mismatches
					// Fix #10 (Round 15): Case-insensitive matching for package name
					if ok && pkgName == strings.ToLower(gameEntry.PackageName) && vc == int64(versionCodeForMatching) {
						obbSize += f.Size
						obbResult := game.ValidateOBB(f.Path)
						if obbResult.Corrupted {
							obbCorrupted = true
							obbReason = fmt.Sprintf("OBB: %s", obbResult.CorruptionReason)
						} else if obbResult.CorruptionReason != "" && !obbCorrupted {
							if obbReason == "" {
								obbReason = fmt.Sprintf("OBB: %s", obbResult.CorruptionReason)
							}
						}
					}
				}
			}
		}

		if obbCorrupted {
			corrupted = true
			reason = obbReason
			message = "Re-validation complete. OBB is corrupted: " + reason
			exposed = false
			vlog.Get().Warn().Str("package", gameEntry.PackageName).Str("reason", reason).Msg("manual revalidation detected OBB corruption")
		} else {
			corrupted = false
			if obbReason != "" && !obbCorrupted {
				reason = obbReason
			} else {
				reason = ""
			}
			message = "Re-validation complete. Game is valid."
			exposed = true

			if metaErr == nil && meta.PackageName != "" {
				gameName = meta.Label
				versionCode = meta.VersionCode
				sizeBytes = info.Size()
			}
			obbSizeBytes = obbSize
			vlog.Get().Info().Str("package", gameEntry.PackageName).Msg("manual revalidation passed")
		}
	}

	// Fix #7 (Round 15): Consolidate manual revalidation updates into a single atomic transaction
	// Fix #4 (Round 15): Panic-safe BeginTx with deferred Rollback
	tx, txErr := h.DB.BeginTx(r.Context(), nil)
	if txErr != nil {
		vlog.Get().Error().Str("package", gameEntry.PackageName).Err(txErr).Msg("failed to begin transaction for manual revalidation")
		writeError(w, http.StatusInternalServerError, "failed to begin transaction", "TRANSACTION_ERROR")
		return
	}
	defer tx.Rollback()

	// Fix #6 (Round 15): Update all columns atomically including exposed status
	updateQuery := `UPDATE games SET game_name = ?, version_code = ?, size_bytes = ?, obb_size_bytes = ?, corrupted = ?, corruption_reason = ?, exposed = ?, last_updated = ? WHERE package_name = ?`
	if _, execErr := tx.Exec(updateQuery, gameName, versionCode, sizeBytes, obbSizeBytes, corrupted, reason, exposed, lastUpdated, gameEntry.PackageName); execErr != nil {
		vlog.Get().Error().Str("package", gameEntry.PackageName).Err(execErr).Msg("failed to update game status during manual revalidation")
		writeError(w, http.StatusInternalServerError, "failed to update game data", "TRANSACTION_ERROR")
		return
	}

	// Commit the transaction
	if commitErr := tx.Commit(); commitErr != nil {
		vlog.Get().Error().Str("package", gameEntry.PackageName).Err(commitErr).Msg("failed to commit transaction for manual revalidation")
		writeError(w, http.StatusInternalServerError, "failed to commit transaction", "TRANSACTION_ERROR")
		return
	}

	// Update gameEntry fields for the API response
	gameEntry.GameName = gameName
	gameEntry.VersionCode = versionCode
	gameEntry.SizeBytes = sizeBytes
	gameEntry.OBBSizeBytes = obbSizeBytes
	gameEntry.Corrupted = corrupted
	gameEntry.CorruptionReason = reason
	gameEntry.Exposed = exposed

	resp := revalidateResponse{
		GameID:      gameEntry.ID,
		ReleaseName: gameEntry.ReleaseName,
		Corrupted:   corrupted,
		Message:     message,
	}

	// Fix #10: Single writeJSON call instead of duplicate
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": resp,
	})
}

// exposedToggleRequest is the JSON request body for the exposed toggle endpoint.
//
// C-15: Exposed is a *bool (not bool) so the handler can distinguish
// "explicitly false" from "field omitted from the payload". A bool
// zero value silently coerces to false, which would let a buggy
// client believe it had successfully unexposed a game when in fact
// it sent an empty body. The handler rejects nil with 400.
type exposedToggleRequest struct {
	Exposed *bool `json:"exposed"`
}

// exposedToggleResponse is the JSON response for the exposed toggle endpoint.
type exposedToggleResponse struct {
	GameID      int64  `json:"game_id"`
	ReleaseName string `json:"release_name"`
	PackageName string `json:"package_name"`
	Exposed     bool   `json:"exposed"`
	Message     string `json:"message"`
}

// HandleExposedTogglePATCH handles PATCH /admin/api/games/:releaseName/exposed.
func (h *AdminHandler) HandleExposedTogglePATCH(w http.ResponseWriter, r *http.Request) {
	// M-06 (review 2026-06-11): CSRF check was missing. Session cookie
	// alone was sufficient for an attacker page to toggle exposure on
	// any game. Match the pattern of HandleRescanPOST.
	if !h.validateCSRF(r) {
		writeError(w, http.StatusForbidden, "CSRF token invalid or missing", "CSRF_INVALID")
		return
	}

	// Story 9.2 (B2): nil-DB guard (see requireDB).
	if !h.requireDB(w) {
		return
	}
	packageName := chi.URLParam(r, "releaseName")
	if packageName == "" {
		writeError(w, http.StatusBadRequest, "release_name is required", "INVALID_PARAM")
		return
	}

	var req exposedToggleRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBodySize)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "INVALID_PARAM")
		return
	}

	// C-15: reject payloads that omit the "exposed" field. Without
	// this check, a buggy client sending {} would silently unexpose
	// the game (the bool zero-value would coerce to false).
	if req.Exposed == nil {
		writeError(w, http.StatusBadRequest, "missing required field: exposed", "INVALID_PARAM")
		return
	}
	exposed := *req.Exposed

	gameEntry, err := h.DB.GetGameByPackage(packageName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "game not found: "+packageName, "GAME_NOT_FOUND")
		} else {
			vlog.Get().Error().Err(err).Str("package", packageName).Msg("database error while looking up game for exposed toggle")
			writeError(w, http.StatusInternalServerError, "internal server error", "DATABASE_ERROR")
		}
		return
	}

	if err := h.DB.UpdateGameExposed(packageName, exposed); err != nil {
		if errors.Is(err, db.ErrGameNotFound) {
			writeError(w, http.StatusNotFound, "game not found: "+packageName, "GAME_NOT_FOUND")
		} else {
			vlog.Get().Error().Err(err).Str("package", packageName).Msg("failed to update game exposed status")
			writeError(w, http.StatusInternalServerError, "failed to update game exposure status", "DATABASE_ERROR")
		}
		return
	}

	// Re-fetch to verify the update succeeded and get current state (avoids race condition)
	gameEntry, err = h.DB.GetGameByPackage(packageName)
	if err != nil {
		vlog.Get().Error().Err(err).Str("package", packageName).Msg("failed to re-fetch game after exposed toggle")
		writeError(w, http.StatusInternalServerError, "failed to verify game exposure status", "DATABASE_ERROR")
		return
	}
	if gameEntry.Exposed != exposed {
		vlog.Get().Error().Str("package", packageName).Msg("exposed state mismatch after update")
		writeError(w, http.StatusInternalServerError, "exposed state mismatch", "DATABASE_ERROR")
		return
	}

	vlog.Get().Info().Str("package", packageName).Bool("exposed", exposed).Msg("game exposure status updated")

	resp := exposedToggleResponse{
		GameID:      gameEntry.ID,
		ReleaseName: gameEntry.ReleaseName,
		PackageName: gameEntry.PackageName,
		Exposed:     gameEntry.Exposed,
		Message:     "Game exposure status updated",
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": resp,
	})
}

// findGameFilePath searches for the APK file of a game in the data directory.
func (h *AdminHandler) findGameFilePath(packageName string) string {
	apkName := packageName + ".apk"
	// Fix #14 (Round 10): Try direct path first to avoid O(N) directory scan
	expectedPath := filepath.Join(h.DataDir, apkName)
	if _, err := os.Stat(expectedPath); err == nil {
		return expectedPath
	}

	files, err := game.ScanDirectory(h.DataDir)
	if err != nil {
		vlog.Get().Warn().Err(err).Str("dir", h.DataDir).Msg("failed to scan data directory for game file lookup")
		return ""
	}

	for _, f := range files {
		if f.IsAPK && strings.EqualFold(f.Name, apkName) {
			return f.Path
		}
	}

	return ""
}

// HandleRescanPOST handles POST /admin/api/games/rescan.
func (h *AdminHandler) HandleRescanPOST(w http.ResponseWriter, r *http.Request) {
	// M-06 (review 2026-06-11): CSRF check was missing. The SPA sent
	// X-CSRF-Token but the server silently ignored it. A cross-origin
	// page could trigger a rescan using only the session cookie.
	if !h.validateCSRF(r) {
		writeError(w, http.StatusForbidden, "CSRF token invalid or missing", "CSRF_INVALID")
		return
	}

	// Story 9.2 (B2): nil-Importer guard. Rescan relies on the game
	// manager (Importer), which is constructed from gameDB. In setup
	// mode gameDB is nil and the manager is not built — we surface a
	// 503 NOT_READY rather than letting ScanAndImport dereference a
	// nil importer.
	if h.Importer == nil {
		writeError(w, http.StatusServiceUnavailable, "game importer not initialized (setup wizard in progress or DB open failed)", "IMPORTER_NOT_READY")
		return
	}
	ctx := r.Context()

	// Load current config to discover the configured game folders.
	// If none are configured, fall back to DataDir for backward
	// compatibility with legacy setups that pre-date game_folders,
	// and persist DataDir to game_folders so the configuration page
	// reflects the effective scan location.
	usingFallback := false
	scanDirs := []string{h.DataDir}
	if currentCfg, ok := h.resolveConfig(); ok {
		if len(currentCfg.GameFolders) > 0 {
			scanDirs = currentCfg.GameFolders
		} else {
			usingFallback = true
		}
	}

	result, err := game.ScanAndImportMultiple(ctx, scanDirs, h.Importer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Rescan failed: "+err.Error(), "RESCAN_ERROR")
		return
	}

	// Persist the DataDir fallback to game_folders so subsequent rescans
	// and the configuration page show the effective scan location.
	if usingFallback {
		if currentCfg, ok := h.resolveConfig(); ok && len(currentCfg.GameFolders) == 0 {
			upgraded := *currentCfg
			upgraded.GameFolders = []string{h.DataDir}
			if upgradeErr := h.UpdateConfig(&upgraded); upgradeErr != nil {
				vlog.Get().Warn().Err(upgradeErr).Msg("rescan: failed to persist DataDir to game_folders")
			}
		}
	}

	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"files_scanned":    result.FilesScanned,
			"games_added":      result.GamesAdded,
			"games_removed":    result.GamesRemoved,
			"total_size_bytes": result.TotalSize,
		},
	}

	writeJSON(w, http.StatusOK, resp)
}

// gameListResponse is the JSON response for the games list endpoint.
type gameListResponse struct {
	Games []gameListItem `json:"games"`
	Count int            `json:"count"`
}

// gameListItem represents a single game in the list response.
type gameListItem struct {
	GameID           int64  `json:"game_id"`
	ReleaseName      string `json:"release_name"`
	GameName         string `json:"game_name"`
	PackageName      string `json:"package_name"`
	VersionCode      int64  `json:"version_code"`
	SizeBytes        int64  `json:"size_bytes"`
	SizeFormatted    string `json:"size_formatted"`
	OBBSizeBytes     int64  `json:"obb_size_bytes"`
	Corrupted        bool   `json:"corrupted"`
	CorruptionReason string `json:"corruption_reason,omitempty"`
	Exposed          bool   `json:"exposed"`
	Status           string `json:"status"`
	LastUpdated      string `json:"last_updated"`
}

// formatBytes converts a byte count to a human-readable string (e.g., "1.5 GB").
func formatBytes(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	// Determine precision based on size
	if bytes < unit*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/unit)
	} else if bytes < unit*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(bytes)/unit)
	} else if bytes < unit*1024*1024*1024 {
		return fmt.Sprintf("%.1f GB", float64(bytes)/unit)
	}
	return fmt.Sprintf("%.1f TB", float64(bytes)/(unit*unit*unit*unit))
}

// deriveGameStatus derives a human-readable status from corrupted and exposed fields.
func deriveGameStatus(corrupted, exposed bool) string {
	if corrupted {
		return "corrupted"
	}
	if !exposed {
		return "excluded"
	}
	return "ok"
}

// HandleGamesListGET handles GET /admin/api/games.
func (h *AdminHandler) HandleGamesListGET(w http.ResponseWriter, r *http.Request) {
	// Story 9.2 (B2): nil-DB guard (see requireDB).
	if !h.requireDB(w) {
		return
	}
	games, err := h.DB.ListGames(nil)
	if err != nil {
		vlog.Get().Error().Err(err).Msg("failed to list games")
		writeError(w, http.StatusInternalServerError, "internal server error", "DATABASE_ERROR")
		return
	}

	items := make([]gameListItem, 0, len(games))
	for _, g := range games {
		items = append(items, gameListItem{
			GameID:           g.ID,
			ReleaseName:      g.ReleaseName,
			GameName:         g.GameName,
			PackageName:      g.PackageName,
			VersionCode:      g.VersionCode,
			SizeBytes:        g.SizeBytes,
			SizeFormatted:    formatBytes(g.SizeBytes),
			OBBSizeBytes:     g.OBBSizeBytes,
			Corrupted:        g.Corrupted,
			CorruptionReason: g.CorruptionReason,
			Exposed:          g.Exposed,
			Status:           deriveGameStatus(g.Corrupted, g.Exposed),
			LastUpdated:      g.LastUpdated.Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": gameListResponse{
			Games: items,
			Count: len(items),
		},
	})
}

// HandleGameDeleteDELETE handles DELETE /admin/api/games/:releaseName.
func (h *AdminHandler) HandleGameDeleteDELETE(w http.ResponseWriter, r *http.Request) {
	// Story 9.2 (B2): nil-DB guard (see requireDB).
	if !h.requireDB(w) {
		return
	}
	packageName := chi.URLParam(r, "releaseName")
	if packageName == "" {
		writeError(w, http.StatusBadRequest, "release_name is required", "INVALID_PARAM")
		return
	}

	if err := h.DB.DeleteGame(packageName); err != nil {
		if errors.Is(err, db.ErrGameNotFound) {
			writeError(w, http.StatusNotFound, "game not found: "+packageName, "GAME_NOT_FOUND")
		} else {
			vlog.Get().Error().Err(err).Str("package", packageName).Msg("failed to delete game from database")
			writeError(w, http.StatusInternalServerError, "failed to delete game", "DATABASE_ERROR")
		}
		return
	}

	vlog.Get().Info().Str("package", packageName).Msg("game removed from database")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": map[string]string{
			"message":      "Game removed from database",
			"package_name": packageName,
		},
	})
}

// loginRequest represents the JSON body for the login endpoint.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginMaxUsernameBytes caps the username at 256 bytes — log-injection and
// memory-spike guard rather than a real bcrypt limit.
const loginMaxUsernameBytes = 256

// loginMaxPasswordBytes caps the password at 72 bytes to match bcrypt's actual
// silent-truncation point. Passing a longer password is rejected with a clear
// 400 rather than silently using only the first 72 bytes — the previous 256
// cap was theatre (bcrypt would ignore bytes 73-256 anyway).
const loginMaxPasswordBytes = 72

// HandleAuthLoginPOST handles POST /admin/api/auth/login with session-based authentication.
// Accept-header-driven response shape (subtask 3.5):
//   - No Accept header or Accept: text/html → 302 redirect to PostLoginRedirect
//   - Accept: application/json (with q > text/html) → 200 JSON with {data: {redirect: PostLoginRedirect}}
//
// The classification is delegated to auth.IsJSONRequest (proper media-type q-value negotiation).
//
// Content-type-driven request body parsing:
//   - application/json (default; admin.js path) — body decoded as {"username","password"}
//   - application/x-www-form-urlencoded (no-JS fallback; form's action/method attrs in ui.go:176)
//     — body parsed via r.PostFormValue (Go's stdlib handles URL-decoding and field caps)
//
// The form-urlencoded fallback exists so the login form's HTML defaults (action="/admin/api/auth/login"
// method="post") submit successfully even if admin.js fails to load. Without it the JSON decoder
// would 400 on the form-encoded body and the browser would render the raw error.
//
// Cache-Control: no-store is set on all responses (success and failure) so corporate
// caches/proxies don't memoize the login outcome — successful login responses contain
// a Set-Cookie that must not be served to a different user.
func (h *AdminHandler) HandleAuthLoginPOST(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	// S-01: rate-limit per-IP BEFORE doing any work. The check is
	// cheap (in-memory sliding window) and runs before body parsing
	// so an attacker cannot waste CPU/IO on a flood. The 429
	// response carries no body details (no "IP locked" vs "user
	// locked" distinction) to avoid leaking whether the username
	// exists.
	if h.LoginRateLimiter != nil {
		ipKey := "ip:" + clientRemoteAddr(r)
		if !h.LoginRateLimiter.Allow(ipKey) {
			vlog.Get().Warn().
				Str("remote_addr", clientRemoteAddr(r)).
				Str("user_agent", r.UserAgent()).
				Str("bucket", "ip").
				Msg("login rate-limited (IP bucket full)")
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "too many requests, slow down", "RATE_LIMITED")
			return
		}
	}

	// Limit request body size to 4 KiB to prevent memory-exhaustion DoS.
	// Wrap r.Body itself (not a separate variable) so both r.ParseForm() (form-urlencoded
	// path) and json.NewDecoder(r.Body) (default path) respect the limit. With the
	// previous separate-variable wrap, the form-urlencoded path bypassed the limit
	// because r.ParseForm() reads from r.Body, not from the separate reader.
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodySize)

	var req loginRequest
	ct := r.Header.Get("Content-Type")
	// R11-HIGH-3: case-insensitive Content-Type match. RFC 7231 §3.1.1.1
	// says media types are case-insensitive. Browsers mostly send lowercase
	// but some clients (curl with -H, certain proxies) capitalise. The
	// previous case-sensitive HasPrefix silently 400'd these with
	// "invalid request body".
	ctLower := strings.ToLower(ct)
	switch {
	case strings.HasPrefix(ctLower, "application/x-www-form-urlencoded"):
		// No-JS fallback: parse the form-encoded body. r.ParseForm handles URL-decoding,
		// trims trailing newlines, and is bounded by the MaxBytesReader wrap on r.Body.
		if err := r.ParseForm(); err != nil {
			if _, ok := err.(*http.MaxBytesError); ok {
				// MaxBytesReader does NOT auto-write 400; we must write the response
				// explicitly so the client sees a status (not the default 200 with
				// an empty body).
				vlog.Get().Warn().Err(err).Msg("login: form body too large")
				writeError(w, http.StatusBadRequest, "request body too large", "BODY_TOO_LARGE")
				return
			}
			vlog.Get().Warn().Err(err).Msg("login: invalid form body")
			writeError(w, http.StatusBadRequest, "invalid request body", "INVALID_INPUT")
			return
		}
		req.Username = r.PostFormValue("username")
		req.Password = r.PostFormValue("password")
	default:
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// MaxBytesReader does NOT auto-write 400; we must write the response
			// explicitly. Otherwise the client would see the default 200 with an
			// empty body and assume the login succeeded.
			if _, ok := err.(*http.MaxBytesError); ok {
				vlog.Get().Warn().Err(err).Msg("login: JSON body too large")
				writeError(w, http.StatusBadRequest, "request body too large", "BODY_TOO_LARGE")
				return
			}
			// Log the full decoder error server-side; return a constant message to the client
			// to avoid leaking *json.SyntaxError position / 4 KiB limit internals.
			vlog.Get().Warn().Err(err).Msg("login: invalid request body")
			writeError(w, http.StatusBadRequest, "invalid request body", "INVALID_INPUT")
			return
		}
	}

	// R10-PASSWORD-NO-TRIM (superseded by R11-CRITICAL-3): the trim was reverted.
	// The setup wizard does NOT trim the password when generating the bcrypt hash,
	// so any password containing trailing whitespace (e.g. created via a
	// copy/paste with a trailing newline) would permanently fail login. The
	// password is compared byte-for-byte by ValidatePassword, so a leading/trailing
	// whitespace mismatch is fatal. Trimming here would silently break every
	// setup-created password that had trailing whitespace.
	//
	// The username IS still trimmed because the username is used in log lines
	// (where trailing whitespace is just noise) and in the constant-time
	// comparison (where trimming is harmless).
	//
	// R12-P2 (security regression fix): the password length check now uses
	// BYTE length, not rune count. Bcrypt silently truncates at 72 BYTES,
	// not 72 runes. With the previous rune-based check, a 72-rune CJK
	// password (~216 bytes) would pass the cap then bcrypt would truncate
	// to ~24 runes, producing an auth-collision (any garbage in runes
	// 25-72 produces the same hash). The username keeps rune count
	// (R11-MEDIUM-1) for HTML maxlength parity; the password uses bytes
	// for security-boundary correctness.
	//
	// R12-P4: also check TrimSpace(req.Password) for the empty
	// classification (without modifying req.Password) so a whitespace-only
	// password surfaces "username and password are required" instead of
	// the misleading "Invalid credentials."
	//
	// R10-PASSWORD-LENGTH-LEAK: the user-facing error for an over-long password
	// is generic ("input too long") so the response does not reveal the bcrypt
	// 72-byte truncation point. The full detail is logged server-side at Warn.
	req.Username = strings.TrimSpace(req.Username)
	// R11-MEDIUM-1 (username): use rune count (Unicode code points) instead of
	// byte length. The HTML form's `maxlength` attribute counts code points.
	if utf8.RuneCountInString(req.Username) > loginMaxUsernameBytes {
		vlog.Get().Warn().Int("username_len", utf8.RuneCountInString(req.Username)).Int("max", loginMaxUsernameBytes).Msg("login: username too long")
		writeError(w, http.StatusBadRequest, "input too long", "INVALID_INPUT")
		return
	}
	// R12-P2 (password): use BYTE length (security boundary matches bcrypt).
	if len(req.Password) > loginMaxPasswordBytes {
		vlog.Get().Warn().Int("password_len", len(req.Password)).Int("max", loginMaxPasswordBytes).Msg("login: password too long")
		writeError(w, http.StatusBadRequest, "input too long", "INVALID_INPUT")
		return
	}
	// R12-P4: classify whitespace-only password as empty (for the error
	// message) without modifying req.Password (so the bcrypt comparison
	// remains byte-exact).
	if req.Username == "" || req.Password == "" || strings.TrimSpace(req.Password) == "" {
		writeError(w, http.StatusBadRequest, "username and password are required", "INVALID_INPUT")
		return
	}

	// Resolve the effective config. Source the cfg host once and pass it through so the
	// cookie attributes are consistent across login (here) and logout/middleware. r.Host
	// is attacker-controlled (Host header injection) and must NOT be used for the
	// Secure-flag decision.
	//
	// R7-CRITICAL-SESSION-INIT: reload config from disk when h.Config is nil or when the
	// stored config has no admin password hash. The setup wizard writes config.toml AFTER
	// the in-memory cfg pointer is captured at startup, so the first login attempt after
	// TransitionToNormal needs the disk-resident cfg to authenticate. After a successful
	// disk-load we cache the loaded cfg into h.Config so subsequent logins do not
	// re-read the file (and to keep the cfg pointer consistent with HandleAuthLogoutPOST
	// which reads h.Config.Server.Host for the cookie-clearing Set-Cookie).
	//
	// Disk-load failures (file not found, malformed TOML, etc.) collapse to 401
	// INVALID_CREDENTIALS to avoid leaking server state to unauthenticated probes.
	// The underlying error is logged server-side for operator forensics.
	cfg, ok := h.resolveConfig()
	if !ok {
		writeError(w, http.StatusUnauthorized, "Invalid credentials", "INVALID_CREDENTIALS")
		return
	}
	// As of Story 9.7 (B7), the configured server host is no longer needed
	// for the SetSessionCookie Secure-flag decision (the request transport
	// is the correct signal, not the configured host). The cfg variable is
	// still required for Authenticate below.

	// Authenticate using the config. Use a single error code to avoid leaking whether username exists.
	if !auth.Authenticate(cfg, req.Username, req.Password) {
		// S-09 audit log: include source IP, user-agent, and username.
		// Session ID prefix is not yet available (auth failed before session
		// creation). Forensic analysis can correlate this with subsequent
		// failed attempts from the same IP.
		vlog.Get().Info().
			Str("username", req.Username).
			Str("remote_addr", clientRemoteAddr(r)).
			Str("user_agent", r.UserAgent()).
			Bool("success", false).
			Msg("login attempt failed")
		writeError(w, http.StatusUnauthorized, "Invalid credentials", "INVALID_CREDENTIALS")
		return
	}

	// Create session.
	if h.SessionStore == nil {
		vlog.Get().Error().Msg("login failed: session store is nil (server misconfigured)")
		writeError(w, http.StatusInternalServerError, "internal server error", "SESSION_ERROR")
		return
	}
	session := h.SessionStore.Create(req.Username)
	if session == nil {
		vlog.Get().Error().Str("username", req.Username).Msg("login failed: session store returned nil")
		writeError(w, http.StatusInternalServerError, "internal server error", "SESSION_ERROR")
		return
	}

	// Set session cookie.
	//
	// Story 9.7 (B7): SetSessionCookie now derives the Secure flag from the
	// request's actual transport (r.TLS != nil or X-Forwarded-Proto: https)
	// rather than from the configured server host. Passing r lets the cookie
	// be Secure=false for plain HTTP requests from a non-loopback client
	// (e.g. http://192.168.50.3:39457 from a phone on the LAN), and Secure=true
	// for HTTPS requests regardless of the configured host.
	auth.SetSessionCookie(w, r, session.ID, session.ExpiresAt)

	// Story 9.6: cache the plaintext password in the in-memory
	// config so the dashboard widget can reveal it. The hash is what
	// gets persisted; the plaintext lives only in memory, only after
	// a successful login, and is cleared by any subsequent
	// UpdateConfig swap (which uses a value copy from h.Config — see
	// HandleSettingsPUT). This is the deliberate Option-1 user
	// decision documented 2026-06-10 in Story 9.6 Subtask 1.3: the
	// server-side tradeoff is "audit-logged plaintext exposure in
	// exchange for the operator being able to reveal the password
	// from the dashboard".
	h.configMu.Lock()
	if h.Config != nil {
		h.Config.Admin.PasswordPlaintext = req.Password
	}
	h.configMu.Unlock()

	// S-01: reset the per-IP rate-limit bucket on a successful
	// login so a legitimate user (whose IP might be on a shared
	// network) isn't penalized for the next login. We do NOT reset
	// the per-user bucket — that's already empty since the
	// successful login required a permitted attempt.
	if h.LoginRateLimiter != nil {
		h.LoginRateLimiter.Reset("ip:" + clientRemoteAddr(r))
	}

	// S-09 audit log: include source IP, user-agent, session ID prefix.
	// Full session ID is sensitive (it's a bearer token); only the first
	// 8 hex chars are logged for forensic correlation.
	vlog.Get().Info().
		Str("username", req.Username).
		Str("remote_addr", clientRemoteAddr(r)).
		Str("user_agent", r.UserAgent()).
		Str("session_id_prefix", sessionIDPrefix(session.ID)).
		Bool("success", true).
		Msg("login successful")

	// Accept-header-driven response shape: JSON clients get 200 with redirect URL, HTML clients get 303.
	//
	// S-08: 303 See Other (not 302 Found) per RFC 7231 §6.4.3. After a non-idempotent
	// POST (login changes server state by creating a session), 303 instructs the
	// browser to issue a GET on the redirect target. With 302, some legacy clients
	// would re-issue the POST on the redirect — re-running login on every redirect,
	// creating duplicate sessions.
	if auth.IsJSONRequest(r) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]string{
				"redirect": PostLoginRedirect,
			},
		})
	} else {
		http.Redirect(w, r, PostLoginRedirect, http.StatusSeeOther)
	}
}

// HandleAuthLogoutPOST handles POST /admin/api/auth/logout.
// Idempotent: logout of an unauthenticated request is still success (204 No Content).
//
// The cookie-clearing Set-Cookie MUST use the same Secure flag as the original
// Set-Cookie: if login was on HTTPS with Secure=true, the deletion MUST also be
// Secure=true or the browser will refuse the deletion. We use resolveConfig so
// the cfg host matches the value that was used for Set-Cookie during login.
func (h *AdminHandler) HandleAuthLogoutPOST(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	// S-02: CSRF protection on logout. The R6-CSRF-LOGOUT defer
	// was already resolved for /admin/api/admin/settings (R6),
	// /api-key/regenerate, and /api-key — but not for /logout
	// itself. Without this check, a cross-site page can issue
	// POST /logout and log the user out.
	//
	// The check is a no-op for unauthenticated requests (the
	// session-cookie read happens first; if no session, no
	// CSRF token, fail closed). Same protection level as the
	// other state-changing admin endpoints.
	if h.validateCSRF(r) == false && /* h is *AdminHandler; the helper is on the same type */ true {
		// distinguish anonymous from CSRF failure: an anonymous
		// request is not a CSRF attempt — it's an unauthenticated
		// request. Let it through to the idempotent 204 path.
		// (A real CSRF attempt on an authenticated session is
		// blocked because the attacker can't read the token.)
		//
		// The CSRF check is conditional on having a session. We
		// peek at the session cookie directly here.
		if _, hasSession := auth.ReadSessionCookie(r); hasSession {
			vlog.Get().Warn().
				Str("remote_addr", clientRemoteAddr(r)).
				Str("user_agent", r.UserAgent()).
				Msg("logout: CSRF token invalid or missing (likely CSRF attack)")
			writeError(w, http.StatusForbidden, "CSRF token invalid or missing", "CSRF_INVALID")
			return
		}
	}

	id, ok := auth.ReadSessionCookie(r)
	sessionIDPrefixLog := ""
	if id != "" {
		// S-09 audit log: log session ID prefix on logout for forensic
		// correlation. The session may already be nil but the ID is
		// enough to trace which session was active.
		sessionIDPrefixLog = sessionIDPrefix(id)
	}
	if ok && h.SessionStore != nil {
		h.SessionStore.Delete(id)
	}

	// S-09 audit log: logout emits an Info-level line. Previously, the
	// logout handler emitted NO log line at all — forensic analysis of
	// unexpected logout was impossible. The log includes source IP,
	// user-agent, and the (already-deleted) session ID prefix.
	if id != "" {
		vlog.Get().Info().
			Str("remote_addr", clientRemoteAddr(r)).
			Str("user_agent", r.UserAgent()).
			Str("session_id_prefix", sessionIDPrefixLog).
			Msg("logout")
	}

	// Trigger a config disk-read so the side effect of h.resolveConfig (used
	// during login to bootstrap h.Config from disk on first post-setup login)
	// is preserved. As of Story 9.7 (B7) the returned host is no longer
	// consumed: ClearSessionCookie derives the Secure flag from the request
	// transport (r.TLS != nil or X-Forwarded-Proto: https), not from the
	// configured host.
	_, _ = h.resolveConfig()

	// The cookie-clearing Set-Cookie MUST use the same Secure flag as the
	// original Set-Cookie. Story 9.7 (B7) achieves this by deriving Secure
	// from the request's actual transport in BOTH SetSessionCookie and
	// ClearSessionCookie — so we pass `r`.
	auth.ClearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// clientRemoteAddr returns the best-effort source IP for audit logging.
// r.RemoteAddr is the standard Go field, formatted as "IP:port" for
// IPv4 or "[IPv6]:port" for IPv6. When behind a reverse proxy, this
// would be the proxy's IP; trusting X-Forwarded-For is intentionally
// out of scope (it would require proxy-trust configuration to avoid
// header-injection attacks).
//
// S-09: forensic analysis of session hijack needs the source IP, so
// it's always logged. The port is dropped (operators don't need it).
func clientRemoteAddr(r *http.Request) string {
	addr := r.RemoteAddr
	// Strip the port suffix. net.SplitHostPort handles both IPv4 and IPv6.
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// sessionIDPrefix returns the first 8 hex chars of a session ID for
// safe-for-logs identification. The full ID is a bearer token and
// must never be written to logs (log access == session compromise).
//
// S-09: 8 hex chars = 32 bits of entropy, enough to correlate a log
// line with a session in the database without exposing the secret.
func sessionIDPrefix(id string) string {
	if len(id) < 8 {
		return id
	}
	return id[:8]
}

// resolveConfig returns the effective *types.Config for cookie/middleware decisions.
//
// Returns (cfg, true) when the config is available (either from h.Config or via a
// successful config.Load from disk) and (nil, false) when no config can be obtained
// (neither in-memory nor on disk, or the on-disk file is malformed, or the file
// is present but has no admin credentials yet — i.e., the setup wizard hasn't run).
//
// Side effect: when a config is loaded from disk and h.Config was nil or had no
// admin password hash, the loaded config is cached back into h.Config. This keeps
// the in-memory pointer consistent with the disk state for the remainder of the
// server lifetime and prevents subsequent logins/logouts from re-reading the file.
//
// R10-RESOLVECONFIG-RACE: the read of h.Config + write back to it is performed
// under h.configMu. Without the mutex, two concurrent logins on a fresh install
// (where h.Config was nil at startup) would race on the assignment: both read
// the same nil pointer, both call config.Load, both write back. The race
// detector flags the concurrent pointer write. With the mutex, only one
// loader hits disk; subsequent callers see the cached pointer.
//
// This helper is shared by HandleAuthLoginPOST and HandleAuthLogoutPOST so the
// cfg host used for Set-Cookie and Clear-Cookie is always the same. Previously
// logout used only h.Config (no disk fallback), so a setup→normal transition
// would emit a non-Secure cookie deletion over HTTPS, leaving the stale cookie
// in the browser.
func (h *AdminHandler) resolveConfig() (*types.Config, bool) {
	// R11-HIGH-1: RWMutex fast path. RLock is cheap and concurrent-friendly.
	// The previous plain Mutex forced every concurrent caller behind the disk
	// I/O of the first loader, including the hostGetter closure that runs on
	// every protected admin request.
	h.configMu.RLock()
	if h.Config != nil && h.Config.Admin.PasswordHash != "" {
		cfg := h.Config
		h.configMu.RUnlock()
		return cfg, true
	}
	h.configMu.RUnlock()

	// Slow path: needs disk reload. Acquire the write lock so only one goroutine
	// hits disk; concurrent callers wait on the lock and then hit the fast path.
	h.configMu.Lock()
	defer h.configMu.Unlock()

	// Re-check under the write lock — another goroutine may have just loaded.
	if h.Config != nil && h.Config.Admin.PasswordHash != "" {
		return h.Config, true
	}

	loaded, err := config.Load(h.DataDir)
	if err != nil {
		vlog.Get().Error().Err(err).Msg("resolveConfig: failed to load config from disk")
		return nil, false
	}
	if loaded.Admin.PasswordHash == "" {
		// config.toml exists but credentials not yet set — the setup wizard hasn't run.
		vlog.Get().Info().Msg("resolveConfig: config.toml has no admin credentials (setup incomplete)")
		return nil, false
	}
	// Cache for subsequent calls. Subsequent logins will use the cached pointer
	// and not re-read the file.
	if h.Config == nil {
		h.Config = loaded
	} else {
		// h.Config was non-nil but lacked a password hash (e.g., a partially
		// constructed test cfg). Replace its fields with the loaded values so
		// Server.Host and other settings match disk state.
		*h.Config = *loaded
	}
	return loaded, true
}
