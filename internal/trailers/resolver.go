// Package trailers resolves a streaming trailer URL (a YouTube watch link)
// for a game WITHOUT hosting any video bytes. Story 11.1.
//
// The resolution is a graceful cascade, highest priority first:
//
//  1. Operator override — a sidecar file next to the APK named
//     "{releaseName}.trailer" (or a generic "trailer.url" in the same
//     directory). Always wins; guarantees the feature works end-to-end.
//  2. Metadata lookup (best-effort) — the cached MetaMetadata oculusdb JSON
//     for the package. RESEARCH (Story 11.1, 2026-06-23): across all 15,162
//     oculusdb files there is NO structured video/trailer field; YouTube
//     URLs only appear embedded in free-text marketing descriptions. So this
//     step logs at Debug and falls through. We do NOT scrape the description
//     (too unreliable — would surface unrelated "let's play" videos).
//  3. YouTube Data API (optional) — only when cfg.Trailer.YouTubeAPIKey is
//     set. Searches "{gameName} trailer" with relevanceLanguage set to
//     cfg.Trailer.Language and takes the first video result.
//
// The resolved URL is meant to be cached in the DB (games.trailer_url) and
// only re-resolved when empty or via an explicit refresh.
package trailers

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// genericOverrideFileName is the package-agnostic operator override file an
// operator can drop into a game directory when they don't want to name it
// per-release.
const genericOverrideFileName = "trailer.url"

// YouTubeSearcher abstracts the YouTube Data API search so the resolver can
// be unit-tested without a network call or a real API key. Resolve calls it
// only when cfg.Trailer.YouTubeAPIKey is non-empty.
type YouTubeSearcher interface {
	// SearchTrailer returns a watch URL for the first video matching
	// "{query} trailer" in the given relevanceLanguage, or "" when nothing
	// is found. Implementations must respect ctx cancellation.
	SearchTrailer(ctx context.Context, apiKey, query, language string) (string, error)
}

// Resolver runs the trailer resolution cascade.
type Resolver struct {
	// metadataDir is "{dataDir}/metadata" — the root of the MetaMetadata
	// cache. Used to locate the oculusdb JSON for the best-effort step.
	metadataDir string
	// youtube is the optional YouTube Data API client. nil disables step 3
	// even when an API key is configured (used by tests / when the key path
	// is not wired). The default New() wires the real HTTP client.
	youtube YouTubeSearcher
}

// New creates a Resolver. metadataDir is "{dataDir}/metadata". The real
// YouTube HTTP client is wired in; pass a custom one via NewWithSearcher for
// tests.
func New(metadataDir string) *Resolver {
	return &Resolver{
		metadataDir: metadataDir,
		youtube:     &httpYouTubeSearcher{},
	}
}

// NewWithSearcher creates a Resolver with an injected YouTubeSearcher (tests).
func NewWithSearcher(metadataDir string, yt YouTubeSearcher) *Resolver {
	return &Resolver{metadataDir: metadataDir, youtube: yt}
}

// Resolve runs the cascade for one game and returns the resolved trailer URL
// (trimmed) or "" when nothing is found. A "" result with a nil error is the
// normal graceful-degradation outcome (AC5) — the caller leaves trailer_url
// empty and adds nothing to meta.7z / the listing.
//
// Resolve never returns a hard error for "not found": every step that fails
// logs at Debug and falls through. A non-nil error is reserved for a real,
// unexpected fault (e.g. a malformed YouTube API response the caller may want
// to surface), and callers may still treat it as best-effort.
func (r *Resolver) Resolve(ctx context.Context, game types.GameEntry, cfg *types.Config) (string, error) {
	// Step 1: operator override (always wins).
	if url := r.resolveOverride(game); url != "" {
		vlog.Get().Debug().
			Str("release", game.ReleaseName).
			Str("source", "override").
			Msg("trailer: resolved from operator sidecar")
		return url, nil
	}

	// Step 2: oculusdb best-effort. RESEARCH-confirmed there is no usable
	// structured field — this is a logged no-op that exists so a future
	// MetaMetadata schema change can be slotted in here.
	if url := r.resolveFromOculusDB(game); url != "" {
		vlog.Get().Debug().
			Str("release", game.ReleaseName).
			Str("source", "oculusdb").
			Msg("trailer: resolved from oculusdb metadata")
		return url, nil
	}

	// Step 3: YouTube Data API (only when a key is configured).
	if cfg != nil && cfg.Trailer.YouTubeAPIKey != "" && r.youtube != nil {
		query := game.GameName
		if query == "" {
			query = game.ReleaseName
		}
		if query != "" {
			url, err := r.youtube.SearchTrailer(ctx, cfg.Trailer.YouTubeAPIKey, query, cfg.Trailer.Language)
			if err != nil {
				vlog.Get().Debug().Err(err).
					Str("release", game.ReleaseName).
					Msg("trailer: YouTube search failed, leaving trailer empty")
				return "", err
			}
			if url != "" {
				vlog.Get().Debug().
					Str("release", game.ReleaseName).
					Str("source", "youtube").
					Msg("trailer: resolved from YouTube Data API")
				return url, nil
			}
		}
	}

	// AC5: nothing found — graceful empty result, no error.
	vlog.Get().Debug().
		Str("release", game.ReleaseName).
		Msg("trailer: no source resolved a URL (override absent, no oculusdb field, no API key/result)")
	return "", nil
}

// resolveOverride reads the operator sidecar next to the APK. It checks
// "{releaseName}.trailer" first, then the generic "trailer.url". Returns the
// trimmed first non-empty line, or "" when no override exists.
//
// The directory is derived from game.ApkPath (the absolute path the scanner
// recorded). When ApkPath is empty (legacy game not yet backfilled) there is
// nothing to look next to, so we return "".
func (r *Resolver) resolveOverride(game types.GameEntry) string {
	if game.ApkPath == "" {
		return ""
	}
	dir := filepath.Dir(game.ApkPath)
	candidates := []string{
		filepath.Join(dir, game.ReleaseName+".trailer"),
		filepath.Join(dir, genericOverrideFileName),
	}
	for _, path := range candidates {
		if game.ReleaseName == "" && strings.HasSuffix(path, ".trailer") {
			// Can't build a per-release name without a release name.
			continue
		}
		if url := readTrailerFile(path); url != "" {
			return url
		}
	}
	return ""
}

// ReadOverrideForDir is the directory-scoped override lookup used by the
// scanner at import time (Task 4): given the directory that contains the APK
// and the game's release name, it returns the override URL or "".
//
// Exposed (vs. resolveOverride which needs a full GameEntry) so the importer
// can call it the moment it has the APK path + release name, before the row
// exists in the DB.
func ReadOverrideForDir(dir, releaseName string) string {
	if dir == "" {
		return ""
	}
	if releaseName != "" {
		if url := readTrailerFile(filepath.Join(dir, releaseName+".trailer")); url != "" {
			return url
		}
	}
	return readTrailerFile(filepath.Join(dir, genericOverrideFileName))
}

// readTrailerFile reads a sidecar override file and returns the trimmed URL.
// The file is expected to contain a single URL; we take the first non-empty
// line (trimmed) so a trailing newline or a stray blank line is tolerated.
// Missing/unreadable/empty files yield "".
func readTrailerFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
