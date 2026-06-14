package game

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// GameManager handles game import and deletion operations.
type GameManager struct {
	database *db.DB
	dataDir  string
	mu       sync.Map // per-package lock for concurrent rescan protection
}

// NewGameManager creates a new GameManager instance.
func NewGameManager(database *db.DB, dataDir string) *GameManager {
	return &GameManager{
		database: database,
		dataDir:  dataDir,
		mu:       sync.Map{},
	}
}

// acquirePackageLock acquires a per-package lock to prevent concurrent rescan races.
// Returns a release function on success; returns ctx.Err() if the context is cancelled
// while waiting for the lock (C-07/C-12). The caller MUST call the release function on success.
func (gm *GameManager) acquirePackageLock(ctx context.Context, packageName string) (func(), error) {
	lock, _ := gm.mu.LoadOrStore(packageName, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	// Try-lock loop with ctx cancellation (C-07/C-12): avoid blocking the goroutine
	// indefinitely if shutdown cancels the context while waiting.
	for {
		if mu.TryLock() {
			return func() { mu.Unlock() }, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// ImportAPK imports a game from an APK file.
func (gm *GameManager) ImportAPK(filePath string) error {
	pkgName := ExtractPackageNameFromPath(filePath)
	if pkgName == "" {
		pkgName = "unknown"
	}

	// TOCTOU Fix (Round 14): Stat the file first to get metadata, then validate
	// This reduces the race window by combining stat and validate closer together
	info, infoErr := os.Stat(filePath)
	if infoErr != nil {
		return fmt.Errorf("stat apk %q: %w", filePath, infoErr)
	}

	// Validate APK integrity
	apkResult := ValidateAPK(filePath)
	if apkResult.Corrupted {
		vlog.Get().Warn().Str("file", filePath).Str("reason", apkResult.CorruptionReason).Msg("corrupted APK detected")

		// Try to extract package name from filename as fallback
		fallbackName := ExtractPackageNameFromPath(filePath)
		if fallbackName == "" {
			return fmt.Errorf("validate apk %q: %s", filePath, apkResult.CorruptionReason)
		}

		// Fix #16 (Round 11): Acquire per-package lock even for corrupted imports to prevent race conditions
		unlock, err := gm.acquirePackageLock(context.Background(), fallbackName)
		if err != nil {
			return fmt.Errorf("acquire package lock for %q: %w", fallbackName, err)
		}
		defer unlock()

		// Fix #16 (Round 11): Check if a valid version of this game already exists
		// If so, don't overwrite it with corrupted metadata - just update corruption status
		existingGame, err := gm.database.GetGameByPackage(fallbackName)
		if err == nil && existingGame != nil && !existingGame.Corrupted {
			// Valid version exists - update corruption status instead of overwriting metadata
			vlog.Get().Warn().Str("package", fallbackName).Str("reason", apkResult.CorruptionReason).Msg("corrupted version detected but valid version exists, updating corruption status")
			if updateErr := gm.database.UpdateCorruptionStatus(fallbackName, true, apkResult.CorruptionReason); updateErr != nil {
				vlog.Get().Error().Err(updateErr).Str("package", fallbackName).Msg("failed to update corruption status for existing valid game")
			}
			// Fix #8 (Round 15): Mark game as unexposed since it's now corrupted
			if unexposeErr := gm.database.UpdateGameExposed(fallbackName, false); unexposeErr != nil {
				vlog.Get().Error().Err(unexposeErr).Str("package", fallbackName).Msg("failed to unexpose existing valid game upon importing corrupted version")
			}
			return nil
		}

		// Use file info obtained above (TOCTOU fix)
		sizeBytes := info.Size()

		// Fix #6 (Round 11): Use SHA-256 of file path for hash consistency with valid APK imports
		fileHash := fmt.Sprintf("%x", sha256.Sum256([]byte(filePath)))

		gameEntry := types.GameEntry{
			ReleaseName:      fallbackName,
			GameName:         "",
			PackageName:      fallbackName,
			VersionCode:      0,
			SizeBytes:        sizeBytes,
			Description:      "",
			IconURL:          "",
			ThumbnailURL:     "",
			LastUpdated:      time.Now(),
			Popularity:       0,
			Hash:             fileHash,
			Corrupted:        true,
			CorruptionReason: apkResult.CorruptionReason,
			Exposed:          false,
			OBBSizeBytes:     0,
			OBBPath:          "",
			// Story 9.10: record the absolute path of the corrupted APK
			// so the file server can attempt to serve it (and report
			// 404 cleanly if the file has since been moved/deleted).
			ApkPath: filePath,
		}

		if err := gm.database.InsertGame(gameEntry); err != nil {
			return fmt.Errorf("insert corrupted game %q: %w", fallbackName, err)
		}

		vlog.Get().Warn().Str("package", fallbackName).Msg("corrupted game stored in database")
		return nil
	}

	// Extract metadata from APK
	meta, err := ExtractAPKMetadata(filePath)
	if err != nil {
		return fmt.Errorf("extract apk metadata for %q: %w", filePath, err)
	}

	if meta.PackageName == "" {
		return fmt.Errorf("apk %q has no package name", filePath)
	}

	pkgName = meta.PackageName
	unlock, err := gm.acquirePackageLock(context.Background(), pkgName)
	if err != nil {
		return fmt.Errorf("acquire package lock for %q: %w", pkgName, err)
	}
	defer unlock()

	// Check if game already exists in DB (AC4 - duplicate handling)
	existing, err := gm.database.GetGameByPackage(meta.PackageName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			vlog.Get().Debug().Str("package", meta.PackageName).Msg("game not found in database, proceeding with new import")
		} else {
			return fmt.Errorf("check existing game %q: %w", meta.PackageName, err)
		}
	} else if existing != nil {
		vlog.Get().Warn().Str("release_name", meta.PackageName).Msg("duplicate game detected, refreshing metadata")

		// Re-extract metadata so we can fix a previously-empty game_name
		// (e.g. old import before C-16 used raw manifest without resource
		// resolution). Also refresh size and last_updated.
		freshMeta, metaErr := ExtractAPKMetadata(filePath)
		if metaErr != nil {
			vlog.Get().Warn().Err(metaErr).Str("file", filePath).Msg("failed to re-extract metadata during duplicate refresh")
		}

		now := time.Now()
		existing.SizeBytes = info.Size()

		tx, txErr := gm.database.BeginTx(context.Background(), nil)
		if txErr != nil {
			return fmt.Errorf("begin transaction for game update %q: %w", meta.PackageName, txErr)
		}

		// Update fields that may have changed or were wrong on first import.
		// game_name is updated if it was empty OR if we successfully
		// re-extracted a better name.
		newGameName := existing.GameName
		if existing.GameName == "" || (metaErr == nil && freshMeta.Label != "" && freshMeta.Label != existing.GameName) {
			newGameName = freshMeta.Label
		}

		updateQuery := `UPDATE games SET exposed = ?, last_updated = ?, size_bytes = ?, game_name = ?, apk_path = ? WHERE package_name = ?`
		// Story 9.10: refresh apk_path on duplicate detection — the
		// operator may have moved the file within game_folders between
		// scans, and we want the file server to find it at the new
		// location.
		if _, execErr := tx.Exec(updateQuery, true, now.Unix(), existing.SizeBytes, newGameName, filePath, meta.PackageName); execErr != nil {
			tx.Rollback()
			return fmt.Errorf("update game %q: %w", meta.PackageName, execErr)
		}

		if commitErr := tx.Commit(); commitErr != nil {
			return fmt.Errorf("commit transaction for game update %q: %w", meta.PackageName, commitErr)
		}

		return nil
	}

	// Find paired OBB files in the same directory (support multiple OBB: main + patch)
	dir := filepath.Dir(filePath)
	allFiles, scanErr := ScanDirectory(dir)
	var obbSize int64
	var obbCorrupted bool
	var obbReason string
	// Story 9.10: record the first valid OBB's absolute path so the
	// file server can serve it directly (no copy to dataDir/games/.../).
	// Multi-OBB is out of scope (one APK + 1 main OBB only); the
	// total size is still summed across all valid OBBs.
	var obbPath string
	if scanErr == nil {
		for _, f := range allFiles {
			if !f.IsAPK && IsOBBFile(f.Name) {
				vc, obbPkgName, ok := ExtractOBBPackageName(strings.ToLower(f.Name))
				if ok && obbPkgName == strings.ToLower(meta.PackageName) && vc == int64(meta.VersionCode) {
					obbSize += f.Size
					// Capture the path of the first valid (non-corrupted,
					// correctly named) OBB found — used by the file server.
					if obbPath == "" && f.Path != "" {
						obbPath = f.Path
					}

					// Validate OBB integrity (AC #3)
					obbResult := ValidateOBB(f.Path)
					if obbResult.Corrupted {
						obbCorrupted = true
						obbReason = fmt.Sprintf("OBB: %s", obbResult.CorruptionReason)
						vlog.Get().Warn().Str("package", meta.PackageName).Str("obb_path", f.Path).Str("reason", obbResult.CorruptionReason).Msg("corrupted OBB detected")
					} else if obbResult.CorruptionReason != "" {
						// Non-standard naming is a warning, not corruption (Option B from Dev Notes)
						if obbReason == "" {
							obbReason = fmt.Sprintf("OBB: %s", obbResult.CorruptionReason)
						}
						vlog.Get().Warn().Str("package", meta.PackageName).Str("obb_path", f.Path).Str("reason", obbResult.CorruptionReason).Msg("non-standard OBB naming detected")
					}
				}
			}
		}
	}

	// Get file size for APK (reuse info from TOCTOU fix above - info is guaranteed non-nil)
	sizeBytes := info.Size()

	// Determine corruption status from OBB validation
	corrupted := obbCorrupted
	corruptionReason := ""
	if obbCorrupted {
		corruptionReason = obbReason
	} else if obbReason != "" && !obbCorrupted {
		// Store warning reason without marking as corrupted
		corruptionReason = obbReason
	}

	// Compute SHA-256 hash of file path for uniqueness (Fix #6: avoid UNIQUE constraint failures)
	fileHash := fmt.Sprintf("%x", sha256.Sum256([]byte(filePath)))

	// Create game entry
	gameEntry := types.GameEntry{
		ReleaseName:      meta.PackageName,
		GameName:         meta.Label,
		PackageName:      meta.PackageName,
		VersionCode:      meta.VersionCode,
		SizeBytes:        sizeBytes,
		Description:      "",
		IconURL:          "",
		ThumbnailURL:     "",
		LastUpdated:      time.Now(),
		Popularity:       0,
		Hash:             fileHash,
		Corrupted:        corrupted,
		CorruptionReason: corruptionReason,
		Exposed:          !corrupted,
		OBBSizeBytes:     obbSize,
		OBBPath:          obbPath,
		// Story 9.10: record the absolute path of the APK as the
		// scanner found it (anywhere in game_folders). The file
		// server uses this path to serve the file directly,
		// removing the need for a staging copy to dataDir/games/.
		ApkPath: filePath,
	}

	if err := gm.database.InsertGame(gameEntry); err != nil {
		return fmt.Errorf("insert game %q: %w", meta.PackageName, err)
	}

	// Extract icon from APK and save to metadata/icons/ for the meta.7z archive.
	iconDir := filepath.Join(gm.dataDir, "metadata", "icons")
	iconPath := filepath.Join(iconDir, meta.PackageName+".png")
	if iconErr := ExtractAPKIcon(filePath, iconPath); iconErr != nil {
		// Non-fatal: the game is still usable without an icon.
		vlog.Get().Warn().Err(iconErr).Str("package", meta.PackageName).Msg("failed to extract icon from APK")
	} else {
		vlog.Get().Debug().Str("package", meta.PackageName).Str("path", iconPath).Msg("icon extracted from APK")
	}

	return nil
}

// DeleteGameByPackage deletes a game from the database by package name.
func (gm *GameManager) DeleteGameByPackage(packageName string) error {
	if err := gm.database.DeleteGame(packageName); err != nil {
		return fmt.Errorf("delete game %q: %w", packageName, err)
	}
	// Fix #5 (Round 11): Do NOT clean up per-package mutex from sync.Map.
	// Deleting the mutex while other goroutines might be waiting on it creates a race condition
	// where a new mutex is created and acquired before the old one is released.
	// The memory cost of keeping stale mutex entries is negligible compared to the race risk.
	return nil
}

// GetGameByPackage retrieves a game by its package name.
func (gm *GameManager) GetGameByPackage(packageName string) (*types.GameEntry, error) {
	return gm.database.GetGameByPackage(packageName)
}

// UpdateGameExposed updates the exposed status of a game by package name.
func (gm *GameManager) UpdateGameExposed(packageName string, exposed bool) error {
	if err := gm.database.UpdateGameExposed(packageName, exposed); err != nil {
		return fmt.Errorf("update game exposed for %q: %w", packageName, err)
	}
	return nil
}

// DeleteGame deletes a game by package name (alias for GameDeleter interface).
func (gm *GameManager) DeleteGame(packageName string) error {
	if err := gm.database.DeleteGame(packageName); err != nil {
		return fmt.Errorf("delete game %q: %w", packageName, err)
	}
	// Fix #5 (Round 11): Do NOT clean up per-package mutex from sync.Map (same reason as DeleteGameByPackage)
	return nil
}

// GetExistingGames returns all package names currently in the database.
func (gm *GameManager) GetExistingGames() ([]string, error) {
	games, err := gm.database.ListAllGamesOrderedByName()
	if err != nil {
		return nil, fmt.Errorf("list games: %w", err)
	}

	packages := make([]string, 0, len(games))
	for _, g := range games {
		packages = append(packages, g.PackageName)
	}
	return packages, nil
}

// RevalidateGame re-validates an existing game's files and updates corruption status.
// Returns true if the caller should proceed with import (new valid game), false otherwise.
func (gm *GameManager) RevalidateGame(ctx context.Context, filePath, packageName string) (bool, error) {
	// Fix #9 (Round 10): Acquire per-package lock to prevent race conditions with ImportAPK
	unlock, err := gm.acquirePackageLock(ctx, packageName)
	if err != nil {
		return false, fmt.Errorf("acquire package lock for %q: %w", packageName, err)
	}
	defer unlock()

	// Get existing game from DB
	existing, err := gm.database.GetGameByPackage(packageName)
	if err != nil {
		return false, fmt.Errorf("get existing game %q: %w", packageName, err)
	}

	// Check file mtime against last_updated
	info, statErr := os.Stat(filePath)
	if statErr != nil {
		return false, fmt.Errorf("stat file %q: %w", filePath, statErr)
	}

	fileMtimeSec := info.ModTime().Unix()
	lastUpdatedSec := existing.LastUpdated.Unix()

	// Compare Unix seconds to account for filesystem mtime granularity (Fix #2)
	if fileMtimeSec != lastUpdatedSec {
		vlog.Get().Info().Str("package", packageName).Str("file", filePath).Msg("game file changed, re-validating")

		// Re-run APK validation
		apkResult := ValidateAPK(filePath)

		var newCorrupted bool
		var newReason string
		var meta APKMetadata
		var metaErr error
		var obbSize int64
		// Story 9.10: declared at function scope (above the if/else
		// branches) so the later UPDATE queries can reference it
		// regardless of which branch ran.
		var obbPath string

		// Fix #12 (Round 10): Reset corruption state before re-evaluation to avoid stale warnings persisting
		newCorrupted = false
		newReason = ""

		if apkResult.Corrupted {
			newCorrupted = true
			newReason = apkResult.CorruptionReason
			vlog.Get().Warn().Str("package", packageName).Str("file", filePath).Str("reason", apkResult.CorruptionReason).Msg("game became corrupted during revalidation")
		} else {
			// APK is valid — extract metadata first to get correct version code for OBB matching
			// Fix #1 (Round 11): Use APK's version code (not DB version) for OBB matching
			meta, metaErr = ExtractAPKMetadata(filePath)
			versionCodeForMatching := existing.VersionCode
			if metaErr == nil && meta.PackageName != "" {
				versionCodeForMatching = meta.VersionCode
			}

			// Check OBB files (Fix #8: lowercase for case-insensitive matching)
			dir := filepath.Dir(filePath)
			allFiles, scanErr := ScanDirectory(dir)
			// Story 9.10: capture the absolute path of the first valid OBB
			// for the file server (mirrors the ImportAPK behavior).
			if scanErr != nil {
				vlog.Get().Warn().Err(scanErr).Str("dir", dir).Msg("failed to scan directory for OBB files during revalidation")
			} else {
				for _, f := range allFiles {
					if !f.IsAPK && IsOBBFile(f.Name) {
						vc, obbPkgName, ok := ExtractOBBPackageName(strings.ToLower(f.Name))
						// Fix #1 (Round 11): Use version code from APK metadata, not from DB entry
						// Fix #10 (Round 15): Case-insensitive matching for package name
						if ok && obbPkgName == strings.ToLower(packageName) && vc == int64(versionCodeForMatching) {
							obbSize += f.Size
							if obbPath == "" && f.Path != "" {
								obbPath = f.Path
							}
							obbResult := ValidateOBB(f.Path)
							if obbResult.Corrupted {
								newCorrupted = true
								newReason = fmt.Sprintf("OBB: %s", obbResult.CorruptionReason)
								vlog.Get().Warn().Str("package", packageName).Str("obb_path", f.Path).Str("reason", obbResult.CorruptionReason).Msg("corrupted OBB detected during revalidation")
							} else if obbResult.CorruptionReason != "" && !newCorrupted && newReason == "" {
								// Non-standard naming warning (Option B) - only set if not already set
								// Fix (Round 11): Preserve first warning instead of overwriting with subsequent ones
								newReason = fmt.Sprintf("OBB: %s", obbResult.CorruptionReason)
							}
						}
					}
				}

				if !newCorrupted {
					vlog.Get().Info().Str("package", packageName).Msg("game re-validation passed, corruption cleared")
				}
			}
		}

		// Fix #5 (Round 15): Consolidate multiple sequential transactions into a single unified transaction
		// Fix #4 (Round 15): Panic-safe BeginTx with deferred Rollback
		tx, txErr := gm.database.BeginTx(ctx, nil)
		if txErr != nil {
			return false, fmt.Errorf("begin transaction for revalidation %q: %w", packageName, txErr)
		}
		defer tx.Rollback()

		// Fix #3 (Round 11): Re-extract metadata when game is restored from corrupted to valid
		// Fix #2 (Round 11): Also update if version code changed (game file was updated/replaced)
		// Story 9.10: also refresh apk_path if it changed (the file may
		// have been moved to a different game_folders location between
		// scans; the revalidate is the only place we can detect a path
		// change at the same time as a mtime change).
		if !newCorrupted && metaErr == nil && meta.PackageName != "" && (existing.GameName == "" || existing.VersionCode == 0 || int64(meta.VersionCode) != existing.VersionCode) {
			updateQuery := `UPDATE games SET game_name = ?, version_code = ?, size_bytes = ?, apk_path = ?, last_updated = ? WHERE package_name = ?`
			if _, execErr := tx.Exec(updateQuery, meta.Label, meta.VersionCode, info.Size(), filePath, info.ModTime().Unix(), packageName); execErr != nil {
				return false, fmt.Errorf("failed to update game metadata: %w", execErr)
			}
			existing.GameName = meta.Label
			existing.VersionCode = meta.VersionCode
			existing.SizeBytes = info.Size()
			existing.ApkPath = filePath
		}

		// Story 9.10: if the APK was moved and the OBB is in the new
		// directory, refresh the OBB path (only when an OBB was
		// found and the old obb_path differs from the new one).
		if !newCorrupted && obbPath != "" && existing.OBBPath != obbPath {
			updateQuery := `UPDATE games SET obb_path = ?, last_updated = ? WHERE package_name = ?`
			if _, execErr := tx.Exec(updateQuery, obbPath, info.ModTime().Unix(), packageName); execErr != nil {
				return false, fmt.Errorf("failed to update OBB path: %w", execErr)
			}
			existing.OBBPath = obbPath
		}

		// Fix #3 (Round 11): Update OBB size if it changed, not just if it was zero
		if !newCorrupted && existing.OBBSizeBytes != obbSize && obbSize > 0 {
			updateQuery := `UPDATE games SET obb_size_bytes = ?, last_updated = ? WHERE package_name = ?`
			if _, execErr := tx.Exec(updateQuery, obbSize, info.ModTime().Unix(), packageName); execErr != nil {
				return false, fmt.Errorf("failed to update OBB size: %w", execErr)
			}
			existing.OBBSizeBytes = obbSize
		}

		// Fix #12 (Round 12): When corruption is cleared, re-expose the game so it's served to users
		// Story 9.10: also refresh apk_path on every revalidation cycle
		// (even when the file size / version code did not change), so
		// that path changes from "moved within game_folders" are
		// picked up on the next scan.
		newExposed := !newCorrupted
		updateQuery := `UPDATE games SET corrupted = ?, corruption_reason = ?, exposed = ?, apk_path = ?, last_updated = ? WHERE package_name = ?`
		if _, execErr := tx.Exec(updateQuery, newCorrupted, newReason, newExposed, filePath, info.ModTime().Unix(), packageName); execErr != nil {
			return false, fmt.Errorf("update corruption status for %q: %w", packageName, execErr)
		}
		existing.ApkPath = filePath

		// Commit the unified transaction
		if commitErr := tx.Commit(); commitErr != nil {
			return false, fmt.Errorf("commit transaction for revalidation %q: %w", packageName, commitErr)
		}

		// If game is now corrupted, it's not importable - return false to skip
		if newCorrupted {
			return false, nil
		}

		// Game was previously corrupted but is now valid — still skip import since it exists
		// The corruption flag has been cleared and exposed has been set to true
		return false, nil
	}

	// File mtime is unchanged — normally no work needed. BUT
	// (Story 9.10 post-merge bugfix): if the row was inserted
	// before the 9.10 migration, `apk_path` is still empty. The
	// file server falls back to the legacy dataDir/games/.../ path
	// when apk_path is empty, which is fine for the 3 legacy games
	// that the operator manually copied there, BUT it returns 404
	// for any APK that lives at a real game_folders location
	// (e.g. AkiBonbon at the root of D:\Documents\Jeux\VR\Test\).
	// We refresh apk_path in a minimal UPDATE so the file server
	// can serve the file from the real disk location. No other
	// column is touched (last_updated only — version_code,
	// size_bytes, etc. are unchanged from the original import).
	if existing.ApkPath == "" {
		vlog.Get().Info().
			Str("package", packageName).
			Str("file", filePath).
			Msg("mtime unchanged but apk_path empty: backfilling apk_path from revalidate (9.10-post bugfix)")

		tx, txErr := gm.database.BeginTx(ctx, nil)
		if txErr != nil {
			return false, fmt.Errorf("begin transaction for apk_path backfill %q: %w", packageName, txErr)
		}
		defer tx.Rollback()

		updateQuery := `UPDATE games SET apk_path = ?, last_updated = ? WHERE package_name = ?`
		if _, execErr := tx.Exec(updateQuery, filePath, time.Now().Unix(), packageName); execErr != nil {
			return false, fmt.Errorf("update apk_path (mtime-unchanged backfill) for %q: %w", packageName, execErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, fmt.Errorf("commit apk_path backfill for %q: %w", packageName, commitErr)
		}
		existing.ApkPath = filePath
	}

	// File hasn't changed, no re-validation needed
	return false, nil
}

// ExtractPackageNameFromPath extracts the package name from an APK file path.
// e.g., "/data/games/com.example.game.apk" -> "com.example.game"
// Handles case-insensitive .apk/.APK extensions (Fix #14 Round 11)
func ExtractPackageNameFromPath(filePath string) string {
	base := filepath.Base(filePath)
	ext := strings.ToLower(filepath.Ext(base))
	if ext != ".apk" {
		return ""
	}
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if name == "" || name == base {
		return ""
	}
	return name
}
