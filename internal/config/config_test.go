package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

func TestLoadOrCreate_FirstRun_ReturnsNil(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := LoadOrCreate(tmpDir)
	if err != nil {
		t.Fatalf("expected no error for first-run, got: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil config for first-run, got non-nil")
	}
}

func TestLoadOrCreate_ExistingConfig_ReturnsConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	testConfig := `
[server]
  host = "0.0.0.0"
  port = 9090

[database]
  path = "/tmp/test.db"
`
	if err := os.WriteFile(configPath, []byte(testConfig), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadOrCreate(tmpDir)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config, got nil")
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected host 0.0.0.0, got %s", cfg.Server.Host)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
	if cfg.Database.Path != "/tmp/test.db" {
		t.Errorf("expected db path /tmp/test.db, got %s", cfg.Database.Path)
	}
}

func TestLoadOrCreate_DefaultDataDir(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := LoadOrCreate(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil for non-existent config")
	}
}

func TestConfig_IsSetup(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{
			Mode: types.ModeSetup,
		},
	}
	if !cfg.IsSetup() {
		t.Error("expected IsSetup() to return true for setup mode")
	}

	cfg.Server.Mode = types.ModeNormal
	if cfg.IsSetup() {
		t.Error("expected IsSetup() to return false for normal mode")
	}
}

func TestSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()

	originalCfg := &types.Config{
		Server: types.ServerConfig{
			Host: "192.168.1.1",
			Port: 7070,
		},
		DataDir: tmpDir,
	}

	if err := Save(originalCfg, tmpDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	loadedCfg, err := LoadOrCreate(tmpDir)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	if loadedCfg.Server.Host != "192.168.1.1" {
		t.Errorf("expected host 192.168.1.1, got %s", loadedCfg.Server.Host)
	}
	if loadedCfg.Server.Port != 7070 {
		t.Errorf("expected port 7070, got %d", loadedCfg.Server.Port)
	}
}

func TestGetDefaultDataDir(t *testing.T) {
	dir := GetDefaultDataDir()
	if dir == "" {
		t.Error("GetDefaultDataDir should not return empty string")
	}
}

func TestAdminConfig_TOML_ArchivePassword_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	originalCfg := &types.Config{
		Server: types.ServerConfig{
			Host: "127.0.0.1",
			Port: 9090,
			Mode: types.ModeNormal,
		},
		Database: types.DatabaseConfig{
			Path: filepath.Join(tmpDir, "vrhub.db"),
		},
		DataDir: tmpDir,
		Admin: types.AdminConfig{
			Username:        "admin",
			PasswordHash:    "$2a$10$testhash",
			ArchivePassword: "client-archive-pw-2026",
		},
	}

	if err := Save(originalCfg, tmpDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(tmpDir, "config.toml"))
	t.Logf("TOML:\n%s", string(data))

	loadedCfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("failed to load saved config: %v", err)
	}

	t.Logf("loaded ArchivePassword = %q", loadedCfg.Admin.ArchivePassword)
	if loadedCfg.Admin.ArchivePassword != "client-archive-pw-2026" {
		t.Errorf("expected archive_password 'client-archive-pw-2026', got %q", loadedCfg.Admin.ArchivePassword)
	}
}

func TestLoadConfig_EmptyArchivePassword_LeavesFieldEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Config without archive_password (simulates pre-9.8 install)
	testConfig := `
[server]
  host = "0.0.0.0"
  port = 8080

[admin]
  username = "admin"
  password_hash = "$2a$10$testhash"
`
	if err := os.WriteFile(configPath, []byte(testConfig), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadOrCreate(tmpDir)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config, got nil")
	}
	if cfg.Admin.ArchivePassword != "" {
		t.Errorf("expected empty archive_password for legacy config, got %q", cfg.Admin.ArchivePassword)
	}
}
