package api

import (
	"net/http"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/network"
)

// HandleNetworkStatusGET returns the latest reachability snapshot
// of the two external services the server depends on (GitHub for
// update checks, MetaMetadata for game metadata enrichment).
//
// Story 7.6 T1:
//
//	GET /admin/api/network-status → 200 JSON
//	  {
//	    "data": {
//	      "github":    "ok" | "degraded" | "offline" | "unknown",
//	      "metadata":  "ok" | "degraded" | "offline" | "unknown",
//	      "checked_at": <unix seconds>,
//	      "all_ok":    bool
//	    }
//	  }
//
// Mode gating: NONE. This is a deliberate departure from the
// 7.4 (monitoring) and 7.5 (stats) pattern — reachability info
// is read-only, non-sensitive, and useful to the Michel mode user
// who needs to know if the network is down while trying to
// diagnose a "metadata won't refresh" complaint. See
// story 7.6 dev-notes § "Mode gating" for the full rationale.
//
// Auth: the route is mounted on `protectedRouter` so an
// unauthenticated client is bounced to /admin/login by
// SessionAuthMiddleware. The handler itself does not inspect
// the mode cookie (see above).
//
// Failure modes:
//   - NetworkChecker == nil  → 503 (defense-in-depth: a mis-
//     wired AdminHandler must not silently return all_ok=true).
//   - The check is in-flight → returns the LAST known snapshot
//     (no synchronous check here — the polling goroutine
//     updates state independently).
func (h *AdminHandler) HandleNetworkStatusGET(w http.ResponseWriter, r *http.Request) {
	if h.NetworkChecker == nil {
		// 503 NOT_CONFIGURED — distinct from the 200-with-unknown
		// case so a UI client can show "status unavailable" vs
		// "still checking". The body is the standard error shape
		// (matches writeError) but with a 503 status so automated
		// monitors can detect a misconfigured server.
		writeError(w, http.StatusServiceUnavailable,
			"network checker not configured", "NOT_CONFIGURED")
		return
	}

	snap := h.NetworkChecker.GetStatus()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": map[string]interface{}{
			"github":     string(snap.GitHub.Status),
			"metadata":   string(snap.MetaMeta.Status),
			"checked_at": snap.LastCheck.Unix(),
			"all_ok":     snap.AllOK(),
		},
	})
}

// Compile-time interface assertion: keep the dependency on
// internal/network minimal and obvious. If the package's
// NetworkStatus / Checker types ever drift, this line fails to
// build before the handler does.
var _ = (*network.Checker)(nil)
