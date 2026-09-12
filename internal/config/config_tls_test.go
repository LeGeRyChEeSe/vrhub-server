package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

func TestValidateTLSFiles(t *testing.T) {
	// Create a temp dir with a dummy cert file.
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(certPath, []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"), 0644); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyPath, []byte("-----BEGIN PRIVATE KEY-----\ntest\n-----END PRIVATE KEY-----\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		cert    string
		key     string
		wantErr bool
	}{
		{
			name:    "both empty — ok",
			cert:    "",
			key:     "",
			wantErr: false,
		},
		{
			name:    "both present and exist — ok",
			cert:    certPath,
			key:     keyPath,
			wantErr: false,
		},
		{
			name:    "cert set, key empty — ok (non-fatal, caller decides)",
			cert:    certPath,
			key:     "",
			wantErr: false,
		},
		{
			name:    "cert empty, key set — ok (non-fatal, caller decides)",
			cert:    "",
			key:     keyPath,
			wantErr: false,
		},
		{
			name:    "cert exists, key missing — error",
			cert:    certPath,
			key:     filepath.Join(dir, "nonexistent_key.pem"),
			wantErr: true,
		},
		{
			name:    "cert missing, key exists — error",
			cert:    filepath.Join(dir, "nonexistent_cert.pem"),
			key:     keyPath,
			wantErr: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateTLSFiles(c.cert, c.key)
			if c.wantErr && err == nil {
				t.Error("want error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Errorf("want nil, got error: %v", err)
			}
		})
	}
}

func TestLoad_TLSFieldsPersisted(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	os.WriteFile(certPath, []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"), 0644)
	os.WriteFile(keyPath, []byte("-----BEGIN PRIVATE KEY-----\ntest\n-----END PRIVATE KEY-----\n"), 0644)

	cfg := &types.Config{
		Server: types.ServerConfig{
			Host:        "127.0.0.1",
			Port:        8080,
			TLSCertFile: certPath,
			TLSCertKey:  keyPath,
		},
	}
	if err := WriteConfig(cfg, dir); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Server.TLSCertFile != certPath {
		t.Errorf("TLSCertFile = %q, want %q", loaded.Server.TLSCertFile, certPath)
	}
	if loaded.Server.TLSCertKey != keyPath {
		t.Errorf("TLSCertKey = %q, want %q", loaded.Server.TLSCertKey, keyPath)
	}
}

func TestLoad_TLSValidationDoesNotFatal(t *testing.T) {
	// TLS validation returns an error but should not be fatal —
	// the operator might add cert files later. The caller (main.go)
	// logs at Warn level and continues.
	dir := t.TempDir()
	cfg := &types.Config{
		Server: types.ServerConfig{
			Host:        "127.0.0.1",
			Port:        8080,
			TLSCertFile: "/nonexistent/cert.pem",
			TLSCertKey:  "/nonexistent/key.pem",
		},
	}
	if err := WriteConfig(cfg, dir); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("want error for missing TLS files, got nil")
	}
	// The error should be wrapped and non-fatal.
	if err != nil {
		t.Logf("TLS validation error (expected): %v", err)
	}
}
