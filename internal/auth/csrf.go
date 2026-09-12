package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// CSRFSecret is a server-side secret used to derive per-session CSRF
// tokens. The secret is per-process (set at startup; could be loaded
// from a config file in a future hardening story). R13-P5: the CSRF
// token is HMAC(sess.ID, CSRFSecret), NOT sess.ID itself, so a leaked
// session cookie does not yield the CSRF token directly.
//
// In production this should be replaced with a per-deployment random
// secret loaded from a config file. The default is intentionally weak
// (all zeros) so development is easy; ops MUST override it for
// production deployments.
var CSRFSecret = []byte("vrhub-csrf-secret-override-me-in-production")

// CSRFTokenForSession derives a per-session CSRF token. The output is
// deterministic for a given (sessionID, CSRFSecret) pair, so the
// server can re-derive the expected token on every request without
// per-session storage.
//
// The output is 64 hex chars (32 bytes SHA-256 HMAC), well above the
// OWASP recommended 128-bit CSRF token entropy.
func CSRFTokenForSession(sessionID string) string {
	mac := hmac.New(sha256.New, CSRFSecret)
	mac.Write([]byte(sessionID))
	return hex.EncodeToString(mac.Sum(nil))
}

// CSRFMiddleware is a Chi-compatible middleware that enforces CSRF
// protection on state-changing routes. It reads the session from the
// request context (populated by SessionAuthMiddleware), derives the
// expected per-session CSRF token via HMAC-SHA256, and compares it
// against the X-CSRF-Token request header.
//
// For form-urlencoded requests the middleware also accepts the token
// from the _csrf_token form field (same logic as
// AdminHandler.validateCSRF in admin_settings.go).
//
// Returns 403 Forbidden with a JSON error body when the token is
// missing or invalid.
func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := SessionFromContext(r.Context())
		if !ok || sess == nil {
			// No session in context: fail closed.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"CSRF token invalid or missing","code":"CSRF_INVALID"}}`))
			return
		}

		expected := CSRFTokenForSession(sess.ID)

		// Try header first (works for both JSON and form requests).
		presented := r.Header.Get("X-CSRF-Token")
		if presented == "" {
			// Only call ParseForm for form-urlencoded requests —
			// JSON body would be consumed by ParseForm and break
			// the downstream json.NewDecoder read.
			ct := strings.ToLower(r.Header.Get("Content-Type"))
			if strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
				_ = r.ParseForm()
				presented = r.PostFormValue("_csrf_token")
			}
		}

		if presented == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"CSRF token invalid or missing","code":"CSRF_INVALID"}}`))
			return
		}

		if !constantTimeEq(expected, presented) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"CSRF token invalid or missing","code":"CSRF_INVALID"}}`))
			return
		}

		next.ServeHTTP(w, r)
	})
}

// constantTimeEq compares two strings in constant time to prevent
// timing attacks. Mirrors the pattern used in AdminHandler.validateCSRF.
func constantTimeEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
