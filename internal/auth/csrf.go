package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// csrfPlaceholder is the development-time default. The server MUST NOT start
// with this value in production — LoadCSRFSecret validates this invariant.
const csrfPlaceholder = "vrhub-csrf-secret-override-me-in-production"

// CSRFSecret is the active HMAC key used to derive per-session CSRF tokens.
// It is set at startup by LoadCSRFSecret (loaded from config.toml or
// generated on first run).
var CSRFSecret []byte

// GenerateCSRFSecret creates a new random 32-byte CSRF secret and returns it
// as a hex-encoded string suitable for persisting in config.toml.
func GenerateCSRFSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate csrf secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// LoadCSRFSecret loads the CSRF secret from the config's hex-encoded secret
// string, validates it is not the development placeholder, and sets the
// package-level CSRFSecret variable.
//
// Returns the loaded []byte secret. Callers should use this at startup;
// if the secret is missing (first-run), the caller should generate one,
// persist it to config.toml, and call LoadCSRFSecret again.
func LoadCSRFSecret(secretHex string) error {
	if secretHex == "" {
		return fmt.Errorf("auth: csrf secret is empty — generate one and persist to config.toml")
	}
	if secretHex == csrfPlaceholder {
		return fmt.Errorf("auth: csrf secret is the development placeholder — generate a random secret and update config.toml")
	}
	b, err := hex.DecodeString(secretHex)
	if err != nil {
		return fmt.Errorf("auth: decode csrf secret hex: %w", err)
	}
	if len(b) != 32 {
		return fmt.Errorf("auth: csrf secret must be 32 bytes, got %d", len(b))
	}
	CSRFSecret = b
	return nil
}

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
