package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/config"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
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

// EnsureAPIKey generates and persists an admin API key on cfg if one
// isn't already configured (cfg.Admin.APIKeyHash == ""). Returns the
// generated plaintext (empty if a key already existed or generation
// failed) and whether a new key was actually created.
//
// This is called from two places: main.go at process startup (the
// normal-boot path, cfg loaded from disk), and the setup wizard's
// HandleLaunchPOST (the live setup→normal transition, no restart).
// Without the second call site, a server that completes the wizard
// without a manual restart would have no API key at all — every
// /admin/api/scripts/* request would 503 with "API key authentication
// not yet configured" despite the server being fully operational.
func EnsureAPIKey(dataDir string, cfg *types.Config) (plaintext string, generated bool, err error) {
	if cfg.Admin.APIKeyHash != "" {
		return "", false, nil
	}

	plaintext, hash, err := GenerateAPIKey()
	if err != nil {
		return "", false, fmt.Errorf("failed to generate API key: %w", err)
	}

	cfg.Admin.APIKeyHash = hash
	cfg.Admin.APIKeyPlaintext = plaintext

	if err := config.WriteConfig(cfg, dataDir); err != nil {
		return "", false, fmt.Errorf("failed to persist API key hash: %w", err)
	}

	return plaintext, true, nil
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
