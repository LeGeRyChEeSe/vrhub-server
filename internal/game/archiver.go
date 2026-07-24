package game

// Split-7z game archive generation hooks (Issue #1).
//
// These methods tie the pure archive generator (internal/archive) to the game
// library: they decide WHICH games need an archive and gather the inputs
// (APK + OBB paths) from the DB row. The actual 7z work lives in
// internal/archive.GenerateGameArchive.
//
// Archives are derived artifacts under {dataDir}/games/{hash}/. They are
// generated lazily — at startup (SweepMissingArchives) and after each import
// (EnsureArchiveForPackage) — never on the hot request path. Until a game's
// archive exists the public API falls back to serving the raw APK/OBB, so the
// feature degrades gracefully while a large library is still being packed.

import (
	"context"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/archive"
	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// gameOBBPaths returns the OBB files to bundle for a game. The importer records
// a single primary OBB path (multi-OBB is out of scope per importer.go); this
// helper centralises that so a future multi-OBB change has one place to grow.
func gameOBBPaths(g types.GameEntry) []string {
	if g.OBBPath != "" {
		return []string{g.OBBPath}
	}
	return nil
}

// gameArchiveSpec builds the archive spec for a game, filling the hash from the
// package name when the row didn't carry one.
func (gm *GameManager) gameArchiveSpec(g types.GameEntry, password, splitSize string) archive.GameArchiveSpec {
	hash := g.Hash
	if hash == "" {
		hash = vrpHash(g.PackageName)
	}
	return archive.GameArchiveSpec{
		DataDir:     gm.dataDir,
		Hash:        hash,
		ReleaseName: g.ReleaseName,
		PackageName: g.PackageName,
		APKPath:     g.ApkPath,
		OBBPaths:    gameOBBPaths(g),
		VersionCode: g.VersionCode,
		Password:    password,
		SplitSize:   splitSize,
	}
}

// archivable reports whether a game is eligible for a split-7z archive: it must
// be exposed, not corrupted, and have a known APK path on disk.
func archivable(g types.GameEntry) bool {
	return g.Exposed && !g.Corrupted && g.ApkPath != "" && g.ReleaseName != "" && g.PackageName != ""
}

// EnsureArchive generates the split-7z archive for one game when it is missing
// or stale (password or version-code change). It is a no-op for non-archivable
// games, when no password is set, or when the archive is already up to date.
// Returns true when it actually (re)generated the archive.
func (gm *GameManager) EnsureArchive(ctx context.Context, g types.GameEntry, password, splitSize string) (bool, error) {
	if password == "" || !archivable(g) {
		return false, nil
	}
	spec := gm.gameArchiveSpec(g, password, splitSize)
	if archive.GameArchiveUpToDate(spec) {
		return false, nil
	}
	if err := archive.GenerateGameArchive(ctx, spec); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureArchiveForPackage looks up a game by package name and ensures its
// archive exists/up-to-date. Used by the watcher import handler so a freshly
// dropped game is packed shortly after it is imported.
func (gm *GameManager) EnsureArchiveForPackage(ctx context.Context, packageName, password, splitSize string) error {
	g, err := gm.database.GetGameByPackage(packageName)
	if err != nil || g == nil {
		return err
	}
	_, genErr := gm.EnsureArchive(ctx, *g, password, splitSize)
	return genErr
}

// SweepMissingArchives generates archives for every archivable game that does
// not yet have an up-to-date one. Best-effort and cancellable; intended to run
// in a background goroutine at startup. Logs a summary.
func (gm *GameManager) SweepMissingArchives(ctx context.Context, password, splitSize string) {
	if password == "" {
		vlog.Get().Warn().Msg("archive sweep: archive password not configured, skipping game archive generation")
		return
	}
	games, err := gm.database.ListAllGamesOrderedByName()
	if err != nil {
		vlog.Get().Warn().Err(err).Msg("archive sweep: failed to list games")
		return
	}

	var generated, upToDate, skipped, failed int
	for _, g := range games {
		select {
		case <-ctx.Done():
			vlog.Get().Info().Int("generated", generated).Msg("archive sweep: cancelled")
			return
		default:
		}
		if !archivable(g) {
			skipped++
			continue
		}
		didGen, genErr := gm.EnsureArchive(ctx, g, password, splitSize)
		switch {
		case genErr != nil:
			failed++
			vlog.Get().Warn().Err(genErr).Str("package", g.PackageName).Msg("archive sweep: generation failed")
		case didGen:
			generated++
		default:
			upToDate++
		}
	}

	vlog.Get().Info().
		Int("generated", generated).
		Int("up_to_date", upToDate).
		Int("skipped", skipped).
		Int("failed", failed).
		Int("total", len(games)).
		Msg("archive sweep: complete")
}
