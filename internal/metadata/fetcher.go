package metadata

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/rs/zerolog"
)

const (
	defaultMetadataURL     = "https://github.com/threethan/MetaMetadata/archive/refs/heads/main.tar.gz"
	cacheDirName           = "metadata"
	maxRetries      = 3
	baseRetryDelay  = 1 * time.Second
	maxFileSize     = 500 * 1024 * 1024 // 500MB per file
	etagFile               = ".etag"
	lastModifiedFile       = ".last_modified"
	lastRefreshFile        = ".last_refresh"
	defaultRefreshInterval = 24 * time.Hour
	metaImageConcurrency   = 8
	metaImageTimeout       = 10 * time.Second
)

// metaCommonJSON is the structure of each file in MetaMetadata's data/common/ directory.
type metaCommonJSON struct {
	Icon   string `json:"icon"`
	Square string `json:"square"`
	Hero   string `json:"hero"`
}

// metaOculusDBJSON holds the fields we read from MetaMetadata's data/oculusdb/ files.
type metaOculusDBJSON struct {
	DisplayLongDescription string `json:"display_long_description"`
}

// Fetcher handles downloading and extracting metadata from a remote source.
type Fetcher struct {
	dataDir         string
	url             string
	httpClient      *http.Client
	logger          *zerolog.Logger
	mu              sync.Mutex
	refreshInterval time.Duration
	done            chan struct{}
	shutdown        atomic.Bool
	waitCh          chan struct{}
	stopped         atomic.Bool
	shutdownCtx     context.Context
	shutdownCancel  context.CancelFunc
	packageSource   func() []string
	packageSourceMu sync.RWMutex
}

// NewFetcher creates a new Fetcher with the given data directory, optional URL override, and refresh interval.
func NewFetcher(dataDir string, url string, refreshInterval time.Duration) *Fetcher {
	if url == "" {
		url = defaultMetadataURL
	}
	if refreshInterval <= 0 {
		refreshInterval = defaultRefreshInterval
	}

	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())

	return &Fetcher{
		dataDir: dataDir,
		url:     url,
		// No Timeout on the client: the tarball can be large (~30 MB+) and
		// a per-request deadline would fire before the body is fully read.
		// The caller's context (10-minute startup timeout or shutdown ctx)
		// is the sole guard against hung connections.
		httpClient: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives:     true,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
		logger:          vlog.Get(),
		refreshInterval: refreshInterval,
		done:            make(chan struct{}),
		waitCh:          make(chan struct{}),
		shutdownCtx:     shutdownCtx,
		shutdownCancel:  shutdownCancel,
	}
}

// SetPackageSource registers a callback that returns the current list of package
// names to enrich with images after each successful metadata download. Only
// packages returned by fn will have their icons/thumbnails downloaded.
// If fn is nil (the default), no image downloads are performed.
func (f *Fetcher) SetPackageSource(fn func() []string) {
	f.packageSourceMu.Lock()
	f.packageSource = fn
	f.packageSourceMu.Unlock()
}

// Fetch downloads and extracts the metadata tarball, then downloads game images
// from the MetaMetadata CDN URLs — but only for the packages returned by the
// registered PackageSource callback (see SetPackageSource).
//
// It supports conditional requests (ETag/Last-Modified) to avoid unnecessary downloads.
// On network failure or other errors, it logs a warning but does not panic — graceful degradation.
func (f *Fetcher) Fetch(ctx context.Context) error {
	cacheDir := filepath.Join(f.dataDir, cacheDirName)
	_, err := f.fetchAndExtract(ctx, cacheDir)
	if err != nil {
		return err
	}
	// Always enrich packages regardless of whether the tarball changed.
	// Notes generation is pure file I/O (no network); image downloads are
	// skipped when the file already exists. This ensures that games added
	// after the last tarball refresh still get their metadata enriched.
	f.packageSourceMu.RLock()
	fn := f.packageSource
	f.packageSourceMu.RUnlock()
	var packages []string
	if fn != nil {
		packages = fn()
	}
	f.enrichPackages(ctx, cacheDir, packages)
	return nil
}

// EnrichGames downloads icons and thumbnails from the MetaMetadata CDN for the
// given package names, using the local cache. Call Fetch first to ensure the
// cache is up-to-date.
func (f *Fetcher) EnrichGames(ctx context.Context, packageNames []string) {
	cacheDir := filepath.Join(f.dataDir, cacheDirName)
	f.enrichPackages(ctx, cacheDir, packageNames)
}

// fetchAndExtract holds the mutex and performs the download + tarball extraction.
// Returns true when a new tarball was downloaded (caller should run processMetadataJSONs).
func (f *Fetcher) fetchAndExtract(ctx context.Context, cacheDir string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Ensure metadata directory exists.
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return false, fmt.Errorf("metadata fetcher: create cache dir: %w", err)
	}

	// Build request with conditional headers if available.
	req, err := f.newRequest(ctx)
	if err != nil {
		f.logger.Warn().Err(err).Msg("Failed to prepare metadata request, fetching fresh")
		req, _ = http.NewRequestWithContext(ctx, "GET", f.url, nil)
	}

	resp, err := f.doWithRetry(req)
	if err != nil {
		return false, fmt.Errorf("metadata fetcher: %w", err)
	}
	defer resp.Body.Close()

	// Handle 304 Not Modified — cache is up to date.
	if resp.StatusCode == http.StatusNotModified {
		f.logger.Debug().Str("url", f.url).Msg("Metadata cache is up to date (304)")
		return false, nil // caller still runs enrichPackages for new packages
	}

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("metadata fetcher: unexpected status %d from %s", resp.StatusCode, f.url)
	}

	// Extract tarball to metadata directory.
	if err := f.extractTarball(ctx, resp.Body, cacheDir); err != nil {
		return false, fmt.Errorf("metadata fetcher: extract: %w", err)
	}

	// Save ETag and Last-Modified for future conditional requests.
	f.saveCacheHeaders(resp)

	// Save last refresh timestamp after successful fetch.
	if err := f.saveLastRefreshTime(); err != nil {
		f.logger.Warn().Err(err).Msg("Failed to save last refresh timestamp")
	}

	f.logger.Debug().Str("url", f.url).Msg("Metadata cache updated (cache miss)")
	return true, nil
}

// findCommonDataDir locates the MetaMetadata data/common directory inside cacheDir.
// GitHub archives wrap content in a top-level directory (e.g. MetaMetadata-main/),
// so this function probes both directly at cacheDir and one level deep.
func findCommonDataDir(cacheDir string) string {
	if info, err := os.Stat(filepath.Join(cacheDir, "data", "common")); err == nil && info.IsDir() {
		return cacheDir
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		nested := filepath.Join(cacheDir, e.Name(), "data", "common")
		if info, err := os.Stat(nested); err == nil && info.IsDir() {
			return filepath.Join(cacheDir, e.Name())
		}
	}
	return ""
}

// enrichPackages downloads icons and thumbnails from the MetaMetadata CDN for
// the given package names. If packageNames is nil or empty, the function is a
// no-op (no images are downloaded). Pass the packages returned by the
// PackageSource callback to restrict downloads to games the operator has imported.
//
// Files are written to {cacheDir}/icons/{packageName}.png and
// {cacheDir}/thumbnails/{packageName}.jpg.
// Failures are logged at Debug level and do not abort the process.
func (f *Fetcher) enrichPackages(ctx context.Context, cacheDir string, packageNames []string) {
	if len(packageNames) == 0 {
		return
	}

	base := findCommonDataDir(cacheDir)
	if base == "" {
		f.logger.Debug().Str("cache_dir", cacheDir).Msg("metadata: MetaMetadata common dir not found, skipping image download")
		return
	}

	// Build a lookup set that covers both "pkg.json" and ".pkg.json" naming
	// conventions used by MetaMetadata.
	pkgSet := make(map[string]bool, len(packageNames)*2)
	for _, p := range packageNames {
		pkgSet[p] = true
		pkgSet["."+p] = true
	}

	commonDir := filepath.Join(base, "data", "common")
	entries, err := os.ReadDir(commonDir)
	if err != nil {
		f.logger.Warn().Err(err).Str("dir", commonDir).Msg("metadata: failed to read common dir")
		return
	}

	iconsDir := filepath.Join(cacheDir, "icons")
	thumbsDir := filepath.Join(cacheDir, "thumbnails")
	if err := os.MkdirAll(iconsDir, 0755); err != nil {
		f.logger.Warn().Err(err).Msg("metadata: failed to create icons dir")
		return
	}
	if err := os.MkdirAll(thumbsDir, 0755); err != nil {
		f.logger.Warn().Err(err).Msg("metadata: failed to create thumbnails dir")
		return
	}

	imgClient := &http.Client{Timeout: metaImageTimeout}
	sem := make(chan struct{}, metaImageConcurrency)

	var wg sync.WaitGroup
	var downloaded, failed atomic.Int64

	for _, e := range entries {
		select {
		case <-ctx.Done():
			wg.Wait()
			f.logger.Info().Msg("metadata: image download cancelled")
			return
		default:
		}

		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		releaseName := strings.TrimSuffix(e.Name(), ".json")

		// Skip packages not in the operator's library.
		if !pkgSet[releaseName] {
			continue
		}

		// Canonical name without the leading dot (used for output file names).
		canonicalName := strings.TrimPrefix(releaseName, ".")

		jsonPath := filepath.Join(commonDir, e.Name())
		raw, err := os.ReadFile(jsonPath)
		if err != nil {
			continue
		}
		var meta metaCommonJSON
		if err := json.Unmarshal(raw, &meta); err != nil {
			continue
		}

		if meta.Icon != "" {
			dest := filepath.Join(iconsDir, canonicalName+".png")
			if _, statErr := os.Stat(dest); statErr != nil {
				wg.Add(1)
				sem <- struct{}{}
				go func(url, dest string) {
					defer wg.Done()
					defer func() { <-sem }()
					if err := f.downloadImage(ctx, imgClient, url, dest); err != nil {
						f.logger.Debug().Err(err).Str("dest", dest).Msg("metadata: icon download failed")
						failed.Add(1)
					} else {
						downloaded.Add(1)
					}
				}(meta.Icon, dest)
			}
		}

		thumbURL := meta.Square
		if thumbURL == "" {
			thumbURL = meta.Hero
		}
		if thumbURL != "" {
			dest := filepath.Join(thumbsDir, canonicalName+".jpg")
			if _, statErr := os.Stat(dest); statErr != nil {
				wg.Add(1)
				sem <- struct{}{}
				go func(url, dest string) {
					defer wg.Done()
					defer func() { <-sem }()
					if err := f.downloadImage(ctx, imgClient, url, dest); err != nil {
						f.logger.Debug().Err(err).Str("dest", dest).Msg("metadata: thumbnail download failed")
						failed.Add(1)
					} else {
						downloaded.Add(1)
					}
				}(thumbURL, dest)
			}
		}
	}

	wg.Wait()
	f.logger.Info().
		Int64("downloaded", downloaded.Load()).
		Int64("failed", failed.Load()).
		Int("packages", len(packageNames)).
		Msg("metadata: image download complete")

	// Generate notes files from oculusdb descriptions (no network I/O — pure file parsing).
	oculusDBDir := filepath.Join(base, "data", "oculusdb")
	if _, statErr := os.Stat(oculusDBDir); statErr == nil {
		notesDir := filepath.Join(cacheDir, "notes")
		if mkErr := os.MkdirAll(notesDir, 0755); mkErr != nil {
			f.logger.Warn().Err(mkErr).Msg("metadata: failed to create notes dir")
		} else {
			var notesWritten, notesSkipped int
			for _, pkg := range packageNames {
				select {
				case <-ctx.Done():
					f.logger.Info().Msg("metadata: notes generation cancelled")
					return
				default:
				}

				// MetaMetadata uses both "pkg.json" and ".pkg.json" naming conventions.
				var raw []byte
				if d, readErr := os.ReadFile(filepath.Join(oculusDBDir, pkg+".json")); readErr == nil {
					raw = d
				} else if d, readErr := os.ReadFile(filepath.Join(oculusDBDir, "."+pkg+".json")); readErr == nil {
					raw = d
				} else {
					notesSkipped++
					continue
				}

				var oDB metaOculusDBJSON
				if jsonErr := json.Unmarshal(raw, &oDB); jsonErr != nil || oDB.DisplayLongDescription == "" {
					notesSkipped++
					continue
				}

				notesPath := filepath.Join(notesDir, pkg+".txt")
				if writeErr := os.WriteFile(notesPath, []byte(oDB.DisplayLongDescription), 0644); writeErr != nil {
					f.logger.Debug().Err(writeErr).Str("package", pkg).Msg("metadata: notes write failed")
				} else {
					notesWritten++
				}
			}
			f.logger.Info().
				Int("written", notesWritten).
				Int("skipped", notesSkipped).
				Msg("metadata: notes generation complete")
		}
	}
}

// downloadImage fetches a single image URL and writes it to dest.
func (f *Fetcher) downloadImage(ctx context.Context, client *http.Client, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("get image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d for %s", resp.StatusCode, url)
	}

	file, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}

	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		os.Remove(dest)
		return fmt.Errorf("write %s: %w", dest, err)
	}

	return file.Close()
}

func (f *Fetcher) newRequest(ctx context.Context) (*http.Request, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", f.url, nil)

	// Add If-None-Match if ETag exists.
	etagPath := filepath.Join(f.dataDir, cacheDirName, etagFile)
	if data, err := os.ReadFile(etagPath); err == nil {
		req.Header.Set("If-None-Match", strings.TrimSpace(string(data)))
	}

	// Add If-Modified-Since if Last-Modified exists.
	lmPath := filepath.Join(f.dataDir, cacheDirName, lastModifiedFile)
	if data, err := os.ReadFile(lmPath); err == nil {
		formats := []string{"Mon, 02 Jan 2006 15:04:05 GMT", time.RFC1123}
		for _, layout := range formats {
			if t, parseErr := time.Parse(layout, strings.TrimSpace(string(data))); parseErr == nil {
				req.Header.Set("If-Modified-Since", t.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
				break
			}
		}
	}

	return req, nil
}

func (f *Fetcher) doWithRetry(req *http.Request) (*http.Response, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * baseRetryDelay
			f.logger.Warn().Int("attempt", attempt+1).Int("max_retries", maxRetries).Dur("delay", delay).Msg("Retrying metadata fetch")

			select {
			case <-req.Context().Done():
				return nil, fmt.Errorf("metadata fetcher: context cancelled during retry: %w", req.Context().Err())
			case <-time.After(delay):
			}
		}

		// Check context before making the request to allow cancellation of in-flight operations.
		select {
		case <-req.Context().Done():
			return nil, fmt.Errorf("metadata fetcher: context cancelled before request: %w", req.Context().Err())
		default:
		}

		resp, err := f.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("metadata fetcher attempt %d: %w", attempt+1, err)
			continue
		}

		// Retry on rate limiting (429) or server errors (5xx).
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			lastErr = fmt.Errorf("metadata fetcher attempt %d: status %d — %s", attempt+1, resp.StatusCode, string(body))

			// Honor Retry-After header on 429.
			if resp.StatusCode == http.StatusTooManyRequests {
				if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
					if secs, parseErr := strconv.Atoi(retryAfter); parseErr == nil && secs > 0 {
						// Clamp to avoid unbounded wait.
						if secs > 300 {
							secs = 300
						}
						f.logger.Warn().Int("retry_after_seconds", secs).Msg("Rate limited, waiting Retry-After duration")
						select {
						case <-req.Context().Done():
							return nil, fmt.Errorf("metadata fetcher: context cancelled during Retry-After: %w", req.Context().Err())
						case <-time.After(time.Duration(secs) * time.Second):
						}
					}
				}
			}
			continue
		}

		// Check context after Do() to catch mid-request cancellations.
		select {
		case <-req.Context().Done():
			resp.Body.Close()
			return nil, fmt.Errorf("metadata fetcher: context cancelled after request: %w", req.Context().Err())
		default:
		}

		return resp, nil
	}

	return nil, fmt.Errorf("metadata fetcher: all %d retries exhausted: %w", maxRetries, lastErr)
}

func (f *Fetcher) extractTarball(ctx context.Context, body io.Reader, destDir string) error {
	gzReader, err := gzip.NewReader(body)
	if err != nil {
		return fmt.Errorf("metadata fetcher: create gzip reader: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("metadata fetcher: read tar header: %w", err)
		}

		// Security: prevent path traversal.
		cleanName := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanName, "..") {
			f.logger.Warn().Str("file", header.Name).Msg("Skipping potentially malicious file in tarball")
			continue
		}

		targetPath := filepath.Join(destDir, cleanName)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("metadata fetcher: create dir %s: %w", targetPath, err)
			}

		case tar.TypeReg:
			if err := f.extractFile(ctx, targetPath, tarReader); err != nil {
				return fmt.Errorf("metadata fetcher: extract file %s: %w", header.Name, err)
			}

		default:
			// Skip symlinks, devices, etc.
			continue
		}
	}

	return nil
}

func (f *Fetcher) extractFile(ctx context.Context, destPath string, reader io.Reader) error {
	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("metadata fetcher: create parent dir for %s: %w", destPath, err)
	}

	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("metadata fetcher: create file %s: %w", destPath, err)
	}

	// Check context cancellation before writing.
	select {
	case <-ctx.Done():
		file.Close()
		os.Remove(destPath)
		return fmt.Errorf("metadata fetcher: context cancelled before write: %w", ctx.Err())
	default:
	}

	written, err := io.Copy(file, reader)
	if err != nil {
		file.Close()
		os.Remove(destPath)
		return fmt.Errorf("metadata fetcher: write file %s: %w", destPath, err)
	}

	if written > maxFileSize {
		file.Close()
		os.Remove(destPath)
		return fmt.Errorf("metadata fetcher: file %s exceeds maximum size (%d bytes)", destPath, maxFileSize)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("metadata fetcher: close file %s: %w", destPath, err)
	}
	vlog.Get().Debug().Int64("bytes", written).Str("file", destPath).Msg("Extracted metadata file")
	return nil
}

func (f *Fetcher) saveCacheHeaders(resp *http.Response) {
	cacheDir := filepath.Join(f.dataDir, cacheDirName)

	if etag := resp.Header.Get("ETag"); etag != "" {
		if err := os.WriteFile(filepath.Join(cacheDir, etagFile), []byte(etag), 0600); err != nil {
			f.logger.Warn().Err(err).Msg("Failed to save ETag")
		}
	}

	if lastMod := resp.Header.Get("Last-Modified"); lastMod != "" {
		if err := os.WriteFile(filepath.Join(cacheDir, lastModifiedFile), []byte(lastMod), 0600); err != nil {
			f.logger.Warn().Err(err).Msg("Failed to save Last-Modified")
		}
	}
}

// StartScheduledFetch starts a background goroutine that fetches metadata
// at the configured interval. It logs each refresh attempt and handles
// graceful shutdown via the done channel.
func (f *Fetcher) StartScheduledFetch(ctx context.Context) {
	defer close(f.waitCh)

	ticker := time.NewTicker(f.refreshInterval)
	defer ticker.Stop()

	f.logger.Info().Dur("interval", f.refreshInterval).Msg("Scheduled metadata refresh started")

	for {
		select {
		case <-ticker.C:
			if f.shutdown.Load() {
				f.logger.Info().Msg("Scheduled metadata refresh stopping")
				return
			}
			f.logger.Info().Msg("Scheduled metadata refresh triggered")
			tickCtx, tickCancel := context.WithCancel(f.shutdownCtx)
			if err := f.Fetch(tickCtx); err != nil {
				f.logger.Warn().Err(err).Msg("Scheduled metadata refresh failed")
			} else {
				f.logger.Info().Msg("Scheduled metadata refresh completed")
			}
			tickCancel()
			if f.shutdown.Load() {
				f.logger.Info().Msg("Scheduled metadata refresh stopping")
				return
			}
		case <-f.done:
			f.logger.Info().Msg("Scheduled metadata refresh stopping")
			return
		case <-ctx.Done():
			f.logger.Info().Msg("Scheduled metadata refresh context cancelled")
			return
		}
	}
}

// Stop signals the background refresh goroutine to stop gracefully.
// It is idempotent — safe to call multiple times.
func (f *Fetcher) Stop() {
	if !f.shutdown.CompareAndSwap(false, true) {
		return
	}
	if f.done != nil {
		close(f.done)
	}
	if f.shutdownCancel != nil {
		f.shutdownCancel()
	}
}

// Wait blocks until the scheduled fetch goroutine has exited.
// Returns false if the timeout is reached before the goroutine exits.
func (f *Fetcher) Wait(timeout time.Duration) bool {
	select {
	case <-f.waitCh:
		return true
	case <-time.After(timeout):
		return false
	}
}

// IsShutdown returns whether the fetcher has been stopped.
func (f *Fetcher) IsShutdown() bool {
	return f.shutdown.Load()
}

// saveLastRefreshTime stores the current time as the last refresh timestamp.
func (f *Fetcher) saveLastRefreshTime() error {
	cacheDir := filepath.Join(f.dataDir, cacheDirName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("metadata fetcher: create cache dir for last refresh: %w", err)
	}
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	if err := os.WriteFile(filepath.Join(cacheDir, lastRefreshFile), []byte(timestamp), 0600); err != nil {
		return fmt.Errorf("metadata fetcher: save last refresh time: %w", err)
	}
	return nil
}

// IsRefreshOverdue reports whether the cache has not been refreshed within the
// configured interval. Returns true when the timestamp file is missing.
func (f *Fetcher) IsRefreshOverdue() bool {
	lastRefresh, err := f.GetLastRefreshTime()
	if err != nil {
		return true
	}
	return time.Unix(lastRefresh, 0).Before(time.Now().Add(-f.refreshInterval))
}

// GetLastRefreshTime reads the last refresh timestamp from disk.
// Returns 0 if the file doesn't exist.
func (f *Fetcher) GetLastRefreshTime() (int64, error) {
	path := filepath.Join(f.dataDir, cacheDirName, lastRefreshFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("metadata fetcher: read last refresh time: %w", err)
	}
	return strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
}
