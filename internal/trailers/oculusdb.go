package trailers

import (
	"encoding/json"
	"os"
	"path/filepath"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// oculusDBTrailerJSON holds the only fields we would read from a MetaMetadata
// data/oculusdb/{pkg}.json file for trailer resolution.
//
// RESEARCH (Story 11.1, 2026-06-23, against a live 15,162-file oculusdb
// cache): there is NO structured video/trailer field anywhere in the union of
// top-level keys. The closest candidates that DO exist are speculative and
// kept here only so a future MetaMetadata schema change can be picked up
// without restructuring the resolver. They are all "" in today's data, so
// resolveFromOculusDB returns "" for every real package today.
//
// We deliberately do NOT parse YouTube links out of display_long_description:
// only ~278 of 15,162 descriptions mention youtube at all, and those links are
// marketing copy / "let's play" videos rather than canonical trailers —
// surfacing them would be worse than showing nothing. The operator-override
// sidecar (step 1) is the guaranteed end-to-end path.
type oculusDBTrailerJSON struct {
	// Speculative fields — none present in current MetaMetadata data. If a
	// future schema adds one of these, resolveFromOculusDB will start
	// returning it with zero further code changes.
	TrailerURL string `json:"trailer_url"`
	VideoURL   string `json:"video_url"`
	Trailer    string `json:"trailer"`
	Video      string `json:"video"`
}

// resolveFromOculusDB inspects the cached MetaMetadata oculusdb JSON for the
// game's package and returns a trailer URL if a usable structured field is
// present. Per the research above, current data has no such field, so this is
// effectively a logged no-op that falls through (AC5). It NEVER returns an
// error: a missing/unreadable/field-less JSON simply yields "".
//
// MetaMetadata uses both "{pkg}.json" and ".{pkg}.json" naming conventions
// (mirrors internal/metadata/fetcher.go notes generation). We try both.
func (r *Resolver) resolveFromOculusDB(game types.GameEntry) string {
	if r.metadataDir == "" || game.PackageName == "" {
		return ""
	}

	base := findOculusDBDir(r.metadataDir)
	if base == "" {
		vlog.Get().Debug().
			Str("release", game.ReleaseName).
			Msg("trailer: no oculusdb cache directory found, skipping metadata step")
		return ""
	}

	pkg := game.PackageName
	var raw []byte
	if d, err := os.ReadFile(filepath.Join(base, pkg+".json")); err == nil {
		raw = d
	} else if d, err := os.ReadFile(filepath.Join(base, "."+pkg+".json")); err == nil {
		raw = d
	} else {
		vlog.Get().Debug().
			Str("release", game.ReleaseName).
			Str("package", pkg).
			Msg("trailer: no oculusdb JSON for package, skipping metadata step")
		return ""
	}

	var odb oculusDBTrailerJSON
	if err := json.Unmarshal(raw, &odb); err != nil {
		vlog.Get().Debug().Err(err).
			Str("release", game.ReleaseName).
			Str("package", pkg).
			Msg("trailer: oculusdb JSON unparseable, skipping metadata step")
		return ""
	}

	// Return the first non-empty candidate. All "" in today's data → falls
	// through to the YouTube step / graceful empty.
	for _, candidate := range []string{odb.TrailerURL, odb.VideoURL, odb.Trailer, odb.Video} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

// findOculusDBDir locates the data/oculusdb directory inside the metadata
// cache. The cache may be laid out either directly under metadataDir (when the
// tarball is extracted in place) or one level down inside a single top-level
// directory such as "MetaMetadata-main" (the GitHub archive layout). Mirrors
// findCommonDataDir in internal/metadata/fetcher.go. Returns "" when no
// oculusdb directory exists.
func findOculusDBDir(metadataDir string) string {
	direct := filepath.Join(metadataDir, "data", "oculusdb")
	if info, err := os.Stat(direct); err == nil && info.IsDir() {
		return direct
	}
	entries, err := os.ReadDir(metadataDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "" || e.Name()[0] == '.' {
			continue
		}
		nested := filepath.Join(metadataDir, e.Name(), "data", "oculusdb")
		if info, err := os.Stat(nested); err == nil && info.IsDir() {
			return nested
		}
	}
	return ""
}
