package api

import (
	"context"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/update"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// UpdateState represents the current state of an update operation.
type UpdateState int32

const (
	UpdateStateIdle           UpdateState = 0 // No update in progress
	UpdateStateRunning        UpdateState = 1 // Download and apply is running
	UpdateStateFailed         UpdateState = 2 // Update failed, operator can retry
	UpdateStateRestartPending UpdateState = 3 // Binary staged; waiting for explicit restart
)

// String returns a human-readable representation of the update state.
func (s UpdateState) String() string {
	switch s {
	case UpdateStateIdle:
		return "idle"
	case UpdateStateRunning:
		return "running"
	case UpdateStateFailed:
		return "failed"
	case UpdateStateRestartPending:
		return "restart-pending"
	default:
		return "unknown"
	}
}

// changelogTTL is the minimum time between live GitHub release fetches.
// Prevents burning the 60 req/h unauthenticated rate limit during testing.
const changelogTTL = 5 * time.Minute

// UpdateHandler handles update-related API endpoints.
type UpdateHandler struct {
	UpdateConfig update.Config
	DataDir      string
	state        atomic.Int32 // UpdateState enum (atomic for lock-free state transitions)

	// applyCancel cancels the in-flight apply download goroutine.
	// Set when HandleUpdateApplyPOST spawns the goroutine, cleared
	// when it finishes. Called by HandleUpdateResetPOST to abort
	// a 500 MB download mid-stream.
	//
	// S-06: previously the apply goroutine used
	// context.Background() (line 175), making the 500 MB download
	// un-interruptible — HandleUpdateResetPOST could flip the
	// state atomic to Failed but the goroutine kept downloading.
	applyCancel atomic.Pointer[context.CancelFunc]

	// ShutdownFn is called before process restart to close the HTTP
	// listener so the replacement process can bind the same port.
	// On Windows, triggerRestart holds the port open for 2 seconds during
	// its liveness-check window; without this the child hits EADDRINUSE
	// and both parent and child die. Wired by the router from main.go's
	// liveRebinder after the http.Server is created.
	ShutdownFn func(context.Context) error

	// changelog cache — avoids hitting the GitHub API on every navigation.
	changelogMu     sync.Mutex
	changelogCache  []update.ReleaseInfo
	changelogExpiry time.Time
}

// NewUpdateHandler creates a new UpdateHandler.
func NewUpdateHandler(cfg update.Config, dataDir string) *UpdateHandler {
	h := &UpdateHandler{
		UpdateConfig: cfg,
		DataDir:      dataDir,
	}
	return h
}

// HandleUpdateStatusGET handles GET /admin/api/update/status.
// Returns the cached update check results from the global checker plus the current
// update state machine value. The state field is required for the JS polling logic
// in admin.js to detect transitions (running vs failed vs idle vs restart-pending).
func (h *UpdateHandler) HandleUpdateStatusGET(w http.ResponseWriter, r *http.Request) {
	checker := update.GetGlobalChecker()

	var available bool
	var currentVersion, latestVersion, releaseNotes string
	var restartPending bool

	currentVersion = update.CurrentVersion.String()
	if checker != nil {
		result := checker.GetResult()
		if result != nil {
			available = result.VersionAvailable
			latestVersion = result.LatestVersion
			releaseNotes = result.ReleaseNotes
			restartPending = result.RestartPending
		}
	}

	// If the handler's own state machine is restart-pending (manual apply path),
	// propagate that to the response even if the checker doesn't know yet.
	handlerState := UpdateState(h.state.Load())
	if handlerState == UpdateStateRestartPending {
		restartPending = true
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": map[string]interface{}{
			"available":      available,
			"currentVersion": currentVersion,
			"latestVersion":  latestVersion,
			"releaseNotes":   releaseNotes,
			"autoApply":      h.UpdateConfig.AutoApply,
			"autoRestart":    h.UpdateConfig.AutoRestart,
			"updateState":    handlerState.String(),
			"restartPending": restartPending,
		},
	})
}

// HandleUpdateApplyPOST handles POST /admin/api/update/apply.
// Triggers the update download and restart flow asynchronously.
//
// B1 contract (debt-triage-2026-06-06 C-02): every response from this
// handler MUST have a `data.updateState` field that exactly matches
// `h.state.Load()` at the moment the response is sent. The state is
// read fresh from the atomic right before writeJSON (see line ~148
// `currentState := UpdateState(h.state.Load())`) so that any race
// between the CAS and the body serialization is resolved by the
// authoritative source. Regression test: TestHandleUpdate_B1Contract.
func (h *UpdateHandler) HandleUpdateApplyPOST(w http.ResponseWriter, r *http.Request) {
	// Atomically transition from Idle to Running.
	if !h.state.CompareAndSwap(int32(UpdateStateIdle), int32(UpdateStateRunning)) {
		current := UpdateState(h.state.Load())
		var message string
		switch current {
		case UpdateStateFailed:
			message = "Previous update failed — reset before retrying"
		default:
			message = "Update already in progress"
		}
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"data": map[string]string{
				"message":     message,
				"updateState": current.String(),
			},
		})
		return
	}

	checker := update.GetGlobalChecker()
	var downloadURL, latestVersion string
	if checker != nil {
		result := checker.GetResult()
		if result != nil {
			downloadURL = result.DownloadURL
			// S-05: pass the latest version to the Applicator so
			// the binary it installs is the one that the operator
			// saw in /admin/api/update/status. Without this, the
			// manual apply path uses the latest download URL but
			// doesn't record which version that URL corresponds to
			// — the .zip staging file is named after Version, so
			// a missing Version leads to a malformed staging name
			// (or, worse, an attacker who swaps the latest
			// download URL via a compromised checker could trick
			// the manual apply into installing a different version
			// than the operator saw).
			latestVersion = result.LatestVersion
		}
	}

	// Validate that we have a download URL before starting the goroutine.
	// If validation fails after CAS, swap state back to Idle so operator can retry.
	if downloadURL == "" {
		// CAS back to Idle for fail-soft semantics; unconditional Store would
		// clobber a concurrent transition (e.g. a panic recovery in flight).
		h.state.CompareAndSwap(int32(UpdateStateRunning), int32(UpdateStateIdle))
		// Snapshot the state for the body so it can never diverge from the atomic.
		currentState := UpdateState(h.state.Load())
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"data": map[string]string{
				"message":     "No update available — no download URL found",
				"updateState": currentState.String(),
			},
		})
		return
	}

	// S-06: use a cancellable context (NOT context.Background) so
	// HandleUpdateResetPOST can abort the 500 MB download mid-stream.
	// The cancel func is stored atomically so the reset handler
	// can invoke it without a lock. The download still survives
	// the HTTP request lifecycle (we explicitly do NOT use
	// r.Context()) — only an explicit /reset call cancels it.
	applyCtx, applyCancel := context.WithCancel(context.Background())
	h.applyCancel.Store(&applyCancel)

	go func() {
		defer func() {
			// Clear the cancel func so a future /reset call doesn't
			// try to cancel a goroutine that already finished.
			var nilFunc context.CancelFunc
			h.applyCancel.Store(&nilFunc)
			if rec := recover(); rec != nil {
				// CAS back to Failed for fail-soft semantics; unconditional Store
				// would clobber a concurrent reset that already set Idle.
				log.Get().Error().
					Interface("panic", rec).
					Bytes("stack", debug.Stack()).
					Msg("update apply goroutine panicked")
				h.state.CompareAndSwap(int32(UpdateStateRunning), int32(UpdateStateFailed))
			}
		}()

		applicator := update.NewApplicator(update.ApplyConfig{
			DataDir:     h.DataDir,
			AutoApply:   h.UpdateConfig.AutoApply,
			AutoBackup:  true,
			DownloadURL: downloadURL,
			AutoRestart: h.UpdateConfig.AutoRestart,
			// S-05: pin the version the operator saw. Auto-apply
			// does this too (checker.go:288); the manual path
			// previously didn't, so the staged zip file had a
			// non-deterministic name and the operator couldn't
			// tell which version was being installed.
			Version: latestVersion,
		})

		ctx := applyCtx
		if err := applicator.DownloadAndApply(ctx); err != nil {
			if err == update.ErrRestartPending {
				// Binary staged; waiting for explicit restart from operator.
				h.state.CompareAndSwap(int32(UpdateStateRunning), int32(UpdateStateRestartPending))
			} else {
				log.Get().Error().Err(err).Msg("update apply failed")
				h.state.CompareAndSwap(int32(UpdateStateRunning), int32(UpdateStateFailed))
			}
		} else {
			// DownloadAndApply calls os.Exit(0) on success to restart the server.
			// If it returns normally (test path, future hot-update), transition to Idle
			// via CAS to avoid clobbering a concurrent reset.
			h.state.CompareAndSwap(int32(UpdateStateRunning), int32(UpdateStateIdle))
		}
	}()

	// B1 contract (code review 2026-06-07): read the atomic state at write
	// time instead of emitting a literal. The goroutine has been spawned
	// (line 175) and could have already CAS'd back to Idle via a concurrent
	// /reset call between then and now. Snapshotting the atomic right
	// before writeJSON guarantees body and atomic stay in sync.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": map[string]string{
			"message":     "Update started. Server will restart shortly.",
			"updateState": UpdateState(h.state.Load()).String(),
		},
	})
}

// HandleUpdateResetPOST handles POST /admin/api/update/reset.
// Resets the update state to Idle, allowing the operator to retry a failed or stuck update.
func (h *UpdateHandler) HandleUpdateResetPOST(w http.ResponseWriter, r *http.Request) {
	current := UpdateState(h.state.Load())

	switch current {
	case UpdateStateFailed:
		if !h.state.CompareAndSwap(int32(UpdateStateFailed), int32(UpdateStateIdle)) {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"data": map[string]string{
					"message":     "Reset failed — state changed during request",
					"updateState": UpdateState(h.state.Load()).String(),
				},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]string{
				"message":     "Update state reset. You can retry the update.",
				"updateState": UpdateStateIdle.String(),
			},
		})
	case UpdateStateRunning:
		// S-06: previously, the Running branch refused to reset
		// ("Cannot reset — update is running. Wait for completion
		// or restart the server process."). This was the
		// acknowledged-deferred R7-H7: a 500 MB download could not
		// be cancelled. Now: the reset handler cancels the
		// in-flight goroutine via the stored cancel func, then
		// transitions Running -> Failed (not Idle — the operator
		// triggered the reset, so the failed state is more
		// accurate for forensics).
		cancel := h.applyCancel.Load()
		if cancel != nil && *cancel != nil {
			(*cancel)()
		}
		// Race note: the goroutine, on seeing ctx.Done(), will
		// CAS Running -> Failed in its error path. Our reset
		// handler is the FIRST to CAS Running -> Failed, so the
		// goroutine's CAS will no-op (correct behavior).
		if !h.state.CompareAndSwap(int32(UpdateStateRunning), int32(UpdateStateFailed)) {
			// State changed under us; report current state.
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"data": map[string]string{
					"message":     "Reset failed — state changed during request",
					"updateState": UpdateState(h.state.Load()).String(),
				},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]string{
				"message":     "Update cancelled. State set to Failed; you can retry.",
				"updateState": UpdateStateFailed.String(),
			},
		})
	default:
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"data": map[string]string{
				"message":     "Reset not available — no failed or running update to retry.",
				"updateState": current.String(),
			},
		})
	}
}

// UpdateStatusFromConfig creates the update status response from config.
func UpdateStatusFromConfig(cfg *types.Config, checkerResult *update.CheckResult) map[string]interface{} {
	available := false
	latestVersion := ""
	autoApply := false

	if cfg != nil {
		autoApply = cfg.Update.AutoApply
	}

	if checkerResult != nil {
		available = checkerResult.VersionAvailable
		latestVersion = checkerResult.LatestVersion
	}

	return map[string]interface{}{
		"available":      available,
		"currentVersion": update.CurrentVersion.String(),
		"latestVersion":  latestVersion,
		"autoApply":      autoApply,
	}
}

// HandleUpdateRestartPOST handles POST /admin/api/update/restart.
// Triggers an immediate process restart by re-execing the current binary.
// Only accepted when the update state is restart-pending.
func (h *UpdateHandler) HandleUpdateRestartPOST(w http.ResponseWriter, r *http.Request) {
	current := UpdateState(h.state.Load())

	// Also accept if the checker reports restart-pending (auto-apply path).
	checkerPending := false
	if checker := update.GetGlobalChecker(); checker != nil {
		if res := checker.GetResult(); res != nil {
			checkerPending = res.RestartPending
		}
	}

	if current != UpdateStateRestartPending && !checkerPending {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"data": map[string]string{
				"message":     "No staged update — restart not available",
				"updateState": current.String(),
			},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": map[string]string{
			"message": "Restarting server now…",
		},
	})

	// Flush response before exiting.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Close the HTTP listener before spawning the replacement process.
	// On Windows, triggerRestart holds the port for up to 2 seconds in its
	// liveness-check window; if the child starts fast enough it hits
	// EADDRINUSE and both parent and child die. Shutting down the server
	// here frees the port before the child attempts to bind it.
	// The goroutine is killed by os.Exit inside TriggerRestart on success.
	if h.ShutdownFn != nil {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
		go func() {
			defer shutCancel()
			_ = h.ShutdownFn(shutCtx)
		}()
		// Brief pause so the listener has time to close before the child
		// process attempts to bind the same address.
		time.Sleep(100 * time.Millisecond)
	}

	if err := update.TriggerRestart(); err != nil {
		log.Get().Error().Err(err).Msg("update restart: TriggerRestart failed")
	}
}

// HandleChangelogGET handles GET /admin/api/update/changelog.
// Returns the last 10 GitHub releases. Results are cached for changelogTTL
// (5 min) to avoid burning the unauthenticated rate limit (60 req/h) during
// normal use. A forced refresh can be triggered by the checker on its own
// schedule; this endpoint only re-fetches when the cache is cold or expired.
func (h *UpdateHandler) HandleChangelogGET(w http.ResponseWriter, r *http.Request) {
	h.changelogMu.Lock()
	if time.Now().Before(h.changelogExpiry) && h.changelogCache != nil {
		releases := h.changelogCache
		h.changelogMu.Unlock()
		h.writeChangelogJSON(w, releases)
		return
	}
	h.changelogMu.Unlock()

	releases, err := update.FetchReleases(r.Context(), h.UpdateConfig)
	if err != nil {
		log.Get().Warn().Err(err).Msg("changelog: failed to fetch GitHub releases")
		// Return stale cache if available rather than an empty list.
		h.changelogMu.Lock()
		stale := h.changelogCache
		h.changelogMu.Unlock()
		if stale != nil {
			h.writeChangelogJSON(w, stale)
		} else {
			writeJSON(w, http.StatusOK, map[string]interface{}{"data": []interface{}{}})
		}
		return
	}

	h.changelogMu.Lock()
	h.changelogCache = releases
	h.changelogExpiry = time.Now().Add(changelogTTL)
	h.changelogMu.Unlock()

	h.writeChangelogJSON(w, releases)
}

func (h *UpdateHandler) writeChangelogJSON(w http.ResponseWriter, releases []update.ReleaseInfo) {
	items := make([]map[string]string, 0, len(releases))
	for _, rel := range releases {
		items = append(items, map[string]string{
			"tag":      rel.TagName,
			"version":  rel.Version,
			"body":     rel.Body,
			"html_url": rel.HTMLURL,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"data": items})
}
