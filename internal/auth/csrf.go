package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
