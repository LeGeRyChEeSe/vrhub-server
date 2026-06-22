package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/log"
)

// Config holds update checker configuration.
type Config struct {
	Enabled       bool          `toml:"enabled"`
	CheckInterval time.Duration `toml:"check-interval"`
	AutoApply     bool          `toml:"auto-apply"`
	AutoRestart   bool          `toml:"auto-restart"`
	GithubToken   string        `toml:"github-token"`
	Owner         string        `toml:"owner"`
	Repo          string        `toml:"repo"`
}

// DefaultConfig returns the default update checker configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:       true,
		CheckInterval: 24 * time.Hour,
		AutoApply:     true,
		Owner:         "LeGeRyChEeSe",
		Repo:          "vrhub-server",
	}
}

const (
	githubAPIURL      = "https://api.github.com/repos/%s/%s/releases/latest"
	githubReleasesURL = "https://api.github.com/repos/%s/%s/releases"
	httpTimeout       = 10 * time.Second
)

// Checker manages GitHub releases checking.
type Checker struct {
	config       Config
	httpClient   *http.Client
	currentVer   Version
	lastResult   *CheckResult
	lastCheck    time.Time
	lastModified string // Last-Modified from last successful response
	lastETag     string // ETag from last successful response
	mu           sync.RWMutex
	stopCh       chan struct{}
	doneCh       chan struct{}
	// applicator is optional; when set and AutoApply is enabled, the checker
	// will automatically download and apply updates after detecting a new version.
	applicator *Applicator
	// apiBaseURL is a TEST-ONLY override for the GitHub releases API URL
	// template (must contain two %s verbs for owner and repo). When empty,
	// production uses the hard-coded githubAPIURL (host api.github.com).
	//
	// This is intentionally an unexported field with no config/TOML surface:
	// exposing an arbitrary releases endpoint to operators would let anyone
	// who can write the config point self-updates at a hostile host, defeating
	// the trustedGitHubHosts allowlist in apply.go. Tests set it (together with
	// httpClient) to drive the checker against an httptest server.
	apiBaseURL string
}

var (
	globalChecker *Checker
	checkerMu     sync.Once
)

// NewChecker creates a new update checker with the given config and current version.
func NewChecker(cfg Config, current Version) *Checker {
	if cfg.CheckInterval == 0 {
		cfg.CheckInterval = 24 * time.Hour
	}

	return &Checker{
		config:     cfg,
		httpClient: &http.Client{Timeout: httpTimeout},
		currentVer: current,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// Start begins periodic update checks.
// It runs the check immediately and then on the configured schedule.
func (c *Checker) Start(ctx context.Context) {
	logger := log.Get()
	logger.Info().
		Str("owner", c.config.Owner).
		Str("repo", c.config.Repo).
		Dur("interval", c.config.CheckInterval).
		Str("current_version", c.currentVer.String()).
		Msg("Update checker: starting")

	// Run initial check asynchronously - don't block startup
	go c.check(ctx)

	// Start periodic checks
	go c.runPeriodic(ctx)
}

// runPeriodic runs checks at the configured interval.
func (c *Checker) runPeriodic(ctx context.Context) {
	logger := log.Get()
	defer close(c.doneCh)
	ticker := time.NewTicker(c.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.check(ctx)
		case <-c.stopCh:
			logger.Info().Msg("Update checker: stopped")
			return
		case <-ctx.Done():
			logger.Info().Msg("Update checker: context cancelled")
			return
		}
	}
}

// Stop gracefully stops the periodic checker.
func (c *Checker) Stop() {
	logger := log.Get()
	close(c.stopCh)
	select {
	case <-c.doneCh:
	case <-time.After(5 * time.Second):
		logger.Warn().Msg("Update checker: stop timed out")
	}
}

// SetApplicator assigns an applicator that will be used for auto-apply when a
// new version is detected. Pass nil to disable auto-apply at runtime.
func (c *Checker) SetApplicator(a *Applicator) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.applicator = a
}

// getApplicator returns the current applicator under lock.
func (c *Checker) getApplicator() *Applicator {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.applicator
}

// check performs a single update check with conditional requests.
func (c *Checker) check(ctx context.Context) {
	logger := log.Get()

	c.mu.Lock()
	lastCheck := c.lastCheck
	lastModified := c.lastModified
	lastETag := c.lastETag
	c.mu.Unlock()

	// Don't check if we checked recently (1 minute cooldown)
	if !lastCheck.IsZero() && time.Since(lastCheck) < time.Minute {
		return
	}

	logger.Debug().Msg("Update checker: checking for new version")

	urlTmpl := githubAPIURL
	if c.apiBaseURL != "" {
		urlTmpl = c.apiBaseURL
	}
	url := fmt.Sprintf(urlTmpl, c.config.Owner, c.config.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logger.Warn().Err(err).Str("url", url).Msg("Update checker: failed to create request")
		return
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	if c.config.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.GithubToken)
	}

	// Conditional request: use server's Last-Modified and ETag from last response
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	if lastETag != "" {
		req.Header.Set("If-None-Match", lastETag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logger.Warn().Err(err).Str("url", url).Msg("Update checker: HTTP request failed")
		c.setResult(false, "", "")
		return
	}
	defer resp.Body.Close()

	// Store Last-Modified and ETag for next request regardless of status
	c.mu.Lock()
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		c.lastModified = lm
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		c.lastETag = etag
	}
	c.mu.Unlock()

	switch resp.StatusCode {
	case http.StatusNotModified:
		logger.Debug().Msg("Update checker: no new release (304 Not Modified)")
		c.mu.Lock()
		c.lastCheck = time.Now()
		c.mu.Unlock()
		// Update result to indicate we checked but no new version
		c.setResult(false, "", "")
		return
	case http.StatusOK:
		// Proceed to parse body
	default:
		logger.Warn().Int("status", resp.StatusCode).Str("url", url).Msg("Update checker: unexpected status code")
		c.setResult(false, "", "")
		c.mu.Lock()
		c.lastCheck = time.Now()
		c.mu.Unlock()
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Warn().Err(err).Msg("Update checker: failed to read response body")
		c.setResult(false, "", "")
		c.mu.Lock()
		c.lastCheck = time.Now()
		c.mu.Unlock()
		return
	}

	release, err := parseRelease(body)
	if err != nil {
		logger.Warn().Err(err).Msg("Update checker: failed to parse release JSON")
		c.setResult(false, "", "")
		c.mu.Lock()
		c.lastCheck = time.Now()
		c.mu.Unlock()
		return
	}

	latestVer, err := ParseVersion(release.TagName)
	if err != nil {
		logger.Warn().Err(err).Str("tag", release.TagName).Msg("Update checker: failed to parse version from tag")
		c.setResult(false, "", "")
		c.mu.Lock()
		c.lastCheck = time.Now()
		c.mu.Unlock()
		return
	}

	updateAvailable := latestVer.GreaterThan(c.currentVer)
	if updateAvailable {
		downloadURL := findAssetURL(release.Assets, runtime.GOOS, runtime.GOARCH)
		if downloadURL == "" {
			logger.Warn().
				Str("platform", runtime.GOOS).
				Str("arch", runtime.GOARCH).
				Msg("Update checker: no asset found for current platform")
		}
		logger.Info().
			Str("current", c.currentVer.String()).
			Str("latest", latestVer.String()).
			Str("download_url", downloadURL).
			Msg("Update checker: new version available")
		c.setResultWithNotes(true, latestVer.String(), downloadURL, release.Body, false)

		// Auto-apply: trigger download and restart if enabled.
		if c.config.AutoApply {
			app := c.getApplicator()
			if app != nil && downloadURL != "" {
				go func() {
					logger.Info().Str("version", latestVer.String()).Msg("Update checker: auto-applying update")
					applyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
					defer cancel()
					applyCfg := ApplyConfig{
						DataDir:     app.config.DataDir,
						AutoBackup:  true,
						MaxBackups:  app.config.MaxBackups,
						DownloadURL: downloadURL,
						Version:     latestVer.String(),
						AutoRestart: c.config.AutoRestart,
					}
					a := NewApplicator(applyCfg)
					if err := a.DownloadAndApply(applyCtx); err != nil {
						if err == ErrRestartPending {
							logger.Info().Str("version", latestVer.String()).Msg("Update checker: auto-apply staged, waiting for explicit restart")
							c.setResultWithNotes(true, latestVer.String(), downloadURL, release.Body, true)
						} else {
							logger.Error().Err(err).Str("version", latestVer.String()).Msg("Update checker: auto-apply failed")
						}
					} else {
						logger.Info().Str("version", latestVer.String()).Msg("Update checker: auto-apply succeeded, process restarted")
					}
				}()
			} else if app == nil {
				logger.Warn().Msg("Update checker: auto-apply enabled but no applicator configured")
			}
		}
	} else {
		logger.Debug().
			Str("current", c.currentVer.String()).
			Str("latest", latestVer.String()).
			Msg("Update checker: no update available")
		c.setResultWithNotes(false, latestVer.String(), "", "", false)
	}

	c.mu.Lock()
	c.lastCheck = time.Now()
	c.mu.Unlock()
}

// setResult updates the last check result atomically (no release notes).
func (c *Checker) setResult(available bool, version, downloadURL string) {
	c.setResultWithNotes(available, version, downloadURL, "", false)
}

// setResultWithNotes updates the last check result with release notes and restart state.
func (c *Checker) setResultWithNotes(available bool, version, downloadURL, notes string, restartPending bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastResult = &CheckResult{
		VersionAvailable: available,
		LatestVersion:    version,
		DownloadURL:      downloadURL,
		CheckedAt:        time.Now(),
		ReleaseNotes:     notes,
		RestartPending:   restartPending,
	}
}

// GetResult returns the last check result.
func (c *Checker) GetResult() *CheckResult {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastResult
}

// findAssetURL finds the download URL for the current platform.
func findAssetURL(assets []Asset, goos, goarch string) string {
	for _, asset := range assets {
		if strings.Contains(asset.Name, goos) && strings.Contains(asset.Name, goarch) {
			return asset.DownloadURL
		}
	}
	return ""
}

// GitHubRelease represents the GitHub API release response.
type GitHubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// parseRelease parses the GitHub API release JSON response.
func parseRelease(body []byte) (*ReleaseInfo, error) {
	var release GitHubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("failed to unmarshal release JSON: %w", err)
	}

	assets := make([]Asset, 0, len(release.Assets))
	for _, a := range release.Assets {
		assets = append(assets, Asset{
			Name:        a.Name,
			DownloadURL: a.BrowserDownloadURL,
		})
	}

	return &ReleaseInfo{
		TagName: release.TagName,
		Version: strings.TrimPrefix(release.TagName, "v"),
		HTMLURL: release.HTMLURL,
		Assets:  assets,
		Body:    release.Body,
	}, nil
}

// FetchReleases fetches the list of recent GitHub releases for the given owner/repo.
// Returns up to 10 releases with their tag, version, body, and HTML URL.
func FetchReleases(ctx context.Context, cfg Config) ([]ReleaseInfo, error) {
	urlTmpl := githubReleasesURL
	url := fmt.Sprintf(urlTmpl+"?per_page=10", cfg.Owner, cfg.Repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("FetchReleases: create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.GithubToken)
	}

	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("FetchReleases: HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("FetchReleases: unexpected status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("FetchReleases: read body: %w", err)
	}

	var ghReleases []GitHubRelease
	if err := json.Unmarshal(raw, &ghReleases); err != nil {
		return nil, fmt.Errorf("FetchReleases: unmarshal: %w", err)
	}

	releases := make([]ReleaseInfo, 0, len(ghReleases))
	for _, r := range ghReleases {
		releases = append(releases, ReleaseInfo{
			TagName: r.TagName,
			Version: strings.TrimPrefix(r.TagName, "v"),
			HTMLURL: r.HTMLURL,
			Body:    r.Body,
		})
	}
	return releases, nil
}

// ParseVersion parses a semantic version string (e.g., "v1.2.3" or "1.2.3").
func ParseVersion(tag string) (Version, error) {
	tag = strings.TrimPrefix(tag, "v")
	parts := strings.Split(tag, ".")
	if len(parts) < 3 {
		return Version{}, fmt.Errorf("invalid version format: %s", tag)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return Version{}, fmt.Errorf("invalid major version: %s", parts[0])
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return Version{}, fmt.Errorf("invalid minor version: %s", parts[1])
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		// Handle patch with pre-release suffix (e.g., "1.2.3-beta")
		patchPart := strings.Split(parts[2], "-")[0]
		patch, err = strconv.Atoi(patchPart)
		if err != nil {
			return Version{}, fmt.Errorf("invalid patch version: %s", parts[2])
		}
	}

	return Version{
		Major: major,
		Minor: minor,
		Patch: patch,
	}, nil
}

// GreaterThan returns true if v is greater than other.
func (v Version) GreaterThan(other Version) bool {
	if v.Major > other.Major {
		return true
	}
	if v.Major == other.Major && v.Minor > other.Minor {
		return true
	}
	if v.Major == other.Major && v.Minor == other.Minor && v.Patch > other.Patch {
		return true
	}
	return false
}

// String returns the string representation of the version.
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// CurrentVersion returns the currently running version of vrhub-server.
var CurrentVersion = Version{Major: 0, Minor: 1, Patch: 4}

// SetCurrentVersion sets the current version (typically from build ldflags).
func SetCurrentVersion(v Version) {
	CurrentVersion = v
}

// StartGlobalChecker starts the global update checker with default settings.
func StartGlobalChecker(ctx context.Context, cfg Config) {
	checkerMu.Do(func() {
		globalChecker = NewChecker(cfg, CurrentVersion)
		globalChecker.Start(ctx)
	})
}

// StopGlobalChecker stops the global update checker.
func StopGlobalChecker() {
	if globalChecker != nil {
		globalChecker.Stop()
	}
}

// SetGlobalChecker registers c as the package-level checker instance so that
// GetGlobalChecker() and the admin API can read its results. Must be called
// after Start(); safe to call exactly once (subsequent calls are no-ops via
// sync.Once). Used by main.go which creates the checker locally (to wire the
// applicator) and then registers it here.
func SetGlobalChecker(c *Checker) {
	checkerMu.Do(func() {
		globalChecker = c
	})
}

// GetGlobalChecker returns the global checker instance.
func GetGlobalChecker() *Checker {
	return globalChecker
}

// APIURL returns the GitHub API URL for a given owner and repo.
func APIURL(owner, repo string) string {
	return fmt.Sprintf(githubAPIURL, owner, repo)
}
