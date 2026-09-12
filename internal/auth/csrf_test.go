package auth

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestGenerateCSRFSecret(t *testing.T) {
	secret1, err := GenerateCSRFSecret()
	if err != nil {
		t.Fatalf("GenerateCSRFSecret() error = %v", err)
	}

	// Must be hex-encoded 32 bytes = 64 hex chars
	if len(secret1) != 64 {
		t.Errorf("GenerateCSRFSecret() length = %d, want 64", len(secret1))
	}
	for i, r := range secret1 {
		c := string(r)
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("GenerateCSRFSecret()[%d] = %q, expected hex char", i, c)
		}
	}

	// Two calls must produce different secrets (cryptographic randomness).
	secret2, err := GenerateCSRFSecret()
	if err != nil {
		t.Fatalf("GenerateCSRFSecret() second call error = %v", err)
	}
	if secret1 == secret2 {
		t.Error("GenerateCSRFSecret() produced identical secrets on two calls")
	}
}

func TestLoadCSRFSecret_Valid(t *testing.T) {
	// Generate a valid 32-byte secret.
	secret, err := GenerateCSRFSecret()
	if err != nil {
		t.Fatalf("GenerateCSRFSecret() error = %v", err)
	}

	// Save the original CSRFSecret so we can restore it.
	orig := CSRFSecret
	defer func() { CSRFSecret = orig }()

	err = LoadCSRFSecret(secret)
	if err != nil {
		t.Fatalf("LoadCSRFSecret() error = %v", err)
	}

	if len(CSRFSecret) != 32 {
		t.Errorf("LoadCSRFSecret() set CSRFSecret to %d bytes, want 32", len(CSRFSecret))
	}
}

func TestLoadCSRFSecret_Empty(t *testing.T) {
	err := LoadCSRFSecret("")
	if err == nil {
		t.Error("LoadCSRFSecret(\"\") returned nil error, want error")
	}
}

func TestLoadCSRFSecret_Placeholder(t *testing.T) {
	err := LoadCSRFSecret(csrfPlaceholder)
	if err == nil {
		t.Error("LoadCSRFSecret(csrfPlaceholder) returned nil error, want error")
	}
	if err != nil && !strings.Contains(err.Error(), "placeholder") {
		t.Errorf("LoadCSRFSecret(csrfPlaceholder) error = %v, want 'placeholder' in message", err)
	}
}

func TestLoadCSRFSecret_BadHex(t *testing.T) {
	err := LoadCSRFSecret("not-a-hex-value!!!")
	if err == nil {
		t.Error("LoadCSRFSecret(bad hex) returned nil error, want error")
	}
}

func TestLoadCSRFSecret_WrongLength(t *testing.T) {
	// Valid hex but only 16 bytes (32 hex chars).
	short := strings.Repeat("0", 32)
	err := LoadCSRFSecret(short)
	if err == nil {
		t.Error("LoadCSRFSecret(16-byte hex) returned nil error, want error")
	}

	// Valid hex but 64 bytes (128 hex chars).
	long := strings.Repeat("0", 128)
	err = LoadCSRFSecret(long)
	if err == nil {
		t.Error("LoadCSRFSecret(32-byte hex) returned nil error, want error")
	}
}

func TestCSRFTokenForSession_Deterministic(t *testing.T) {
	// Generate a known secret.
	secret, err := GenerateCSRFSecret()
	if err != nil {
		t.Fatalf("GenerateCSRFSecret() error = %v", err)
	}
	if err := LoadCSRFSecret(secret); err != nil {
		t.Fatalf("LoadCSRFSecret() error = %v", err)
	}

	token1 := CSRFTokenForSession("test-session-id")
	token2 := CSRFTokenForSession("test-session-id")

	if token1 != token2 {
		t.Errorf("CSRFTokenForSession() produced different tokens for same session: %q vs %q", token1, token2)
	}

	// Must be 64 hex chars (SHA-256 HMAC output).
	if len(token1) != 64 {
		t.Errorf("CSRFTokenForSession() token length = %d, want 64", len(token1))
	}

	// Different session IDs must produce different tokens.
	token3 := CSRFTokenForSession("different-session-id")
	if token1 == token3 {
		t.Error("CSRFTokenForSession() produced same token for different session IDs")
	}
}

func TestCSRFTokenForSession_OutputLength(t *testing.T) {
	secret, _ := GenerateCSRFSecret()
	if err := LoadCSRFSecret(secret); err != nil {
		t.Fatalf("LoadCSRFSecret() error = %v", err)
	}

	token := CSRFTokenForSession("any-session")

	// Verify it's valid hex.
	decoded, err := hex.DecodeString(token)
	if err != nil {
		t.Fatalf("CSRFTokenForSession() output is not valid hex: %v", err)
	}
	if len(decoded) != 32 {
		t.Errorf("CSRFTokenForSession() decoded length = %d, want 32", len(decoded))
	}
}
