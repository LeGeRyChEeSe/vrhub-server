package api

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/archive"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/trailers"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	chi "github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"
)

// PublicAPIHandler serves public VRHub-compatible API endpoints.
type PublicAPIHandler struct {
	ModeVal    *atomic.Value
	DB         GameListProvider
	FileDB     FileServerDB
	FileReader FileReader
	Config     *types.Config
	// StatsDB is the per-game download counter sink (Story 7.5).
	// nil = stats recording disabled (the download handler gracefully
	// skips the increment). See StatsRecorder for the contract.
	StatsDB StatsRecorder
	// inceptionTime is the server-start timestamp (rounded to second
	// granularity). It is used as a stable Last-Modified sentinel for
	// payloads that have no natural modification time — the /config.json
	// response (no config.toml mtime plumbed in), and the /meta.7z
	// response when the catalog is empty (no MAX(last_updated) to fall
	// back to). Choosing a "fresh" sentinel rather than the epoch
	// (1970-01-01) prevents stale IMS dates from spuriously matching
	// and producing unwanted 304s on an empty catalog. Pinned at
	// handler construction; stable for the lifetime of the process.
	inceptionTime time.Time
}

// GameListProvider provides game entries for meta.7z generation.
type GameListProvider interface {
	ListGamesForMeta7z() ([]types.GameEntry, error)
	// GetCatalogLastModified returns MAX(last_updated) across ALL games
	// (including unexposed) for use as the Last-Modified header on /meta.7z.
	// This prevents the header from going stale when a recently-updated game
	// is unexposed and would otherwise not appear in the filtered list.
	GetCatalogLastModified() (time.Time, error)
}

// FileServerDB provides database methods needed by FileServerHandler.
type FileServerDB interface {
	GetGameByHash(hash string) (*types.GameEntry, error)
	ListPackagesByHash(hash string) ([]string, error)
}

// StatsRecorder increments per-game download counters after a successful
// file download. The hook is async fire-and-forget (Story 7.5 T2):
// the download handler does NOT block on the write, so the DB call
// cannot delay the response. If the server crashes between scheduling
// the goroutine and the SQLite commit, that one increment is lost —
// acceptable for usage statistics.
//
// Defined as an interface (not *db.DB) so tests can inject a stub
// recorder without standing up a real SQLite database. The real
// implementation is *db.DB (which has IncrementDownloadStats since
// 7.5 T1); production wiring lives in MountPublicRoutes.
type StatsRecorder interface {
	IncrementDownloadStats(hash string, bytesServed int64) error
}

// FileReader provides file system access for directory listings.
type FileReader interface {
	Open(name string) (*os.File, error)
	ReadDir(dirname string) ([]os.DirEntry, error)
}

// realFileReader is the default implementation using the real OS filesystem.
type realFileReader struct{}

func (r *realFileReader) Open(name string) (*os.File, error) { return os.Open(name) }
func (r *realFileReader) ReadDir(dirname string) ([]os.DirEntry, error) {
	return os.ReadDir(dirname)
}

// NewPublicAPIHandler creates a new public API handler.
func NewPublicAPIHandler(modeVal *atomic.Value) *PublicAPIHandler {
	return &PublicAPIHandler{
		ModeVal:    modeVal,
		FileReader: &realFileReader{},
	}
}

// NewPublicAPIHandlerWithDeps creates a new public API handler with all dependencies.
//
// Story 7.5: added stats parameter for the async download-stats
// increment. Pass nil to disable stats recording (e.g. tests or
// read-only deployment); the download handler gracefully skips the
// increment when stats == nil.
func NewPublicAPIHandlerWithDeps(modeVal *atomic.Value, db GameListProvider, fileDB FileServerDB, fr FileReader, stats StatsRecorder) *PublicAPIHandler {
	return &PublicAPIHandler{
		ModeVal:       modeVal,
		DB:            db,
		FileDB:        fileDB,
		FileReader:    fr,
		StatsDB:       stats,
		inceptionTime: time.Now().UTC().Truncate(time.Second),
	}
}

// getMode returns the current server mode from the atomic value.
func (h *PublicAPIHandler) getMode() types.ServerMode {
	if s, ok := h.ModeVal.Load().(string); ok {
		return types.ServerMode(s)
	}
	return types.ModeSetup
}

// Meta7zHandler handles GET /meta.7z — returns the generated 7z archive.
func (h *PublicAPIHandler) Meta7zHandler(w http.ResponseWriter, r *http.Request) {
	if h.getMode() == types.ModeSetup {
		http.Error(w, "Server not configured", http.StatusServiceUnavailable)
		return
	}

	deps := meta7zDeps{
		DB:            h.DB,
		Config:        h.Config,
		InceptionTime: h.inceptionTime,
	}

	meta7zHandlerWithDeps(deps).ServeHTTP(w, r)
}

// meta7zDeps holds the dependencies needed by Meta7zHandler.
type meta7zDeps struct {
	DB            GameListProvider
	Config        *types.Config
	InceptionTime time.Time
}

// computeCatalogETag returns a stable, RFC 7232-compliant ETag value
// (including the surrounding double quotes) for the current catalog
// (meta.7z) payload. The hash is computed over the same filtered game
// list that BuildGameListForMeta7z produces, so an identical 7z body
// always yields an identical ETag.
//
// Story 9.9 (B9 fix): the VRHub client's catalog-sync logic compares
// the server's ETag / Last-Modified to a locally-cached value to decide
// whether to re-download meta.7z. Without these headers the client
// receives `remote metadata: {}` from the server, can't compare, and
// keeps its stale Worker cache forever (see reproduce in 9.9 dev notes).
//
// Algorithm: stable, deterministic, no time.Now() involvement. We hash
// the package_name list (sorted) joined with `|`, plus the max
// last_updated (as Unix seconds) across the filtered set. Any change
// to the visible catalog — add, remove, metadata refresh, re-expose —
// changes one of those two inputs and yields a new ETag.
func computeCatalogETag(games []types.GameEntry) string {
	pkgs := make([]string, 0, len(games))
	var maxUpdated time.Time
	for _, g := range games {
		if g.PackageName == "" {
			continue
		}
		pkgs = append(pkgs, g.PackageName)
		if g.LastUpdated.After(maxUpdated) {
			maxUpdated = g.LastUpdated
		}
	}
	sort.Strings(pkgs)
	joined := strings.Join(pkgs, "|")
	h := md5.Sum([]byte(fmt.Sprintf("%d|%s", maxUpdated.Unix(), joined)))
	return fmt.Sprintf("\"%s\"", hex.EncodeToString(h[:]))
}

// computeConfigETag returns a stable ETag for the /config.json payload.
// The hash covers the archive password + bind host/port. Any change to
// those three fields invalidates the cache.
func computeConfigETag(cfg *types.Config) string {
	if cfg == nil {
		return "\"\""
	}
	h := md5.Sum([]byte(fmt.Sprintf("%s|%s|%d",
		cfg.Admin.ArchivePassword, cfg.Server.Host, cfg.Server.Port)))
	return fmt.Sprintf("\"%s\"", hex.EncodeToString(h[:]))
}

// shortETag returns the first 8 hex chars of an ETag (without the
// surrounding quotes) for compact logging. The full ETag stays in the
// response header.
func shortETag(etag string) string {
	etag = strings.Trim(etag, "\"")
	if len(etag) > 8 {
		return etag[:8]
	}
	return etag
}

// meta7zHandlerWithDeps returns the handler function with injected dependencies.
//
// The endpoint is INTENTIONALLY UNAUTHENTICATED at the HTTP layer.
//
// Rationale: the VRHub client (com.vrhub.logic.CatalogUtils.downloadFile,
// MainRepository.syncCatalog) issues a plain GET against `${baseUri}meta.7z`
// with only a User-Agent header — it never sends an X-Password-style
// header. The "password" returned by /config.json is decoded by the
// client and used to extract the 7z archive LOCALLY after the download
// (see ServerConfig.kt: "The password for extracting archives from the
// server"). Therefore the HTTP-level password check is dead weight that
// breaks every client while adding zero security — the 7z archive is
// already AES-256 encrypted (LZMA2 + AES-256 per the VRHub protocol),
// and the extraction password is the actual access control. We do keep
// the password used for archive encryption in the request log for
// operator visibility.
//
// We still log the request (User-Agent + remote_addr) at Info so the
// operator can see who's hitting the catalog endpoint.
//
// Story 9.9 (B9 fix): the response now carries ETag + Last-Modified +
// Cache-Control headers so the client's catalog-sync logic can detect
// catalog changes. The handler also honors If-None-Match /
// If-Modified-Since and replies 304 Not Modified when the client's
// cached version is still current, avoiding the ~50 ms 7z generation
// + full body transfer on every sync poll.
func meta7zHandlerWithDeps(deps meta7zDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Config == nil || deps.Config.Admin.ArchivePassword == "" {
			log.Error().Msg("archive password not configured (cannot encrypt meta.7z)")
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		if deps.DB == nil {
			log.Error().Msg("database not initialized")
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		games, err := deps.DB.ListGamesForMeta7z()
		if err != nil {
			log.Error().Err(err).Msg("failed to list games for meta.7z")
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		filtered := archive.BuildGameListForMeta7z(games)

		// ETag is computed from the filtered (exposed) game list — it reflects
		// the actual archive content (package names + max last_updated).
		etag := computeCatalogETag(filtered)

		// Story 11.3 (hybrid trailers): the configured trailer language changes
		// the trailers/*.txt search links embedded in meta.7z, so fold it into
		// the validator — otherwise a language change would not invalidate
		// client caches and clients would keep the old links.
		if lang := cfgTrailerLanguage(deps.Config); lang != "" {
			sum := md5.Sum([]byte(etag + "|tl=" + lang))
			etag = fmt.Sprintf("\"%s\"", hex.EncodeToString(sum[:]))
		}

		// Last-Modified uses MAX(last_updated) across ALL games, not just
		// exposed ones. This ensures the header always advances when any
		// catalog mutation occurs (expose/unexpose/import/delete), even
		// when the mutated game is excluded from the filtered list.
		// Without this, exposing a game that was previously the most-recently
		// updated would leave Last-Modified unchanged — the VRHub client's
		// OR-based cache check (Last-Modified OR ETag OR MD5) would then
		// short-circuit on the matching Last-Modified and skip the download.
		catalogTS, tsErr := deps.DB.GetCatalogLastModified()
		var lastModified time.Time
		if tsErr == nil && !catalogTS.IsZero() {
			lastModified = time.Unix(catalogTS.Unix(), 0).UTC()
		} else {
			if tsErr != nil {
				log.Warn().Err(tsErr).Msg("failed to get catalog last modified, falling back to filtered list")
			}
			// Fallback: derive from the filtered list (original behaviour).
			for _, g := range filtered {
				if g.LastUpdated.After(lastModified) {
					lastModified = g.LastUpdated
				}
			}
			if !lastModified.IsZero() {
				lastModified = time.Unix(lastModified.Unix(), 0).UTC()
			}
		}
		if lastModified.IsZero() {
			// Empty catalog: use inception time as sentinel so a stale
			// If-Modified-Since never produces a spurious 304.
			lastModified = deps.InceptionTime
		}

		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", lastModified.Format(http.TimeFormat))
		w.Header().Set("Cache-Control", "no-cache")

		// Validate client's cached copy (Story 9.9 B9 fix: client
		// sends If-None-Match from its persisted metadata, or
		// If-Modified-Since as a fallback. If either matches the
		// current catalog, we return 304 with no body).
		if matchesClientCache(r, etag, lastModified) {
			w.WriteHeader(http.StatusNotModified)
			log.Info().
				Str("remote_addr", r.RemoteAddr).
				Str("etag", shortETag(etag)).
				Msg("meta.7z 304: cache hit (etag/date match)")
			return
		}

		// Minimal per-request access log. The password is logged
		// only as a length + masked form, never in cleartext, so
		// log aggregation doesn't leak the archive password.
		log.Info().
			Str("remote_addr", r.RemoteAddr).
			Str("user_agent", r.UserAgent()).
			Str("archive_password", maskSecret(deps.Config.Admin.ArchivePassword)).
			Int("archive_password_length", len(deps.Config.Admin.ArchivePassword)).
			Str("etag", shortETag(etag)).
			Msg("meta.7z 200: streaming encrypted catalog (no HTTP auth — see handler docstring)")

		metadataPath := filepath.Join(deps.Config.DataDir, "metadata")
		metadata, err := archive.NewMetadataCache(metadataPath)
		if err != nil {
			log.Error().Err(err).Str("path", metadataPath).Msg("failed to create metadata cache")
			// Strip cache-validation headers before replying 5xx.
			// A 500 must not advertise ETag/Last-Modified/Cache-Control
			// because the next response is not guaranteed to be a
			// re-derivation of this representation — it could be a
			// freshly generated 7z, or another 5xx. Letting the client
			// cache-validate a 5xx would be a protocol violation.
			w.Header().Del("ETag")
			w.Header().Del("Last-Modified")
			w.Header().Del("Cache-Control")
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		ctx := r.Context()
		archivePassword := ""
		if deps.Config != nil {
			archivePassword = deps.Config.Admin.ArchivePassword
		}

		// Generate the archive into a buffer FIRST, then commit the
		// response. Streaming via io.Pipe (the previous approach) forced
		// us to WriteHeader(200) before generation finished, so any
		// generation failure (missing/incompatible 7zz binary, exec
		// error) left the client with a 200 OK + empty body — a corrupt
		// catalog indistinguishable from "no games". By buffering we can
		// surface a real 500 on failure. The catalog (game list + small
		// metadata files) is modest in size, so in-memory buffering is
		// acceptable.
		// Story 11.3 (hybrid trailers): expose a trailer link for EVERY game.
		// Games with a resolved/override trailer_url keep it; the rest get a
		// YouTube search link for "{gameName} trailer" in the configured
		// language. This is a transient view for archive generation only — it
		// is never persisted to the DB (so adding an API key later can still
		// upgrade the empty-trailer_url games to specific videos).
		trailerLang := cfgTrailerLanguage(deps.Config)
		withTrailers := make([]types.GameEntry, len(filtered))
		for i, g := range filtered {
			g.TrailerURL = trailers.EffectiveTrailerURL(g, trailerLang)
			withTrailers[i] = g
		}

		var buf bytes.Buffer
		if genErr := archive.GenerateMeta7z(ctx, withTrailers, metadata, &buf, archivePassword); genErr != nil {
			log.Error().Err(genErr).Msg("meta.7z generation failed")
			// Strip cache-validation headers before replying 5xx (a 500
			// must not advertise ETag/Last-Modified/Cache-Control).
			w.Header().Del("ETag")
			w.Header().Del("Last-Modified")
			w.Header().Del("Cache-Control")
			http.Error(w, "Failed to generate catalog archive", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/x-7z-compressed")
		w.Header().Set("Content-Disposition", `attachment; filename="meta.7z"`)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
		w.WriteHeader(http.StatusOK)

		if _, copyErr := w.Write(buf.Bytes()); copyErr != nil {
			log.Warn().Err(copyErr).Msg("stream copy error during meta.7z download")
		}
	}
}

// matchesClientCache returns true if the request's conditional headers
// (If-None-Match, If-Modified-Since) match the current server-side
// ETag / Last-Modified, in which case the handler should reply
// 304 Not Modified instead of generating + streaming the body.
//
// Both checks are RFC 7232 compliant:
//   - If-None-Match: exact string match (with quotes) wins. We don't
//     implement the comma-separated list form yet (single-ETag clients
//     are the only ones we know the VRHub app uses).
//   - If-Modified-Since: parsed as HTTP date; we compare second
//     granularity. A client sending a date >= server's Last-Modified
//     is treated as "still fresh".
func matchesClientCache(r *http.Request, etag string, lastModified time.Time) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		// Trim any surrounding whitespace and ignore weak prefix
		// (W/"…") — both forms are accepted by the spec.
		inm = strings.TrimSpace(inm)
		if strings.HasPrefix(inm, "W/") {
			inm = strings.TrimPrefix(inm, "W/")
		}
		// Defensive: an INM that trims to empty (header was sent as
		// "If-None-Match:   " or "If-None-Match: W/" with no value)
		// is semantically equivalent to the header being absent. Fall
		// through to the IMS check rather than short-circuiting to
		// "no match" (which would skip IMS validation entirely).
		if inm == "" {
			// fall through to IMS check below
		} else if inm == etag {
			return true
		} else {
			// No match — fall through to date check? No: per RFC 7232,
			// If-None-Match takes precedence. A non-match on ETag means
			// the cache is stale regardless of date.
			return false
		}
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		t, err := http.ParseTime(ims)
		if err == nil && !t.Before(lastModified) {
			return true
		}
	}
	return false
}

// maskSecret returns a human-friendly redacted form of s: the first two
// runes, "..." and the last two runes, plus a length tag. Used by
// verbose log lines so an operator can spot a Base64 vs cleartext
// mismatch ("sent 'te...78' len 16" vs "sent 'te...78' len 12") without
// ever logging the actual secret in plaintext. Empty input is rendered
// as "<empty>".
func maskSecret(s string) string {
	if s == "" {
		return "<empty>"
	}
	runes := []rune(s)
	if len(runes) <= 4 {
		return fmt.Sprintf("len=%d,value=%q", len(runes), s)
	}
	return fmt.Sprintf("len=%d,prefix=%q,suffix=%q", len(runes), string(runes[:2]), string(runes[len(runes)-2:]))
}

// htmlEscapeString escapes HTML special characters in s.
func htmlEscapeString(s string) string {
	var buf strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		case '"':
			buf.WriteString("&quot;")
		case '\\':
			buf.WriteString("&#92;")
		default:
			if r < 0x20 {
				fmt.Fprintf(&buf, "&#%02d;", r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	return buf.String()
}

// encodeContentDispositionFilename encodes a filename for the Content-Disposition header per RFC 5987.
// Returns filename*=UTF-8”<percent-encoded> for non-ASCII/special chars, or plain filename for ASCII-safe names.
func encodeContentDispositionFilename(filename string) string {
	needsEncoding := false
	for _, r := range filename {
		if !isAttrChar(r) {
			needsEncoding = true
			break
		}
	}
	if !needsEncoding {
		return fmt.Sprintf("attachment; filename=\"%s\"", filename)
	}

	// RFC 5987 ext-value: percent-encode non-attr-char bytes
	encoded := encodeRFC5987(filename)

	// filename parameter: use token-safe encoding (RFC 6266 / RFC 2616 token rules)
	// Replace characters invalid in HTTP tokens with HTML entities for compatibility
	safeFilename := encodeTokenFilename(filename)

	return fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s", safeFilename, encoded)
}

// isAttrChar reports whether r is a valid RFC 5987 attr-char.
// attr-char = ALPHA / DIGIT / "!" / "#" / "$" / "&" / "+" / "-" / "." / "^" / "_" / "`" / "'" / "%" / "*"
func isAttrChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '!':
		return true
	case r == '#':
		return true
	case r == '$':
		return true
	case r == '&':
		return true
	case r == '+':
		return true
	case r == '-':
		return true
	case r == '.':
		return true
	case r == '^':
		return true
	case r == '_':
		return true
	case r == '`':
		return true
	case r == '\'':
		return true
	case r == '%':
		return true
	case r == '*':
		return true
	default:
		return false
	}
}

// encodeRFC5987 encodes s as an RFC 5987 ext-value (percent-encoding non-attr-char bytes).
func encodeRFC5987(s string) string {
	var buf strings.Builder
	for _, r := range s {
		if isAttrChar(r) {
			buf.WriteRune(r)
		} else if r < 0x80 {
			// Non-ASCII printable (e.g. ", \, space, control chars)
			fmt.Fprintf(&buf, "%%%02X", r)
		} else {
			// Multi-byte UTF-8: percent-encode each byte
			for _, b := range []byte(string(r)) {
				fmt.Fprintf(&buf, "%%%02X", b)
			}
		}
	}
	return buf.String()
}

// encodeTokenFilename encodes a filename for the plain filename= parameter.
// Per RFC 6266/RFC 2616, the value must be a token or quoted-string.
// We use HTML entities for characters that would break HTTP token parsing.
func encodeTokenFilename(s string) string {
	var buf strings.Builder
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString("&quot;")
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		default:
			if r < 0x20 {
				fmt.Fprintf(&buf, "%%%02X", r)
			} else if r == '\\' {
				buf.WriteString("&#92;")
			} else {
				buf.WriteRune(r)
			}
		}
	}
	return buf.String()
}

// FileServerHandler serves files from game directories by hash.
func (h *PublicAPIHandler) FileServerHandler(w http.ResponseWriter, r *http.Request) {
	if h.getMode() == types.ModeSetup {
		http.Error(w, "Server not configured", http.StatusServiceUnavailable)
		return
	}

	deps := fileServerDeps{
		FileDB:     h.FileDB,
		FileReader: h.FileReader,
		Config:     h.Config,
		StatsDB:    h.StatsDB,
	}

	fileServerHandlerWithDeps(deps).ServeHTTP(w, r)
}

// fileServerDeps holds the dependencies needed by FileServerHandler.
type fileServerDeps struct {
	FileDB     FileServerDB
	FileReader FileReader
	Config     *types.Config
	// StatsDB is the per-game download counter sink. Story 7.5 T2:
	// serveFileDownload fires an async IncrementDownloadStats on
	// HTTP 200 only (no 206 partial, no 4xx, no 5xx). nil disables.
	StatsDB StatsRecorder
}

// fileServerHandlerWithDeps returns the handler function with injected dependencies.
func fileServerHandlerWithDeps(deps fileServerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash := chi.URLParam(r, "hash")
		if hash == "" {
			http.NotFound(w, r)
			return
		}

		// Validate hash format: must be a 32-char hex MD5 digest
		// (MD5(packageName + "\n") — mirrors the VRHub client's
		// CryptoUtils.md5(releaseName + "\n") URL construction).
		// Also accept 64-char SHA-256 to serve games imported by
		// a buggy intermediate build (fixed in this commit).
		if len(hash) != 32 && len(hash) != 64 {
			http.NotFound(w, r)
			return
		}

		if deps.FileDB == nil {
			log.Error().Msg("file server database not initialized")
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Look up game by hash in DB
		game, err := deps.FileDB.GetGameByHash(hash)
		if err != nil {
			log.Debug().Str("hash", hash).Err(err).Msg("game not found by hash")
			http.NotFound(w, r)
			return
		}

		// Determine path depth after hash
		// chi.URLParam(r, "*") gives everything after the hash
		// (the catch-all wildcard is `/*`, not `{path:*}` — see chi docs).
		path := chi.URLParam(r, "*")
		// Remove leading slash if present
		path = strings.TrimPrefix(path, "/")

		if path == "" || path == "/" {
			// Package-level listing: GET /{hash}/ or GET /{hash}
			servePackageListing(w, r, deps, game)
			return
		}

		// Extract packageName from path (first segment only)
		pkgName := strings.SplitN(path, "/", 2)[0]

		if pkgName == "" {
			http.NotFound(w, r)
			return
		}

		// Handle hash-level metadata files (notes.txt, thumbnail.jpg) before
		// package name validation. These are single-component paths (no "/")
		// served directly from the metadata cache, not from game directories.
		// The VRHub client fetches notes.txt directly for descriptions, and
		// discovers screenshot images (*.jpg) from the package listing hrefs.
		if !strings.Contains(path, "/") {
			switch {
			case path == "notes.txt":
				serveNotesFile(w, r, deps, game)
				return
			case path == "trailer.txt":
				// Story 11.1 — Delivery channel B: the trailer URL is
				// served as plain text from the game's DB column (no file
				// on disk), parallel to notes.txt.
				serveTrailerFile(w, r, game, cfgTrailerLanguage(deps.Config))
				return
			case isImageExtension(path):
				serveMetadataImage(w, r, deps, game, path)
				return
			}
		}

		// Verify package name matches the game's package_name in DB
		if pkgName != game.PackageName {
			log.Debug().Str("hash", hash).Str("pkg", pkgName).Str("expected_pkg", game.PackageName).Msg("package name mismatch")
			http.NotFound(w, r)
			return
		}

		// Prevent path traversal: reject package names containing ".." or absolute paths
		if strings.Contains(pkgName, "..") || filepath.IsAbs(pkgName) {
			log.Warn().Str("hash", hash).Str("pkg", pkgName).Msg("path traversal attempt detected")
			http.NotFound(w, r)
			return
		}

		// Check if path ends with / (directory listing) or is a file request
		if strings.HasSuffix(path, "/") || path == pkgName {
			serveFileListing(w, r, deps, game, pkgName)
			return
		}

		// File download: path = packageName/filename (no trailing slash)
		filePathParts := strings.SplitN(path, "/", 2)
		if len(filePathParts) == 2 {
			filePkgName := filePathParts[0]
			fileName := filePathParts[1]

			// Verify package name matches the game's package_name in DB
			if filePkgName != game.PackageName {
				log.Debug().Str("hash", hash).Str("pkg", filePkgName).Str("expected_pkg", game.PackageName).Msg("package name mismatch for file download")
				http.NotFound(w, r)
				return
			}

			// Prevent path traversal: reject filenames containing ".." or absolute paths
			if strings.Contains(fileName, "..") || filepath.IsAbs(fileName) {
				log.Warn().Str("hash", hash).Str("pkg", filePkgName).Str("file", fileName).Msg("path traversal attempt in filename detected")
				http.NotFound(w, r)
				return
			}

			serveFileDownload(w, r, deps, game, filePkgName, fileName)
			return
		}

		// Unknown path depth — 404
		http.NotFound(w, r)
	}
}

// isImageExtension reports whether filename ends with a client-recognized image extension.
func isImageExtension(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png"
}

// serveNotesFile serves the game's description text from the metadata cache.
// Called for GET /{hash}/notes.txt requests.
func serveNotesFile(w http.ResponseWriter, r *http.Request, deps fileServerDeps, game *types.GameEntry) {
	if deps.Config == nil {
		http.NotFound(w, r)
		return
	}
	notesPath := filepath.Join(deps.Config.DataDir, "metadata", "notes", game.PackageName+".txt")
	info, err := os.Stat(notesPath)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(notesPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// serveTrailerFile serves the game's resolved trailer URL as plain text.
// Called for GET /{hash}/trailer.txt requests (Story 11.1 — Delivery
// channel B). Parallel to serveNotesFile, but the payload comes from the
// game's DB column (game.TrailerURL), NOT a file on disk: the server never
// hosts the video, only the link.
//
// cfgTrailerLanguage returns the configured trailer language (Story 11.3), or
// "" when no config is wired. Used by the delivery layer to build the YouTube
// search-link fallback. Takes *types.Config so it works for both meta7zDeps and
// fileServerDeps call sites.
func cfgTrailerLanguage(cfg *types.Config) string {
	if cfg != nil {
		return cfg.Trailer.Language
	}
	return ""
}

// Story 11.3 (hybrid): the body is the game's EFFECTIVE trailer URL — a
// resolved/override video URL when present, otherwise a YouTube search link for
// "{gameName} trailer" in `language`. 404 only when neither exists (a nameless
// game), symmetric with the listing.
func serveTrailerFile(w http.ResponseWriter, r *http.Request, game *types.GameEntry, language string) {
	if game == nil {
		http.NotFound(w, r)
		return
	}
	url := trailers.EffectiveTrailerURL(*game, language)
	if url == "" {
		http.NotFound(w, r)
		return
	}
	data := []byte(url)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// serveMetadataImage serves a known metadata image (e.g. thumbnail.jpg) from the
// metadata cache. Called for GET /{hash}/{imagename} requests where imagename is
// a recognized metadata image filename. Unknown names return 404.
func serveMetadataImage(w http.ResponseWriter, r *http.Request, deps fileServerDeps, game *types.GameEntry, filename string) {
	if deps.Config == nil {
		http.NotFound(w, r)
		return
	}

	var imagePath string
	switch filename {
	case "thumbnail.jpg":
		imagePath = filepath.Join(deps.Config.DataDir, "metadata", "thumbnails", game.PackageName+".jpg")
	default:
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(imagePath)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(imagePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(filename))
	contentType := "image/jpeg"
	if ext == ".png" {
		contentType = "image/png"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.WriteHeader(http.StatusOK)
	io.Copy(w, f) //nolint:errcheck
}

// servePackageListing generates HTML with links to package subdirectories.
func servePackageListing(w http.ResponseWriter, r *http.Request, deps fileServerDeps, game *types.GameEntry) {
	packages, err := deps.FileDB.ListPackagesByHash(game.Hash)
	if err != nil {
		log.Error().Err(err).Str("hash", game.Hash).Msg("failed to list packages by hash")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	title := htmlEscapeString(game.GameName)
	fmt.Fprintf(w, "<!DOCTYPE html>\n<html lang=\"en\">\n<head><meta charset=\"utf-8\"><title>%s</title></head>\n<body>\n", title)
	fmt.Fprintf(w, "<h1>%s</h1>\n<ul>\n", title)

	if len(packages) == 0 {
		fmt.Fprintf(w, "<p>No packages found.</p>\n")
	} else {
		for _, pkg := range packages {
			encodedPkg := url.PathEscape(pkg)
			fmt.Fprintf(w, "<li><a href=\"%s/\">%s/</a></li>\n", encodedPkg, htmlEscapeString(pkg))
		}
	}

	// Expose metadata files so the VRHub client can discover them from the listing.
	// The client parses href links ending in .jpg/.png/.jpeg as screenshot URLs,
	// and fetches notes.txt directly for the game description.
	if deps.Config != nil {
		thumbPath := filepath.Join(deps.Config.DataDir, "metadata", "thumbnails", game.PackageName+".jpg")
		if info, statErr := os.Stat(thumbPath); statErr == nil && !info.IsDir() {
			fmt.Fprintf(w, "<li><a href=\"thumbnail.jpg\">thumbnail.jpg</a></li>\n")
		}
		notesPath := filepath.Join(deps.Config.DataDir, "metadata", "notes", game.PackageName+".txt")
		if info, statErr := os.Stat(notesPath); statErr == nil && !info.IsDir() {
			fmt.Fprintf(w, "<li><a href=\"notes.txt\">notes.txt</a></li>\n")
		}
	}

	// Story 11.1 — Delivery channel B: advertise the trailer link when the
	// game has a resolved trailer URL. The URL lives on the DB row (not a
	// file on disk), so this is independent of deps.Config. serveTrailerFile
	// serves the body at GET /{hash}/trailer.txt.
	// Story 11.3 (hybrid): every named game exposes trailer.txt — a resolved
	// video URL, or a YouTube search link as the fallback.
	if trailers.EffectiveTrailerURL(*game, cfgTrailerLanguage(deps.Config)) != "" {
		fmt.Fprintf(w, "<li><a href=\"trailer.txt\">trailer.txt</a></li>\n")
	}

	fmt.Fprintf(w, "</ul>\n</body>\n</html>\n")
}

// serveFileListing generates HTML with links to APK and OBB files on disk.
func serveFileListing(w http.ResponseWriter, r *http.Request, deps fileServerDeps, game *types.GameEntry, pkgName string) {
	if deps.Config == nil {
		log.Error().Msg("config not initialized for file listing")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if deps.FileReader == nil {
		log.Error().Msg("file reader not initialized")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Story 9.10 (B10): resolve the game directory from the absolute
	// path of the APK as the scanner recorded it. The directory
	// containing the APK is where the paired OBB lives (the scanner
	// pairs OBBs by directory + version code).
	//
	// Legacy fallback: if apk_path is empty (game predates the 9.10
	// migration), fall back to the canonical dataDir/games/{hash}/
	// {pkgName}/ layout so installs that haven't yet run the startup
	// backfill still work.
	var gameDir string
	if game.ApkPath != "" {
		gameDir = filepath.Dir(game.ApkPath)
	} else {
		gameDir = filepath.Join(deps.Config.DataDir, "games", game.Hash, pkgName)
	}

	entries, err := deps.FileReader.ReadDir(gameDir)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug().Str("dir", gameDir).Msg("game directory not found on disk")
			http.NotFound(w, r)
			return
		}
		log.Error().Err(err).Str("dir", gameDir).Msg("failed to read game directory")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	title := htmlEscapeString(pkgName)
	fmt.Fprintf(w, "<!DOCTYPE html>\n<html lang=\"en\">\n<head><meta charset=\"utf-8\"><title>%s</title></head>\n<body>\n", title)
	fmt.Fprintf(w, "<h1>%s</h1>\n<ul>\n", title)

	listedOBB := false
	for _, entry := range entries {
		name := entry.Name()

		// Skip hidden files and system files
		if strings.HasPrefix(name, ".") || name == "Thumbs.db" || name == "desktop.ini" {
			continue
		}

		// Only list APK and OBB files
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".apk" && ext != ".obb" {
			continue
		}
		if ext == ".obb" {
			listedOBB = true
		}

		encodedName := url.PathEscape(name)
		fmt.Fprintf(w, "<li><a href=\"%s\">%s</a></li>\n", encodedName, htmlEscapeString(name))
	}

	// OBB may be stored in a subdirectory of the release folder (e.g.
	// com.Package/main.N.com.Package.obb). ReadDir only walks one level,
	// so inject the OBB basename from game.OBBPath when it lives outside
	// gameDir. serveFileDownload routes any .obb URL directly to OBBPath.
	if !listedOBB && game.OBBPath != "" && filepath.Dir(game.OBBPath) != gameDir {
		obbName := filepath.Base(game.OBBPath)
		encodedName := url.PathEscape(obbName)
		fmt.Fprintf(w, "<li><a href=\"%s\">%s</a></li>\n", encodedName, htmlEscapeString(obbName))
	}

	fmt.Fprintf(w, "</ul>\n</body>\n</html>\n")
}

// parseRangeHeader extracts start/end byte positions from a Range header value.
// Supports "bytes=start-end" and "bytes=start-" (to EOF) formats.
//
// Returns (start, end, valid, unsatisfiable):
//   - valid=false, _ : malformed Range header (caller should respond 400)
//   - valid=true, unsatisfiable=true : range is well-formed but beyond file size
//     (caller should respond 416 per RFC 7233 §4.4)
//   - valid=true, unsatisfiable=false : satisfiable range (caller serves 206)
//
// C-11 (debt-triage-2026-06-06): previous code "saved" unsatisfiable ranges
// by serving from byte 0 with HTTP 206, which violates RFC 7233 §4.4.
// The new behavior returns 416 with Content-Range: */<fileSize>.
func parseRangeHeader(rangeHeader string, fileSize int64) (int64, int64, bool, bool) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, false, false
	}

	spec := strings.TrimPrefix(rangeHeader, "bytes=")
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false, false
	}

	startStr := strings.TrimSpace(parts[0])
	endStr := strings.TrimSpace(parts[1])

	var start int64
	_, err := fmt.Sscanf(startStr, "%d", &start)
	if err != nil || start < 0 {
		return 0, 0, false, false
	}

	var end int64
	if endStr == "" {
		end = fileSize - 1
	} else {
		_, err = fmt.Sscanf(endStr, "%d", &end)
		if err != nil || end < start {
			return 0, 0, false, false
		}
	}

	// Empty file: any range is unsatisfiable.
	if fileSize == 0 {
		return 0, 0, true, true
	}

	// Clamp end to file size - 1
	if end >= fileSize {
		end = fileSize - 1
	}

	// If start is beyond file size, the range is unsatisfiable.
	// RFC 7233 §4.4: "If a valid byte-range-set includes at least one
	// byte-range-spec with a first-byte-pos that is less than the
	// current length of the selected representation, or at least one
	// suffix-byte-range-spec with a non-zero suffix-length, then the
	// byte-range-set is satisfiable. Otherwise, the byte-range-set is
	// unsatisfiable." Caller must return 416.
	if start >= fileSize {
		return 0, 0, true, true
	}

	return start, end, true, false
}

// detectContentType returns the appropriate Content-Type for a given filename.
func detectContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".apk":
		return "application/vnd.android.package-archive"
	case ".obb":
		return "application/octet-stream"
	default:
		return "application/octet-stream"
	}
}

// serveFileDownload serves a game file with HTTP Range support for resumable downloads.
func serveFileDownload(w http.ResponseWriter, r *http.Request, deps fileServerDeps, game *types.GameEntry, pkgName string, fileName string) {
	if deps.Config == nil {
		log.Error().Msg("config not initialized for file download")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if deps.FileReader == nil {
		log.Error().Msg("file reader not initialized for file download")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Story 9.10 (B10): resolve the file path from the absolute path
	// stored in DB. We use:
	//   - game.ApkPath for *.apk files
	//   - game.OBBPath for *.obb files
	//   - legacy dataDir/games/{hash}/{pkgName}/{fileName} as fallback
	//     (for games that pre-date the 9.10 column and have not yet
	//     been backfilled by the startup scan).
	//
	// The previous hard-coded layout ignored the operator's
	// game_folders configuration: the scanner found the APK anywhere
	// in game_folders, but the file server always looked under
	// dataDir/games/. Operators had to manually copy the file to the
	// canonical location to make it downloadable (see the live AkiBonbon
	// bug: the orphan APK at the root of D:\Documents\Jeux\VR\Test\
	// was scanned but 404'd on download).
	var filePath string
	ext := strings.ToLower(filepath.Ext(fileName))
	switch {
	case game.ApkPath != "" && ext == ".apk":
		filePath = game.ApkPath
	case game.OBBPath != "" && ext == ".obb":
		filePath = game.OBBPath
	default:
		// Legacy fallback: dataDir/games/{hash}/{pkgName}/{fileName}
		// Used when (a) apk_path is empty (pre-9.10 game not yet
		// backfilled), or (b) the requested extension is neither
		// .apk nor .obb (e.g. .txt manifest — future-proofing for
		// game metadata side-files).
		filePath = filepath.Join(deps.Config.DataDir, "games", game.Hash, pkgName, fileName)
	}

	// Verify file exists and is a regular file (not directory, not symlink)
	info, err := os.Lstat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug().Str("file", filePath).Msg("file not found on disk")
			http.NotFound(w, r)
			return
		}
		log.Error().Err(err).Str("file", filePath).Msg("failed to stat file")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Reject symlinks for security
	if info.Mode()&os.ModeSymlink != 0 {
		log.Warn().Str("file", filePath).Msg("symlink rejected for file download")
		http.NotFound(w, r)
		return
	}

	// Reject directories masquerading as files
	if info.IsDir() {
		log.Debug().Str("file", filePath).Msg("path is a directory, not a file")
		http.NotFound(w, r)
		return
	}

	fileSize := info.Size()

	// Parse Range header
	rangeHeader := r.Header.Get("Range")
	var statusCode int
	contentStart := int64(0)
	contentEnd := fileSize - 1

	if rangeHeader == "" {
		// Full download
		statusCode = http.StatusOK
	} else {
		// Partial download with Range support
		var valid, unsatisfiable bool
		contentStart, contentEnd, valid, unsatisfiable = parseRangeHeader(rangeHeader, fileSize)
		if !valid {
			http.Error(w, "Invalid Range header", http.StatusBadRequest)
			return
		}
		if unsatisfiable {
			// RFC 7233 §4.4: 416 with Content-Range: */<fileSize>
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
			http.Error(w, "Range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		statusCode = http.StatusPartialContent
	}

	// Set response headers
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", contentEnd-contentStart+1))
	w.Header().Set("Content-Type", detectContentType(fileName))
	w.Header().Set("Content-Disposition", encodeContentDispositionFilename(fileName))

	if statusCode == http.StatusPartialContent {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", contentStart, contentEnd, fileSize))
	}

	w.WriteHeader(statusCode)

	// Open file and seek to start position
	f, err := os.Open(filePath)
	if err != nil {
		log.Error().Err(err).Str("file", filePath).Msg("failed to open file for serving")
		return
	}
	defer f.Close()

	if contentStart > 0 {
		_, err = f.Seek(contentStart, io.SeekStart)
		if err != nil {
			log.Error().Err(err).Str("file", filePath).Msg("failed to seek in file")
			return
		}
	}

	// Stream partial or full content — never buffer entire file in memory
	bytesToRead := contentEnd - contentStart + 1
	if _, err := io.CopyN(w, f, bytesToRead); err != nil {
		f.Close()
		log.Warn().Err(err).Str("file", filePath).Msg("error streaming file to client")
		return
	}

	// Story 7.5 T2: record the download. We only count full HTTP 200
	// downloads (NOT 206 partial, NOT 4xx/5xx). The 5xx / 4xx branches
	// above have already returned; 206 returns from the io.CopyN success
	// path because statusCode is set to 206 when a Range was requested.
	//
	// Async fire-and-forget: the increment is performed in a goroutine
	// so a slow DB write cannot delay the response. The trade-off is
	// that a crash between scheduling the goroutine and the SQLite
	// commit loses that one increment — acceptable for non-critical
	// usage statistics. The increment targets game.Hash; the
	// IncrementDownloadStats method silently no-ops on an unknown hash
	// (defense for "game deleted between GetGameByHash and now").
	if statusCode == http.StatusOK && deps.StatsDB != nil && game.Hash != "" {
		hash := game.Hash
		bytesServed := bytesToRead
		go func() {
			if incErr := deps.StatsDB.IncrementDownloadStats(hash, bytesServed); incErr != nil {
				log.Warn().Err(incErr).Str("hash", hash).Msg("stats: increment failed")
			}
		}()
	}
}

// ClientConfigResponse is the JSON returned by GET /config.json.
type ClientConfigResponse struct {
	BaseURI  string `json:"baseUri"`
	Password string `json:"password"`
}

// HandleClientConfigGET serves the public client configuration JSON.
// This endpoint is intentionally unauthenticated so VRHub clients can
// auto-configure by fetching this URL.
//
// The "password" field is the Base64-encoded archive password. The
// client decodes it (Android Base64.decode with NO_WRAP) before
// passing it to the 7z extractor as a char[] — see
// com.vrhub.data.MainRepository.decodeBase64Password. If we sent the
// cleartext password here, the Base64 decode would throw on any
// cleartext that isn't valid Base64 (e.g. "test12345678" has
// charset+length issues) and the client would fall back to a null
// password, which then fails to extract the AES-256-encrypted
// meta.7z with "invalid password".
//
// This is independent of the HTTP-level password check that used to
// live on /meta.7z: the HTTP check was removed (the archive itself
// is the access control); the Base64-on-the-wire encoding is a
// client-side contract that's been there since the very first
// VRHub release.
func (h *PublicAPIHandler) HandleClientConfigGET(w http.ResponseWriter, r *http.Request) {
	if h.Config == nil {
		writeError(w, http.StatusServiceUnavailable, "Server not configured", "NOT_CONFIGURED")
		return
	}
	cfg := h.Config
	host := cfg.Server.Host
	port := cfg.Server.Port
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = getOutboundIP()
	}
	baseURI := fmt.Sprintf("http://%s:%d/", host, port)
	// Encode the archive password to Base64 (standard alphabet, no
	// line wrapping) so the client can round-trip it through
	// android.util.Base64.decode(NO_WRAP). The cleartext
	// `cfg.Admin.ArchivePassword` is what the server uses to
	// encrypt the archive; the wire form is just a transport
	// encoding.
	resp := ClientConfigResponse{
		BaseURI:  baseURI,
		Password: base64.StdEncoding.EncodeToString([]byte(cfg.Admin.ArchivePassword)),
	}

	// Story 9.9 (B9 fix): same cache-headers pattern as /meta.7z so
	// the client can detect config.json changes (e.g. when the
	// operator changes the archive password) without having to clear
	// its app data. Last-Modified is anchored to the handler's
	// inception time (server-start sentinel). Any admin-driven
	// change to the password/host/port currently requires a process
	// restart, so the sentinel flips on restart and the client's
	// IMS check correctly invalidates its cache. If a future change
	// makes the config hot-reloadable, this should be re-anchored
	// to config.toml mtime via the ConfigPropagator closure.
	etag := computeConfigETag(cfg)
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", h.inceptionTime.Format(http.TimeFormat))
	w.Header().Set("Cache-Control", "no-cache")

	// Use the same matchesClientCache helper as /meta.7z: INM (with
	// weak-prefix support) takes precedence; on empty/whitespace INM
	// we fall through to IMS. Either matching → 304.
	if matchesClientCache(r, etag, h.inceptionTime) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// MountPublicRoutes mounts public API routes with setup mode protection.
//
// Story 7.5: gameDB also satisfies the StatsRecorder interface
// (IncrementDownloadStats method on *db.DB, see internal/db/stats.go).
// We pass it as the StatsDB sink so per-game download counters are
// incremented on every HTTP 200 file download.
//
// Story 9.1 (B1): MountPublicRoutes now returns the *PublicAPIHandler it
// created so the caller (SetupRouter) can wire a ConfigPropagator closure
// that refreshes h.Config after the setup→normal transition. Without this,
// PublicAPIHandler.Config stays nil at startup (cfg was nil in setup mode)
// and GET /meta.7z returns 500 "admin password hash not configured".
//
// Note on the 3 file-server routes: the canonical VRHub client
// uses /{hash}/ (trailing slash) to fetch the package listing and
// /{hash}/{packageName}/{filename} to download the file. The
// previous single-route version `r.Get("/{hash}/{path:.*}", h)`
// silently failed to match the trailing-slash variant under
// chi v5.6 — the test setup registered all three forms (see
// TestFileServerHandler_PackageListing_ReturnsHTML), but the
// production wiring only had one. This caused a 404 for every
// game list request from the client. We register all three forms
// explicitly here so the production behaviour matches the test.
//
// IMPORTANT: the catch-all route uses `/*` (bare asterisk), NOT
// `{path:*}`. In chi v5 `{path:*}` is parsed as a regular
// parameter whose literal key is `path:*`; it does NOT cross
// `/` boundaries. The correct catch-all syntax is `/*` and the
// parameter name is `*` (see chi/mux_test.go). Using `/{hash}/*`
// lets the handler receive the full remainder of the path,
// including slashes, so it can dispatch to package listings or
// file downloads based on path depth. Using any `{name:*}` or
// `{name:.*}` form would truncate at the first `/` and cause
// 404 for every download URL. This was a real production bug.
func MountPublicRoutes(r *chi.Mux, modeVal *atomic.Value, gameDB *db.DB, cfg *types.Config) *PublicAPIHandler {
	h := NewPublicAPIHandler(modeVal)
	h.DB = gameDB
	h.FileDB = gameDB
	h.Config = cfg
	h.StatsDB = gameDB

	// HEAD support: the VRHub client issues HEAD requests to discover
	// file sizes before downloading (Content-Length). Chi does not
	// register HEAD automatically for GET routes, so we use the
	// GetHead middleware which intercepts HEAD, runs the matching
	// GET handler, and strips the response body.
	r.Use(middleware.GetHead)

	r.Get("/config.json", h.HandleClientConfigGET)
	r.Get("/meta.7z", h.Meta7zHandler)
	// Three forms of the file-server route, mirroring the test
	// fixtures. Each form funnels into the same handler; the
	// handler itself dispatches between package-listing and
	// file-download based on the path depth (see
	// fileServerHandlerWithDeps).
	r.Get("/{hash}", h.FileServerHandler)
	r.Get("/{hash}/", h.FileServerHandler)
	r.Get("/{hash}/*", h.FileServerHandler)
	return h
}
