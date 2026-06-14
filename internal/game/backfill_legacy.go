package game

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// GameDB is the minimum DB surface area needed by
// BackfillLegacyApkPaths. The production *db.DB satisfies this
// interface (and the existing concrete UpdateApkAndOBBPath method).
// Tests can inject a fake by implementing the same method set.
type GameDB interface {
	ListGames(exposed *bool) ([]types.GameEntry, error)
	// UpdateApkAndOBBPath is defined on *db.DB. The interface
	// here is satisfied transitively by *db.DB's concrete
	// implementation. Tests use a *db.DB instance.
	UpdateApkAndOBBPath(ctx context.Context, packageName, apkPath, obbPath string) error
}

// BackfillLegacyApkPaths walks every game in the DB that has an
// empty apk_path (pre-9.10 migration) and tries to resolve the
// file by scanning the legacy dataDir/games/{hash}/{pkgName}/
// directory.
//
// Story 9.10 post-merge bugfix: the original implementation
// assumed the legacy APK was named {pkgName}.apk. Real legacy
// files (copied by the operator before Story 9.10) follow the
// {Label}__v{VersionCode}_{pkgName}.apk convention from
// Story 9.4 (B4) — e.g. AFGirlfriend__18___v1_com.NekumaSoft.AFGirlfriend.apk.
// The hardcoded {pkgName}.apk resolution returned not-exist for
// every legacy game, leaving apk_path empty even after the
// startup scan ran. The fix scans the directory instead and
// picks the first .apk (and first .obb) present.
//
// If the APK file exists at the legacy location, the row is
// updated with the absolute path. OBBPath is also populated if a
// matching OBB is found next to the APK. Otherwise the game is
// left untouched — the startup scan (ScanAndImportMultiple) will
// eventually find it at its real game_folders location and
// populate the column from there, or mark the game as unexposed
// if the file is gone.
//
// Returns the number of rows updated. Errors are non-fatal: a
// per-row failure is logged at the caller (this function stays
// free of logger dependencies for testability) and the loop
// continues — the story's "errors must not block the boot" rule
// is enforced by the caller (cmd/server/main.go) which logs at
// Warn level and continues.
//
// This is a one-shot migration: once all games have a non-empty
// apk_path, subsequent calls become a cheap no-op (the
// "skip if already set" branch). Operators upgrading from < 9.10
// will run this exactly once.
//
// Story 9.10 T4 (Subtask 4.2) + 9.10-post bugfix.
func BackfillLegacyApkPaths(ctx context.Context, db GameDB, dataDir string) (int, error) {
	if dataDir == "" {
		return 0, errors.New("backfill legacy apk paths: dataDir is empty")
	}
	if db == nil {
		return 0, errors.New("backfill legacy apk paths: db is nil")
	}

	// List all games (exposed + unexposed — the unexposed ones
	// also need their apk_path set so the admin UI can offer a
	// re-import path later).
	allGames, err := db.ListGames(nil)
	if err != nil {
		return 0, fmt.Errorf("backfill: list games: %w", err)
	}

	updated := 0
	for _, g := range allGames {
		// Skip games that already have an apk_path — the scanner
		// (or a previous backfill) populated it. This is the
		// idempotent guard that makes it safe to run on every
		// startup after the first migration.
		if g.ApkPath != "" {
			continue
		}

		// Cancellation: stop on context expiry.
		select {
		case <-ctx.Done():
			return updated, ctx.Err()
		default:
		}

		// Scan the legacy directory for an APK. The legacy
		// layout is dataDir/games/{hash}/{pkgName}/ and may
		// contain one APK (and optionally one OBB) with any
		// filename — operators' manual copies from
		// pre-Story 9.10 used {Label}__v{ver}_{pkgName}.apk
		// (Story 9.4 / B4 convention) but we don't pin that
		// naming here. We just take the first .apk present
		// (and verify the package name appears in the
		// filename as a sanity check to avoid mis-attributing
		// a stray file from a different game).
		legacyDir := filepath.Join(dataDir, "games", g.Hash, g.PackageName)
		entries, scanErr := ScanDirectory(legacyDir)
		if scanErr != nil {
			// Directory missing — the most common case for
			// pre-9.10 games whose files were never manually
			// copied (e.g. AkiBonbon). Leave the game alone:
			// the startup scan (phase 1) will discover the
			// file at its real game_folders location, or
			// mark the game as unexposed if it's gone. We
			// treat missing-dir as a clean no-op, not an
			// error, to keep the boot-time contract "errors
			// must not block the boot" honest.
			if errors.Is(scanErr, os.ErrNotExist) {
				continue
			}
			// Other errors (permission denied, I/O) are
			// surfaceable but also non-fatal: log and stop
			// the loop. The caller logs at Warn level.
			return updated, fmt.Errorf("backfill: scan %q: %w", legacyDir, scanErr)
		}

		// Pick the first .apk whose name contains the package
		// name (case-insensitive). The package name is part of
		// the legacy filename convention but we keep the
		// check loose so that variations on the operator's
		// side don't cause us to skip a valid file.
		apkPath := ""
		pkgLower := strings.ToLower(g.PackageName)
		for _, f := range entries {
			if !f.IsAPK {
				continue
			}
			if apkPath == "" && strings.Contains(strings.ToLower(f.Name), pkgLower) {
				apkPath = f.Path
			}
		}
		// Fallback: if no APK contains the package name in
		// its filename, take the first .apk we see (the
		// legacy directory is per-package, so anything in
		// there belongs to this game).
		if apkPath == "" {
			for _, f := range entries {
				if f.IsAPK {
					apkPath = f.Path
					break
				}
			}
		}
		if apkPath == "" {
			// No APK in the legacy dir — leave apk_path
			// empty. The startup scan will discover the
			// file at its real game_folders location (or
			// mark the game as unexposed).
			continue
		}

		// Look for a matching OBB in the same directory. We
		// don't have version code metadata at backfill time,
		// so any .obb in the directory is a candidate. Take
		// the first one (legacy layout is per-package).
		obbPath := ""
		if g.OBBSizeBytes > 0 {
			for _, f := range entries {
				if !f.IsAPK && IsOBBFile(f.Name) {
					obbPath = f.Path
					break
				}
			}
		}

		if err := db.UpdateApkAndOBBPath(ctx, g.PackageName, apkPath, obbPath); err != nil {
			return updated, fmt.Errorf("backfill: update %q: %w", g.PackageName, err)
		}
		updated++
	}

	return updated, nil
}
