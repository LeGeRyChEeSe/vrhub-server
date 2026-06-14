package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
