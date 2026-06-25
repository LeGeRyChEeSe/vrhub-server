package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

const (
	defaultUnixDataDir    = "$HOME/.vrhub-server"
	defaultWindowsDataDir = "%APPDATA%/vrhub-server"
	defaultPort           = 39457
	defaultHost           = "127.0.0.1"
	configFileName        = "config.toml"
	// defaultTrailerLanguage is the relevanceLanguage default for the
	// YouTube trailer resolver (Story 11.1).
	defaultTrailerLanguage = "en"
)

// Load reads the config file from dataDir/config.toml.
// If the file does not exist, it returns a nil config to signal setup mode.
func Load(dataDir string) (*types.Config, error) {
	if dataDir == "" {
		dataDir = GetDefaultDataDir()
	}

	configPath := filepath.Join(dataDir, configFileName)

	// Check if config file exists — this is the first-run detection.
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// No config file found — first run detected.
		return nil, fmt.Errorf("config not found at %s: first-run mode", configPath)
	}

	cfg := &types.Config{
		Server: types.ServerConfig{
			Host: defaultHost,
			Port: defaultPort,
		},
		Database: types.DatabaseConfig{
			Path: filepath.Join(dataDir, "vrhub.db"),
		},
		DataDir: dataDir,
	}

	if err := decodeTOML(configPath, cfg); err != nil {
		return nil, fmt.Errorf("config loader: %w", err)
	}

	// Apply defaults for any missing values.
	if cfg.Server.Host == "" {
		cfg.Server.Host = defaultHost
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = defaultPort
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = filepath.Join(dataDir, "vrhub.db")
	}
	// Story 11.1/11.3: default the trailer language to "en" when the [trailer]
	// section is absent. Used as the "hl" hint of the YouTube search-link
	// fallback; an empty value would drop the hint.
	if cfg.Trailer.Language == "" {
		cfg.Trailer.Language = defaultTrailerLanguage
	}

	return cfg, nil
}

// LoadOrCreate returns a config if the file exists, or nil for first-run detection.
func LoadOrCreate(dataDir string) (*types.Config, error) {
	if dataDir == "" {
		dataDir = GetDefaultDataDir()
	}

	configPath := filepath.Join(dataDir, configFileName)

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, nil
	}

	return Load(dataDir)
}

// Save writes the config to dataDir/config.toml. Story 6-3 alias for
// WriteConfig (the latter is the canonical name going forward; Save
// preserved for backward compat with setup wizard call sites).
func Save(cfg *types.Config, dataDir string) error {
	return WriteConfig(cfg, dataDir)
}

// WriteConfig atomically writes cfg to {dataDir}/config.toml. The atomic
// write is implemented via a temp-file + fsync + rename pattern: the
// temp file uses a unique name (pid + nanos) to avoid concurrent-write
// races, and rename is atomic on POSIX (Windows: MoveFileEx with
// REPLACE_EXISTING). On crash mid-write, the original config.toml is
// preserved (the temp file is left behind but never atomically
// swapped into place). Story 6-3 Task 3.5.
func WriteConfig(cfg *types.Config, dataDir string) error {
	if dataDir == "" {
		dataDir = cfg.DataDir
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("config.WriteConfig: mkdir: %w", err)
	}

	configPath := filepath.Join(dataDir, configFileName)

	// Encode to a unique temp file in the same directory (rename atomic
	// requires same filesystem).
	tmp, err := os.CreateTemp(dataDir, ".config.toml.tmp.*")
	if err != nil {
		return fmt.Errorf("config.WriteConfig: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup of the temp file on any failure below.
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	encoder := toml.NewEncoder(tmp)
	if err := encoder.Encode(cfg); err != nil {
		tmp.Close()
		return fmt.Errorf("config.WriteConfig: encode: %w", err)
	}

	// fsync the file's data to disk before rename so the new contents
	// are durable across a crash. The parent dir is also fsynced on
	// Linux for full crash-safety; we skip that here because not all
	// filesystems support it (e.g. macOS HFS+) and the atomic rename
	// already provides the key invariant (no partial-write visibility).
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("config.WriteConfig: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config.WriteConfig: close: %w", err)
	}

	if err := os.Rename(tmpPath, configPath); err != nil {
		return fmt.Errorf("config.WriteConfig: rename: %w", err)
	}
	// Rename succeeded — clear the cleanup path so the temp file is
	// not deleted (it's been promoted to configPath).
	tmpPath = ""
	return nil
}

func decodeTOML(path string, cfg *types.Config) error {
	_, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return fmt.Errorf("decode TOML %s: %w", path, err)
	}
	return nil
}

// GetDefaultDataDir returns the OS-specific default data directory.
func GetDefaultDataDir() string {
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
		}
		return filepath.Join(appData, "vrhub-server")
	default:
		home := os.Getenv("HOME")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		if home == "" {
			home = "."
		}
		return filepath.Join(home, ".vrhub-server")
	}
}
