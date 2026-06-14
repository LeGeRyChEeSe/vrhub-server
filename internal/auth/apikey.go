package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
)

// apiKeyBytes is the entropy size for a generated admin API key.
// 32 bytes = 256 bits, well above the 128-bit OWASP recommendation.
const apiKeyBytes = 32

// randReaderAPIKey is the io.Reader used to source entropy for API key
// generation. Same pattern as session.go's randReader (RWMutex-protected,
// swappable in tests via withRandReader).
func randReaderAPIKey() io.Reader {
	randReaderMu.RLock()
	defer randReaderMu.RUnlock()
	return randReader
}

// GenerateAPIKey generates a fresh admin API key. Returns:
//   - plaintext: 64-char hex string (caller shows this to the operator ONCE)
//   - hash: SHA-256 hex of plaintext (caller persists to AdminConfig.APIKeyHash)
//
// On crypto/rand failure, returns an error. The plaintext is generated
// via the shared randReader (so tests can inject a failing reader).
func GenerateAPIKey() (plaintext string, hash string, err error) {
	b := make([]byte, apiKeyBytes)
	if _, err := io.ReadFull(randReaderAPIKey(), b); err != nil {
		return "", "", fmt.Errorf("auth.apikey.generate: random source failed: %w", err)
	}
	plaintext = hex.EncodeToString(b)
	h := sha256.Sum256([]byte(plaintext))
	hash = hex.EncodeToString(h[:])
	return plaintext, hash, nil
}

// VerifyAPIKey checks a presented plaintext API key against a stored
// SHA-256 hash. Returns true on match. The comparison is constant-time
// to avoid timing oracles that could leak key material.
func VerifyAPIKey(presented, storedHash string) bool {
	if storedHash == "" {
		return false
	}
	h := sha256.Sum256([]byte(presented))
	computed := hex.EncodeToString(h[:])
	return subtleEqual(computed, storedHash)
}

// subtleEqual wraps subtle.ConstantTimeCompare with a length precheck.
// Returns false fast if lengths differ; otherwise constant-time compare.
func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
