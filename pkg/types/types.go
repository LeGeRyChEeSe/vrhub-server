package types

import "time"

// ServerMode defines the operational mode of the server.
type ServerMode string

const (
	ModeSetup  ServerMode = "setup"
	ModeNormal ServerMode = "normal"
)

// Config holds all server configuration loaded from config.toml.
type Config struct {
	DataDir string `toml:"data_dir"`
	// GameFolders is the list of directories scanned for VR games.
	// Populated by the setup wizard and editable in the admin settings.
	GameFolders []string       `toml:"game_folders"`
	Server      ServerConfig   `toml:"server"`
	Database    DatabaseConfig `toml:"database"`
	Metadata    MetadataConfig `toml:"metadata"`
	Update      UpdateConfig   `toml:"update"`
	Admin       AdminConfig    `toml:"admin"`
	Trailer     TrailerConfig  `toml:"trailer"`
}

// TrailerConfig holds settings for the streaming-trailer feature (Story 11.1 /
// 11.3).
type TrailerConfig struct {
	// Language is the language hint for trailer links (BCP-47 / ISO-639 code,
	// e.g. "en", "fr"). Default "en". Used as the "hl" parameter of the YouTube
	// search-link fallback built for every game. Surfaced in the admin settings
	// (Power mode) as a global dropdown.
	Language string `toml:"language"`
}

// AdminConfig holds admin credentials for the web UI.
type AdminConfig struct {
	Username     string `toml:"username"`
	PasswordHash string `toml:"password_hash"`
	// ArchivePassword is the cleartext password used to encrypt the meta.7z
	// archive with AES-256 (Story 9.8). It is stored in cleartext in TOML
	// because the VRHub client needs the original password to decrypt the
	// archive; hashing would make the archive unusable. The value is revealed
	// to the operator once at setup and can be viewed later in the admin UI.
	ArchivePassword string `toml:"archive_password"`
	// APIKeyHash is the SHA-256 hash of the admin API key. The plaintext is
	// never persisted to TOML; it is held in memory only after generation.
	// Story 6-3.
	APIKeyHash string `toml:"api_key_hash"`
	// APIKeyPlaintext is the in-memory plaintext of the API key. Populated
	// after GenerateAPIKey or first-run generation; never serialized to TOML.
	// Story 6-3.
	APIKeyPlaintext string `toml:"-"`
	// PasswordPlaintext is the in-memory plaintext of the admin password.
	// Populated only by a successful HandleAuthLoginPOST; never serialized
	// to TOML. Cleared by UpdateConfig swaps so a stale value cannot
	// survive a config reload. Read by HandleSettingsGET (JSON branch) so
	// the dashboard's "Configuration" widget can reveal the password to
	// the operator on demand (Story 9.6, decision 2026-06-10).
	//
	// SECURITY: exposing this field over the wire is a deliberate,
	// user-approved tradeoff (Story 9.6 Subtask 1.3). The endpoint is
	// session-authenticated and the response carries a Warn-level audit
	// log entry. Production deployments should run behind HTTPS; over
	// plaintext HTTP the password is observable on the wire.
	PasswordPlaintext string `toml:"-"`
}

// ServerConfig holds server-specific settings.
type ServerConfig struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
	Mode ServerMode
}

// DatabaseConfig holds database settings.
type DatabaseConfig struct {
	Path string `toml:"path"`
}

// MetadataConfig holds metadata source settings.
type MetadataConfig struct {
	URL             string        `toml:"url"`
	RefreshInterval time.Duration `toml:"refresh_interval"`
}

// UpdateConfig holds update checker settings.
type UpdateConfig struct {
	Enabled       bool          `toml:"enabled"`
	CheckInterval time.Duration `toml:"check-interval"`
	AutoApply     bool          `toml:"auto-apply"`
	AutoRestart   bool          `toml:"auto-restart"`
	GithubToken   string        `toml:"github-token"`
	Owner         string        `toml:"owner"`
	Repo          string        `toml:"repo"`
}

// GameEntry represents a game in the library.
type GameEntry struct {
	ID               int64     `json:"id" db:"game_id"`
	ReleaseName      string    `json:"release_name"`
	GameName         string    `json:"game_name"`
	PackageName      string    `json:"package_name"`
	VersionCode      int64     `json:"version_code"`
	SizeBytes        int64     `json:"size_bytes"`
	Description      string    `json:"description"`
	IconURL          string    `json:"icon_url"`
	ThumbnailURL     string    `json:"thumbnail_url"`
	LastUpdated      time.Time `json:"last_updated"`
	Popularity       int64     `json:"popularity"`
	Hash             string    `json:"hash"`
	Corrupted        bool      `json:"corrupted" db:"corrupted"`
	CorruptionReason string    `json:"corruption_reason,omitempty" db:"corruption_reason"`
	Exposed          bool      `json:"exposed" db:"exposed"`
	OBBSizeBytes     int64     `json:"obb_size_bytes" db:"obb_size_bytes"`
	OBBPath          string    `json:"obb_path" db:"obb_path"`
	// ApkPath is the absolute path to the APK file on disk.
	// Story 9.10: the scanner (game_folders walk) writes the actual
	// path it found the file at, and the file server uses it to serve
	// the file directly (no copy to dataDir/games/.../pkgName/).
	// Empty for legacy games that pre-date the field — the file
	// server falls back to the legacy dataDir/games/{hash}/{pkgName}/
	// layout in that case (the operator can migrate them via the
	// startup scan which backfills this column).
	ApkPath string `json:"apk_path,omitempty" db:"apk_path"`
	// TrailerURL is a streaming trailer link (a YouTube watch URL such as
	// https://www.youtube.com/watch?v=XXXXXXXXXXX). Story 11.1: the server
	// NEVER hosts the video bytes — it only resolves and exposes the URL so
	// the VRHub client can stream it. Resolved via a cascade (operator
	// override sidecar > oculusdb best-effort > YouTube Data API) and cached
	// in the games.trailer_url column. Empty when no trailer is known; the
	// meta.7z / listing channels emit nothing for such games.
	TrailerURL string `json:"trailer_url,omitempty" db:"trailer_url"`
}

// ReviewGameEntry represents a game entry in the review response.
type ReviewGameEntry struct {
	ID               int64  `json:"id"`
	ReleaseName      string `json:"release_name"`
	GameName         string `json:"game_name"`
	PackageName      string `json:"package_name"`
	VersionCode      int64  `json:"version_code"`
	SizeBytes        int64  `json:"size_bytes"`
	OBBSizeBytes     int64  `json:"obb_size_bytes"`
	Corrupted        bool   `json:"corrupted"`
	CorruptionReason string `json:"corruption_reason,omitempty"`
	Excluded         bool   `json:"excluded"`
}

// scanRequest represents the JSON body for the scan endpoint.
type scanRequest struct {
	Folder string `json:"folder"`
}

// OrphanOBBEntry represents an OBB file that could not be paired with any APK.
type OrphanOBBEntry struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size_bytes"`
}

// ScanResult is returned by the scan endpoint.
type ScanResult struct {
	FileCount      int              `json:"file_count"`
	TotalSizeBytes int64            `json:"total_size_bytes"`
	Games          []GameScanEntry  `json:"games"`
	OrphanOBBs     []OrphanOBBEntry `json:"orphan_obb_files,omitempty"`
}

// GameScanEntry represents a single game entry in scan results.
type GameScanEntry struct {
	ReleaseName      string `json:"release_name"`
	GameName         string `json:"game_name"`
	PackageName      string `json:"package_name"`
	VersionCode      int64  `json:"version_code"`
	SizeBytes        int64  `json:"size_bytes"`
	OBBSizeBytes     int64  `json:"obb_size_bytes"`
	Corrupted        bool   `json:"corrupted"`
	CorruptionReason string `json:"corruption_reason,omitempty"`
}

// ReviewResult represents the response for the review POST endpoint.
type ReviewResult struct {
	UpdatedCount int    `json:"updated_count"`
	Message      string `json:"message"`
}

// LaunchResult holds the response data for the launch endpoint.
type LaunchResult struct {
	BaseURI      string   `json:"base_uri"`
	Password     string   `json:"password"`
	Instructions []string `json:"instructions"`
}

// IsSetup returns true when the server is in setup mode (no config file exists).
func (c *Config) IsSetup() bool {
	return c.Server.Mode == ModeSetup
}
