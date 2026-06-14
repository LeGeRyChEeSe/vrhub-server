package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestGenerateAPIKey_BasicProperties verifies the basic properties of a
// generated API key: 64-char hex plaintext, hash matches SHA-256 round-trip.
func TestGenerateAPIKey_BasicProperties(t *testing.T) {
	plaintext, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey returned error: %v", err)
	}
	if len(plaintext) != 64 {
		t.Errorf("plaintext length = %d, want 64", len(plaintext))
	}
	// Validate hex.
	for _, c := range plaintext {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("plaintext contains non-lowercase-hex char %q", c)
			break
		}
	}
	// Validate hash matches SHA-256(plaintext).
	h := sha256.Sum256([]byte(plaintext))
	want := hex.EncodeToString(h[:])
	if hash != want {
		t.Errorf("hash mismatch: got %s, want %s", hash, want)
	}
}

// TestGenerateAPIKey_Unique verifies two consecutive calls produce
// different keys (256 bits of entropy should not collide).
func TestGenerateAPIKey_Unique(t *testing.T) {
	p1, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("first GenerateAPIKey: %v", err)
	}
	p2, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("second GenerateAPIKey: %v", err)
	}
	if p1 == p2 {
		t.Error("two consecutive GenerateAPIKey calls returned the same key (no entropy?)")
	}
}

// TestVerifyAPIKey verifies the verify function correctly matches the
// plaintext against its hash and rejects mismatches.
func TestVerifyAPIKey(t *testing.T) {
	plaintext, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}

	if !VerifyAPIKey(plaintext, hash) {
		t.Error("VerifyAPIKey(plaintext, hash) = false, want true")
	}

	// Wrong plaintext rejected.
	if VerifyAPIKey(plaintext+"X", hash) {
		t.Error("VerifyAPIKey with mutated plaintext = true, want false")
	}

	// Empty stored hash rejected (defense against uninitialized config).
	if VerifyAPIKey(plaintext, "") {
		t.Error("VerifyAPIKey with empty stored hash = true, want false")
	}

	// Empty presented key rejected.
	if VerifyAPIKey("", hash) {
		t.Error("VerifyAPIKey with empty presented = true, want false")
	}
}
