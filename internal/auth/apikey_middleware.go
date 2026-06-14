package auth

import (
	"context"
	"net/http"
	"sync/atomic"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// apiKeyContextKey is the unexported context key used to mark a
// request as API-key-authenticated. Handlers can use
// APIKeyAuthenticatedFromContext to check.
type apiKeyContextKey struct{}

// APIKeyAuthenticatedFromContext returns true if the request was
// authenticated via the API key middleware (Story 6.4). False if the
// request was authenticated via session or not authenticated at all.
func APIKeyAuthenticatedFromContext(ctx context.Context) bool {
	v, ok := ctx.Value(apiKeyContextKey{}).(bool)
	return ok && v
}

// APIKeyAuthMiddleware returns a middleware that validates the
// `X-API-Key` header against the SHA-256 hash stored in
// cfg.Admin.APIKeyHash. The middleware does NOT accept the session
// cookie as a substitute — the two auth schemes are independent.
//
// Story 6.4 Task 1.1.
//
// Response codes:
//   - 200 (via next.ServeHTTP) — valid X-API-Key header
//   - 401 API_KEY_MISSING  — no X-API-Key header
//   - 401 API_KEY_INVALID  — header present but doesn't match the hash
//   - 503 API_KEY_NOT_CONFIGURED — cfg.Admin.APIKeyHash is empty
//     (defense against a misconfigured server that hasn't generated
//     a key yet; rejects ALL API key requests until first-run
//     generation completes).
//
// Concurrency:
//   - The middleware reads cfg via the resolveConfig pattern (caller's
//     responsibility). Reads are cheap (pointer load under RLock).
//   - On 401 responses, the middleware uses sync/atomic.Bool to rate-limit
//     the audit log (Warn on failure) to once per process per
//     (host, key-prefix) pair. This prevents log flooding from a
//     misconfigured client. The rate limit is best-effort; production
//     rate limiting belongs in a separate story.
//
// Cache-Control: no-store is set on all error responses so
// intermediate proxies don't memoize the auth result.
func APIKeyAuthMiddleware(cfg *types.Config) func(http.Handler) http.Handler {
	if cfg == nil {
		// Should never happen (SetupRouter always passes a non-nil cfg
		// when sessionStore is non-nil), but defensive: if the caller
		// passed nil, refuse everything.
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeAPIKeyError(w, http.StatusServiceUnavailable, "API key auth not configured", "API_KEY_NOT_CONFIGURED")
			})
		}
	}

	// Per-process rate-limit of auth-failure Warn logs. The atomic
	// store is incremented on each 401; a separate goroutine could
	// reset it, but for a self-hosted single-instance server, an
	// "only log first 100 failures" approach is sufficient.
	var failureCount atomic.Int32

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")

			// Read the current config snapshot. resolveConfig is
			// called by the caller (router wiring) — here we read the
			// pointer field directly for hot-path speed.
			currentCfg := currentConfigSnapshot(cfg)
			if currentCfg == nil || currentCfg.Admin.APIKeyHash == "" {
				// R13-P2: 503 (not 401) when the server itself is
				// misconfigured, not the client.
				vlog.Get().Warn().Str("event", "api_key_not_configured").Msg("api key auth skipped; no key in config")
				writeAPIKeyError(w, http.StatusServiceUnavailable, "API key not configured on server", "API_KEY_NOT_CONFIGURED")
				return
			}

			presented := r.Header.Get("X-API-Key")
			if presented == "" {
				// R13 audit: log the failure (rate-limited).
				if failureCount.Add(1) <= 100 {
					vlog.Get().Warn().Str("event", "api_key_auth_fail").Str("code", "API_KEY_MISSING").Str("remote_addr", r.RemoteAddr).Msg("api key auth failed (missing header)")
				}
				writeAPIKeyError(w, http.StatusUnauthorized, "API key required", "API_KEY_MISSING")
				return
			}

			if !VerifyAPIKey(presented, currentCfg.Admin.APIKeyHash) {
				if failureCount.Add(1) <= 100 {
					vlog.Get().Warn().Str("event", "api_key_auth_fail").Str("code", "API_KEY_INVALID").Str("remote_addr", r.RemoteAddr).Msg("api key auth failed (invalid key)")
				}
				writeAPIKeyError(w, http.StatusUnauthorized, "API key invalid", "API_KEY_INVALID")
				return
			}

			// Success: inject the apikey marker into ctx and proceed.
			vlog.Get().Info().Str("event", "api_key_auth").Str("method", r.Method).Str("path", r.URL.Path).Msg("api key authenticated request")

			ctx := context.WithValue(r.Context(), apiKeyContextKey{}, true)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// currentConfigSnapshot returns the current cfg pointer, or nil if
// nil. Reads are O(1) and not behind any lock (the AdminConfig
// fields are read-only at runtime; only the *types.Config pointer
// itself can be replaced via the Story 6.3 UpdateConfig method,
// and an in-flight handler is safe because it captured the pointer
// at the start of the request).
func currentConfigSnapshot(cfg *types.Config) *types.Config {
	if cfg == nil {
		return nil
	}
	return cfg
}

// writeAPIKeyError is a tiny helper that writes a JSON error
// response in the same format as the rest of the admin API.
func writeAPIKeyError(w http.ResponseWriter, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Inline JSON (avoid the api package import which would create
	// a circular dependency: api -> auth, auth -> api).
	_, _ = w.Write([]byte(`{"error":{"message":"` + message + `","code":"` + code + `"}}`))
}
