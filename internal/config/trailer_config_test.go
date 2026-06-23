package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestLoad_TrailerDefaults verifies AC4: when no [trailer] section is present,
// Load defaults language to "en" and leaves the API key empty.
func TestLoad_TrailerDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[server]\n  host = \"127.0.0.1\"\n  port = 39457\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Trailer.Language != "en" {
		t.Errorf("Trailer.Language = %q, want default \"en\"", cfg.Trailer.Language)
	}
	if cfg.Trailer.YouTubeAPIKey != "" {
		t.Errorf("Trailer.YouTubeAPIKey = %q, want \"\"", cfg.Trailer.YouTubeAPIKey)
	}
}

// TestLoad_TrailerExplicit verifies the [trailer] section is parsed from TOML.
func TestLoad_TrailerExplicit(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	toml := `
[server]
  host = "127.0.0.1"
  port = 39457

[trailer]
  language = "fr"
  youtube_api_key = "secret-123"
`
	if err := os.WriteFile(configPath, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Trailer.Language != "fr" {
		t.Errorf("Trailer.Language = %q, want \"fr\"", cfg.Trailer.Language)
	}
	if cfg.Trailer.YouTubeAPIKey != "secret-123" {
		t.Errorf("Trailer.YouTubeAPIKey = %q, want \"secret-123\"", cfg.Trailer.YouTubeAPIKey)
	}
}

// TestSaveLoad_TrailerRoundTrip verifies AC4: a [trailer] config round-trips
// through Save → Load via config.toml.
func TestSaveLoad_TrailerRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &types.Config{}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 39457
	cfg.Database.Path = filepath.Join(tmpDir, "vrhub.db")
	cfg.Trailer.Language = "pt-BR"
	cfg.Trailer.YouTubeAPIKey = "round-trip-key"

	if err := Save(cfg, tmpDir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Trailer.Language != "pt-BR" {
		t.Errorf("round-trip Language = %q, want \"pt-BR\"", loaded.Trailer.Language)
	}
	if loaded.Trailer.YouTubeAPIKey != "round-trip-key" {
		t.Errorf("round-trip YouTubeAPIKey = %q, want \"round-trip-key\"", loaded.Trailer.YouTubeAPIKey)
	}
}
