package db

import (
	"context"
	"crypto/md5"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	_ "modernc.org/sqlite"
)

// ErrGameNotFound is returned when a game is not found in the database.
var ErrGameNotFound = errors.New("game not found")

// DB wraps a sqlite database connection with convenience methods.
type DB struct {
	conn *sql.DB
}

// GamesTable is the SQL schema for the games table.
//
// Story 7.5: three new columns track per-game download metrics
// (download_count, total_bandwidth_bytes, last_download_at). They are
// defined here for NEW databases. Legacy databases are upgraded in
// place by the migrations in Migrate() (migrateAddStatsColumnDownloadCount,
// migrateAddStatsColumnBandwidth, migrateAddStatsColumnLastDownloadAt).
//
// Story 9.10: one new column `apk_path` records the absolute path of
// the APK on disk so the file server can serve from any
// game_folders layout (no more copy-to-canonical-path required).
// Empty by default — populated by the scanner (ImportAPK / Revalidate)
// and by the legacy backfill (BackfillLegacyApkPaths).
const gamesTableSQL = `
CREATE TABLE IF NOT EXISTS games (
    game_id        INTEGER PRIMARY KEY AUTOINCREMENT,
    release_name   TEXT NOT NULL UNIQUE,
    game_name      TEXT NOT NULL,
    package_name   TEXT NOT NULL,
    version_code   INTEGER NOT NULL,
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    description    TEXT DEFAULT '',
    icon_url       TEXT DEFAULT '',
    thumbnail_url  TEXT DEFAULT '',
    last_updated   INTEGER NOT NULL,
    popularity     INTEGER DEFAULT 0,
    hash           TEXT NOT NULL UNIQUE,
    corrupted      BOOLEAN NOT NULL DEFAULT 0,
    corruption_reason TEXT DEFAULT '',
    exposed        BOOLEAN NOT NULL DEFAULT 1,
    obb_size_bytes INTEGER NOT NULL DEFAULT 0,
    obb_path       TEXT DEFAULT '',
    apk_path       TEXT DEFAULT '',
    trailer_url    TEXT DEFAULT '',
    download_count       INTEGER NOT NULL DEFAULT 0,
    total_bandwidth_bytes INTEGER NOT NULL DEFAULT 0,
    last_download_at      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_games_package_name ON games(package_name);
CREATE INDEX IF NOT EXISTS idx_games_exposed ON games(exposed);
CREATE INDEX IF NOT EXISTS idx_games_hash ON games(hash);
`

// Open opens a SQLite database at the given path and runs migrations.
//
// Story 7.5 T2: the `?_pragma=busy_timeout(5000)` parameter tells
// modernc.org/sqlite to wait up to 5 seconds for a write lock before
// returning SQLITE_BUSY. Without it, concurrent async writes from
// the download-stats hook (one goroutine per download) collide
// immediately and several increments are lost. 5s is a reasonable
// ceiling: the hook runs on a goroutine, so the user-facing download
// is not blocked; the cost is only paid when the DB is genuinely
// contended.
func Open(dbPath string) (*DB, error) {
	dsn := dbPath + "?_pragma=busy_timeout(5000)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	db := &DB{conn: conn}

	if err := db.Migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

// BeginTx begins a new transaction on the database connection.
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return d.conn.BeginTx(ctx, opts)
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.conn.Close()
}

// Migrate runs all schema migrations.
func (d *DB) Migrate() error {
	_, err := d.conn.Exec(gamesTableSQL)
	if err != nil {
		return fmt.Errorf("create games table: %w", err)
	}

	// C-05 (debt-triage-2026-06-06): restore UNIQUE constraint on games.hash
	// for databases created before the constraint was added. CREATE TABLE
	// IF NOT EXISTS does not retroactively add constraints to an existing
	// table, so we use ALTER TABLE for legacy DBs. The constraint is
	// idempotent: if it already exists, the ALTER returns an error which
	// we ignore (UNIQUE constraint already in place).
	//
	// If the table has duplicate hashes (unlikely but possible after a
	// pre-fix import), the ALTER will fail with "UNIQUE constraint failed"
	// on duplicate detection. We surface the error so the operator can
	// deduplicate before retrying. The error message tells them which
	// hashes are duplicated.
	if err := d.migrateAddHashUnique(); err != nil {
		return fmt.Errorf("migrate hash UNIQUE: %w", err)
	}

	// Story 7.5: add the 3 per-game download stat columns to legacy
	// databases. CREATE TABLE IF NOT EXISTS does not retroactively add
	// new columns, so we use ALTER TABLE for DBs created before 7.5.
	// Each helper inspects `PRAGMA table_info(games)` and runs the
	// ALTER only if the column is missing (idempotent on every run).
	if err := d.migrateAddStatsColumnDownloadCount(); err != nil {
		return fmt.Errorf("migrate add download_count: %w", err)
	}
	if err := d.migrateAddStatsColumnBandwidth(); err != nil {
		return fmt.Errorf("migrate add total_bandwidth_bytes: %w", err)
	}
	if err := d.migrateAddStatsColumnLastDownloadAt(); err != nil {
		return fmt.Errorf("migrate add last_download_at: %w", err)
	}

	// Story 9.10: add the games.apk_path column to legacy databases.
	// Stores the absolute path of the APK file on disk (set by the
	// scanner / startup backfill). Same idempotent ALTER TABLE pattern
	// as the stats migrations above: PRAGMA table_info check first,
	// then ALTER TABLE only if the column is missing.
	if err := d.migrateAddApkPath(); err != nil {
		return fmt.Errorf("migrate add apk_path: %w", err)
	}

	// Story 11.1: add the games.trailer_url column to legacy databases.
	// Stores a streaming trailer URL (YouTube watch link) resolved by the
	// trailer cascade. Same idempotent PRAGMA table_info + ALTER TABLE
	// pattern as apk_path above.
	if err := d.migrateAddTrailerURL(); err != nil {
		return fmt.Errorf("migrate add trailer_url: %w", err)
	}

	return nil
}

// migrateAddHashUnique adds a UNIQUE constraint on games.hash via ALTER TABLE.
// Idempotent: returns nil if the constraint already exists.
func (d *DB) migrateAddHashUnique() error {
	// Check if the constraint already exists by inspecting the table schema.
	// SQLite stores CREATE TABLE text in sqlite_master; we look for the
	// hash column to have a UNIQUE constraint. If CREATE TABLE was created
	// with UNIQUE on hash, this migration is a no-op.
	var sqlText string
	err := d.conn.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='games'`,
	).Scan(&sqlText)
	if err != nil {
		return fmt.Errorf("read games table schema: %w", err)
	}
	if strings.Contains(sqlText, "hash") && strings.Contains(sqlText[strings.Index(sqlText, "hash"):], "UNIQUE") {
		// Constraint already in the CREATE TABLE definition.
		return nil
	}

	// Legacy DB without UNIQUE on hash. Try to add the constraint.
	// If duplicate hashes exist, this will fail with a clear error.
	_, err = d.conn.Exec(
		`ALTER TABLE games ADD CONSTRAINT games_hash_unique UNIQUE (hash)`,
	)
	if err != nil {
		// Common case: constraint already added by a prior run.
		// SQLite error msg: "constraint "games_hash_unique" already exists" → idempotent.
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("ALTER TABLE add UNIQUE on hash: %w", err)
	}
	return nil
}

// InsertGame inserts a game entry into the database. Returns an error
// if a game with the same release_name OR hash already exists (UNIQUE
// constraints enforced). Use InsertGameTx + tx.Rollback for batch
// scenarios, or call Update* methods to modify an existing game.
func (d *DB) InsertGame(game types.GameEntry) error {
	query := `
		INSERT INTO games
		(release_name, game_name, package_name, version_code, size_bytes, description, icon_url, thumbnail_url, last_updated, popularity, hash, corrupted, corruption_reason, exposed, obb_size_bytes, obb_path, apk_path, trailer_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := d.conn.Exec(query,
		game.ReleaseName,
		game.GameName,
		game.PackageName,
		game.VersionCode,
		game.SizeBytes,
		game.Description,
		game.IconURL,
		game.ThumbnailURL,
		game.LastUpdated.Unix(),
		game.Popularity,
		game.Hash,
		game.Corrupted,
		game.CorruptionReason,
		game.Exposed,
		game.OBBSizeBytes,
		game.OBBPath,
		game.ApkPath,
		game.TrailerURL,
	)

	if err != nil {
		return fmt.Errorf("insert game: %w", err)
	}

	return nil
}

// InsertGameTx inserts a game entry within an existing transaction.
func (d *DB) InsertGameTx(tx *sql.Tx, game types.GameEntry) error {
	query := `
		INSERT OR REPLACE INTO games
		(release_name, game_name, package_name, version_code, size_bytes, description, icon_url, thumbnail_url, last_updated, popularity, hash, corrupted, corruption_reason, exposed, obb_size_bytes, obb_path, apk_path, trailer_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := tx.Exec(query,
		game.ReleaseName,
		game.GameName,
		game.PackageName,
		game.VersionCode,
		game.SizeBytes,
		game.Description,
		game.IconURL,
		game.ThumbnailURL,
		game.LastUpdated.Unix(),
		game.Popularity,
		game.Hash,
		game.Corrupted,
		game.CorruptionReason,
		game.Exposed,
		game.OBBSizeBytes,
		game.OBBPath,
		game.ApkPath,
		game.TrailerURL,
	)

	if err != nil {
		return fmt.Errorf("insert game tx: %w", err)
	}

	return nil
}

// GetGameByPackage retrieves a game by its package name.
func (d *DB) GetGameByPackage(packageName string) (*types.GameEntry, error) {
	query := `SELECT game_id, release_name, game_name, package_name, version_code, size_bytes, description, icon_url, thumbnail_url, last_updated, popularity, hash, corrupted, corruption_reason, exposed, obb_size_bytes, obb_path, apk_path, trailer_url FROM games WHERE package_name = ?`

	row := d.conn.QueryRow(query, packageName)

	var lastUpdatedUnix int64
	var corrupted bool
	var corruptionReason string
	var exposed bool
	var obbSizeBytes int64
	var obbPath string
	var apkPath string
	var trailerURL string
	game := &types.GameEntry{}
	err := row.Scan(
		&game.ID,
		&game.ReleaseName,
		&game.GameName,
		&game.PackageName,
		&game.VersionCode,
		&game.SizeBytes,
		&game.Description,
		&game.IconURL,
		&game.ThumbnailURL,
		&lastUpdatedUnix,
		&game.Popularity,
		&game.Hash,
		&corrupted,
		&corruptionReason,
		&exposed,
		&obbSizeBytes,
		&obbPath,
		&apkPath,
		&trailerURL,
	)

	if err != nil {
		return nil, fmt.Errorf("get game by package %q: %w", packageName, err)
	}

	game.LastUpdated = time.Unix(lastUpdatedUnix, 0)
	game.Corrupted = corrupted
	game.CorruptionReason = corruptionReason
	game.Exposed = exposed
	game.OBBSizeBytes = obbSizeBytes
	game.OBBPath = obbPath
	game.ApkPath = apkPath
	game.TrailerURL = trailerURL
	return game, nil
}

// ListGames returns all games, optionally filtered by exposed status.
func (d *DB) ListGames(exposed *bool) ([]types.GameEntry, error) {
	query := `SELECT game_id, release_name, game_name, package_name, version_code, size_bytes, description, icon_url, thumbnail_url, last_updated, popularity, hash, corrupted, corruption_reason, exposed, obb_size_bytes, obb_path, apk_path, trailer_url FROM games`

	var args []interface{}
	if exposed != nil {
		query += " WHERE exposed = ?"
		args = append(args, *exposed)
	}

	query += " ORDER BY last_updated DESC"

	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list games: %w", err)
	}
	defer rows.Close()

	var games []types.GameEntry
	for rows.Next() {
		var game types.GameEntry
		var lastUpdatedUnix int64
		var corrupted bool
		var corruptionReason string
		var exposed bool
		var obbSizeBytes int64
		var obbPath string
		var apkPath string
		var trailerURL string
		if err := rows.Scan(
			&game.ID,
			&game.ReleaseName,
			&game.GameName,
			&game.PackageName,
			&game.VersionCode,
			&game.SizeBytes,
			&game.Description,
			&game.IconURL,
			&game.ThumbnailURL,
			&lastUpdatedUnix,
			&game.Popularity,
			&game.Hash,
			&corrupted,
			&corruptionReason,
			&exposed,
			&obbSizeBytes,
			&obbPath,
			&apkPath,
			&trailerURL,
		); err != nil {
			return nil, fmt.Errorf("scan game: %w", err)
		}
		game.LastUpdated = time.Unix(lastUpdatedUnix, 0)
		game.Corrupted = corrupted
		game.CorruptionReason = corruptionReason
		game.Exposed = exposed
		game.OBBSizeBytes = obbSizeBytes
		game.OBBPath = obbPath
		game.ApkPath = apkPath
		game.TrailerURL = trailerURL
		games = append(games, game)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return games, nil
}

// UpdateGameExposed updates the exposed status of a game.
// Returns ErrGameNotFound if no game matches the package name.
func (d *DB) UpdateGameExposed(packageName string, exposed bool) error {
	// C-09: bump last_updated on every mutation so ListGames ORDER BY
	// last_updated DESC reflects the actual recency of the change.
	query := `UPDATE games SET exposed = ?, last_updated = ? WHERE package_name = ?`
	result, err := d.conn.Exec(query, exposed, time.Now().Unix(), packageName)
	if err != nil {
		return fmt.Errorf("update game exposed: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update game exposed: %w", err)
	}
	if rowsAffected == 0 {
		return ErrGameNotFound
	}
	return nil
}

// DeleteGame removes a game by its package name.
// Returns ErrGameNotFound if no game matches the package name.
func (d *DB) DeleteGame(packageName string) error {
	query := `DELETE FROM games WHERE package_name = ?`
	result, err := d.conn.Exec(query, packageName)
	if err != nil {
		return fmt.Errorf("delete game: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete game: %w", err)
	}
	if rowsAffected == 0 {
		return ErrGameNotFound
	}
	return nil
}

// UpdateCorruptionStatus updates the corruption flag and reason for a game.
// Returns ErrGameNotFound if no game matches the package name (C-14).
func (d *DB) UpdateCorruptionStatus(packageName string, corrupted bool, reason string) error {
	query := `UPDATE games SET corrupted = ?, corruption_reason = ?, last_updated = ? WHERE package_name = ?`
	result, err := d.conn.Exec(query, corrupted, reason, time.Now().Unix(), packageName)
	if err != nil {
		return fmt.Errorf("update corruption status for %q: %w", packageName, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update corruption status for %q: %w", packageName, err)
	}
	if rowsAffected == 0 {
		return ErrGameNotFound
	}
	return nil
}

// UpdateLastUpdated updates the last_updated timestamp for a game.
// Returns ErrGameNotFound if no game matches the package name (C-14).
func (d *DB) UpdateLastUpdated(packageName string) error {
	query := `UPDATE games SET last_updated = ? WHERE package_name = ?`
	result, err := d.conn.Exec(query, time.Now().Unix(), packageName)
	if err != nil {
		return fmt.Errorf("update last updated for %q: %w", packageName, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update last updated for %q: %w", packageName, err)
	}
	if rowsAffected == 0 {
		return ErrGameNotFound
	}
	return nil
}

// UpdateGameLastUpdatedTx updates the last_updated timestamp within a transaction.
// Returns ErrGameNotFound if no game matches the package name (C-14).
func (d *DB) UpdateGameLastUpdatedTx(tx *sql.Tx, packageName string) error {
	query := `UPDATE games SET last_updated = ? WHERE package_name = ?`
	result, err := tx.Exec(query, time.Now().Unix(), packageName)
	if err != nil {
		return fmt.Errorf("update game last updated in tx: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update game last updated in tx: %w", err)
	}
	if rowsAffected == 0 {
		return ErrGameNotFound
	}
	return nil
}

// UpdateCorruptionStatusTx updates the corruption flag and reason within a transaction.
// Returns ErrGameNotFound if no game matches the package name (C-14).
func (d *DB) UpdateCorruptionStatusTx(tx *sql.Tx, packageName string, corrupted bool, reason string) error {
	// C-09: bump last_updated on every mutation so ListGames ORDER BY
	// last_updated DESC reflects the actual recency of the change.
	query := `UPDATE games SET corrupted = ?, corruption_reason = ?, last_updated = ? WHERE package_name = ?`
	result, err := tx.Exec(query, corrupted, reason, time.Now().Unix(), packageName)
	if err != nil {
		return fmt.Errorf("update corruption status in tx for %q: %w", packageName, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update corruption status in tx for %q: %w", packageName, err)
	}
	if rowsAffected == 0 {
		return ErrGameNotFound
	}
	return nil
}

// UpdateApkAndOBBPath updates the games.apk_path (and optionally
// obb_path) columns in a single statement. Used by
// game.BackfillLegacyApkPaths at startup to migrate pre-9.10 games
// that have apk_path=” to the new absolute-path layout.
//
// The obb_path argument is only written when non-empty (so the
// caller can pass "" to mean "leave obb_path as-is"). This avoids
// an unconditional clobber of the OBB path during a partial
// migration where the OBB was already set by the scanner but
// the APK is still legacy.
//
// Story 9.10 T4 (Subtask 4.2).
func (d *DB) UpdateApkAndOBBPath(ctx context.Context, packageName, apkPath, obbPath string) error {
	if packageName == "" {
		return fmt.Errorf("update apk_path: empty package name")
	}

	var (
		query string
		args  []interface{}
	)
	if obbPath == "" {
		// C-09: bump last_updated so the change is visible in
		// the admin UI's "recently updated" feed.
		query = `UPDATE games SET apk_path = ?, last_updated = ? WHERE package_name = ?`
		args = []interface{}{apkPath, time.Now().Unix(), packageName}
	} else {
		query = `UPDATE games SET apk_path = ?, obb_path = ?, last_updated = ? WHERE package_name = ?`
		args = []interface{}{apkPath, obbPath, time.Now().Unix(), packageName}
	}

	result, err := d.conn.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update apk_path for %q: %w", packageName, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update apk_path for %q: %w", packageName, err)
	}
	if rowsAffected == 0 {
		return ErrGameNotFound
	}
	return nil
}

// CountGames returns the total number of games.
func (d *DB) CountGames() (int, error) {
	var count int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM games").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count games: %w", err)
	}
	return count, nil
}

// ListAllGamesOrderedByName returns all games ordered by game_name ASC.
func (d *DB) ListAllGamesOrderedByName() ([]types.GameEntry, error) {
	query := `SELECT game_id, release_name, game_name, package_name, version_code, size_bytes, description, icon_url, thumbnail_url, last_updated, popularity, hash, corrupted, corruption_reason, exposed, obb_size_bytes, obb_path, apk_path, trailer_url FROM games ORDER BY game_name ASC`

	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("list all games ordered by name: %w", err)
	}
	defer rows.Close()

	games := make([]types.GameEntry, 0)
	for rows.Next() {
		var game types.GameEntry
		var lastUpdatedUnix int64
		var corrupted bool
		var corruptionReason string
		var exposed bool
		var obbSizeBytes int64
		var obbPath string
		var apkPath string
		var trailerURL string
		if err := rows.Scan(
			&game.ID,
			&game.ReleaseName,
			&game.GameName,
			&game.PackageName,
			&game.VersionCode,
			&game.SizeBytes,
			&game.Description,
			&game.IconURL,
			&game.ThumbnailURL,
			&lastUpdatedUnix,
			&game.Popularity,
			&game.Hash,
			&corrupted,
			&corruptionReason,
			&exposed,
			&obbSizeBytes,
			&obbPath,
			&apkPath,
			&trailerURL,
		); err != nil {
			return nil, fmt.Errorf("scan game: %w", err)
		}
		game.LastUpdated = time.Unix(lastUpdatedUnix, 0)
		game.Corrupted = corrupted
		game.CorruptionReason = corruptionReason
		game.Exposed = exposed
		game.OBBSizeBytes = obbSizeBytes
		game.OBBPath = obbPath
		game.ApkPath = apkPath
		game.TrailerURL = trailerURL
		games = append(games, game)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return games, nil
}

// UpdateGamesExposedTx updates the exposed status for multiple games within a transaction.
// excludedPackages is a set of package names that should be marked as not exposed.
func (d *DB) UpdateGamesExposedTx(tx *sql.Tx, excludedPackages map[string]bool) (int64, error) {
	if len(excludedPackages) == 0 {
		result, err := tx.Exec("UPDATE games SET exposed = 1")
		if err != nil {
			return 0, fmt.Errorf("update all games exposed: %w", err)
		}
		return result.RowsAffected()
	}

	placeholder := make([]string, 0, len(excludedPackages))
	args := make([]interface{}, 0, len(excludedPackages))
	for pkg := range excludedPackages {
		placeholder = append(placeholder, "?")
		args = append(args, interface{}(pkg))
	}

	sqlStr := fmt.Sprintf("UPDATE games SET exposed = CASE WHEN package_name IN (%s) THEN 0 ELSE 1 END", strings.Join(placeholder, ","))
	result, err := tx.Exec(sqlStr, args...)
	if err != nil {
		return 0, fmt.Errorf("update games exposed: %w", err)
	}

	return result.RowsAffected()
}

// ListGamesForMeta7z returns all games suitable for meta.7z generation.
// Filters out corrupted and non-exposed games, ordered by popularity descending.
func (d *DB) ListGamesForMeta7z() ([]types.GameEntry, error) {
	query := `SELECT game_id, release_name, game_name, package_name, version_code, size_bytes, description, icon_url, thumbnail_url, last_updated, popularity, hash, corrupted, corruption_reason, exposed, obb_size_bytes, obb_path, apk_path, trailer_url FROM games WHERE corrupted = 0 AND exposed = 1 ORDER BY popularity DESC`

	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("list games for meta.7z: %w", err)
	}
	defer rows.Close()

	var games []types.GameEntry
	for rows.Next() {
		var game types.GameEntry
		var lastUpdatedUnix int64
		var corrupted bool
		var corruptionReason string
		var exposed bool
		var obbSizeBytes int64
		var obbPath string
		var apkPath string
		var trailerURL string
		if err := rows.Scan(
			&game.ID,
			&game.ReleaseName,
			&game.GameName,
			&game.PackageName,
			&game.VersionCode,
			&game.SizeBytes,
			&game.Description,
			&game.IconURL,
			&game.ThumbnailURL,
			&lastUpdatedUnix,
			&game.Popularity,
			&game.Hash,
			&corrupted,
			&corruptionReason,
			&exposed,
			&obbSizeBytes,
			&obbPath,
			&apkPath,
			&trailerURL,
		); err != nil {
			return nil, fmt.Errorf("scan game: %w", err)
		}
		game.LastUpdated = time.Unix(lastUpdatedUnix, 0)
		game.Corrupted = corrupted
		game.CorruptionReason = corruptionReason
		game.Exposed = exposed
		game.OBBSizeBytes = obbSizeBytes
		game.OBBPath = obbPath
		game.ApkPath = apkPath
		game.TrailerURL = trailerURL
		games = append(games, game)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return games, nil
}

// GetCatalogLastModified returns the most recent last_updated timestamp across
// ALL games regardless of exposed status. Used for the Last-Modified header on
// /meta.7z: any mutation (expose/unexpose/import/delete) bumps this value so
// clients using Last-Modified comparison always detect catalog changes — even
// when the unexposed game had a lower last_updated than the remaining exposed
// games, which would otherwise leave Last-Modified unchanged.
func (d *DB) GetCatalogLastModified() (time.Time, error) {
	var ts sql.NullInt64
	if err := d.conn.QueryRow(`SELECT MAX(last_updated) FROM games`).Scan(&ts); err != nil {
		return time.Time{}, fmt.Errorf("get catalog last modified: %w", err)
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return time.Unix(ts.Int64, 0), nil
}

// GetGameByHash retrieves a game by its hash and exposed status.
func (d *DB) GetGameByHash(hash string) (*types.GameEntry, error) {
	if hash == "" {
		return nil, fmt.Errorf("get game by hash: empty hash")
	}

	query := `SELECT game_id, release_name, game_name, package_name, version_code, size_bytes, description, icon_url, thumbnail_url, last_updated, popularity, hash, corrupted, corruption_reason, exposed, obb_size_bytes, obb_path, apk_path, trailer_url FROM games WHERE hash = ? AND exposed = 1`

	row := d.conn.QueryRow(query, hash)

	var lastUpdatedUnix int64
	var corrupted bool
	var corruptionReason string
	var exposed bool
	var obbSizeBytes int64
	var obbPath string
	var apkPath string
	var trailerURL string
	game := &types.GameEntry{}
	err := row.Scan(
		&game.ID,
		&game.ReleaseName,
		&game.GameName,
		&game.PackageName,
		&game.VersionCode,
		&game.SizeBytes,
		&game.Description,
		&game.IconURL,
		&game.ThumbnailURL,
		&lastUpdatedUnix,
		&game.Popularity,
		&game.Hash,
		&corrupted,
		&corruptionReason,
		&exposed,
		&obbSizeBytes,
		&obbPath,
		&apkPath,
		&trailerURL,
	)

	if err != nil {
		return nil, fmt.Errorf("get game by hash %q: %w", hash, err)
	}

	game.LastUpdated = time.Unix(lastUpdatedUnix, 0)
	game.Corrupted = corrupted
	game.CorruptionReason = corruptionReason
	game.Exposed = exposed
	game.OBBSizeBytes = obbSizeBytes
	game.OBBPath = obbPath
	game.ApkPath = apkPath
	game.TrailerURL = trailerURL
	return game, nil
}

// ListPackagesByHash returns all distinct package names for a given hash, ordered ASC.
func (d *DB) ListPackagesByHash(hash string) ([]string, error) {
	if hash == "" {
		return nil, fmt.Errorf("list packages by hash: empty hash")
	}

	query := `SELECT DISTINCT package_name FROM games WHERE hash = ? AND exposed = 1 ORDER BY package_name ASC`

	rows, err := d.conn.Query(query, hash)
	if err != nil {
		return nil, fmt.Errorf("list packages by hash %q: %w", hash, err)
	}
	defer rows.Close()

	packages := make([]string, 0)
	for rows.Next() {
		var pkg string
		if err := rows.Scan(&pkg); err != nil {
			return nil, fmt.Errorf("scan package name: %w", err)
		}
		packages = append(packages, pkg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration for packages by hash: %w", err)
	}

	return packages, nil
}

// NewGameEntryFromScan creates a GameEntry from scan metadata.
func NewGameEntryFromScan(pkgName string, versionCode int64, sizeBytes int64, obbSizeBytes int64) types.GameEntry {
	hash := computeHash(pkgName)
	return types.GameEntry{
		ReleaseName:  pkgName,
		GameName:     pkgName,
		PackageName:  pkgName,
		VersionCode:  versionCode,
		SizeBytes:    sizeBytes,
		Description:  "",
		IconURL:      "",
		ThumbnailURL: "",
		LastUpdated:  time.Now(),
		Popularity:   0,
		Hash:         hash,
		Corrupted:    false,
		Exposed:      true,
		OBBSizeBytes: obbSizeBytes,
		OBBPath:      "",
	}
}

// ComputeHash computes MD5 hash of the release name.
func ComputeHash(releaseName string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(releaseName+"\n")))
}

// computeHash is an alias for backward compatibility within the package.
var computeHash = ComputeHash

// hasColumn reports whether the games table has a column named name.
// Uses PRAGMA table_info(games) (returns one row per column).
//
// Story 7.5: used by the stats-column migrations to avoid
// "duplicate column name" errors on a second run.
func hasColumn(d *DB, name string) (bool, error) {
	rows, err := d.conn.Query(`PRAGMA table_info(games)`)
	if err != nil {
		return false, fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var cname, ctype string
		var notnull, pk int
		var dfltValue interface{}
		if err := rows.Scan(&cid, &cname, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("scan table_info: %w", err)
		}
		if cname == name {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("rows iteration: %w", err)
	}
	return false, nil
}

// migrateAddStatsColumnDownloadCount adds the games.download_count
// column to legacy DBs. Idempotent: returns nil if the column
// already exists (PRAGMA table_info check).
//
// Story 7.5 T1 (Subtask 1.2). The PRAGMA-table_info pattern
// mirrors the migrateAddHashUnique approach above and is robust
// to multiple migrate runs and to legacy CREATE TABLE definitions
// that did not include the new column.
func (d *DB) migrateAddStatsColumnDownloadCount() error {
	has, err := hasColumn(d, "download_count")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.conn.Exec(`ALTER TABLE games ADD COLUMN download_count INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("ALTER TABLE add download_count: %w", err)
	}
	return nil
}

// migrateAddStatsColumnBandwidth adds the games.total_bandwidth_bytes
// column to legacy DBs. Idempotent: returns nil if the column
// already exists.
func (d *DB) migrateAddStatsColumnBandwidth() error {
	has, err := hasColumn(d, "total_bandwidth_bytes")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.conn.Exec(`ALTER TABLE games ADD COLUMN total_bandwidth_bytes INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("ALTER TABLE add total_bandwidth_bytes: %w", err)
	}
	return nil
}

// migrateAddStatsColumnLastDownloadAt adds the games.last_download_at
// column to legacy DBs. Idempotent: returns nil if the column
// already exists.
func (d *DB) migrateAddStatsColumnLastDownloadAt() error {
	has, err := hasColumn(d, "last_download_at")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.conn.Exec(`ALTER TABLE games ADD COLUMN last_download_at INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("ALTER TABLE add last_download_at: %w", err)
	}
	return nil
}

// migrateAddApkPath adds the games.apk_path column to legacy DBs
// (Story 9.10). Idempotent: returns nil if the column already exists.
//
// Empty DEFAULT ” matches the GameEntry.ApkPath zero value, so all
// existing rows survive the ALTER as "no path" and the file server
// falls back to the legacy dataDir/games/{hash}/{pkgName}/{fileName}
// layout until the startup scan (or a manual rescan) backfills them.
func (d *DB) migrateAddApkPath() error {
	has, err := hasColumn(d, "apk_path")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.conn.Exec(`ALTER TABLE games ADD COLUMN apk_path TEXT DEFAULT ''`); err != nil {
		return fmt.Errorf("ALTER TABLE add apk_path: %w", err)
	}
	return nil
}

// migrateAddTrailerURL adds the games.trailer_url column to legacy DBs
// (Story 11.1). Idempotent: returns nil if the column already exists.
//
// Empty DEFAULT '' matches the GameEntry.TrailerURL zero value, so all
// existing rows survive the ALTER as "no trailer" and the meta.7z /
// listing channels emit nothing for them until the resolver (or an
// operator override sidecar) populates the column.
func (d *DB) migrateAddTrailerURL() error {
	has, err := hasColumn(d, "trailer_url")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.conn.Exec(`ALTER TABLE games ADD COLUMN trailer_url TEXT DEFAULT ''`); err != nil {
		return fmt.Errorf("ALTER TABLE add trailer_url: %w", err)
	}
	return nil
}

// UpdateTrailerURL sets the games.trailer_url column for the game matching
// packageName. Story 11.1 (Delivery contract): the resolver and the scanner
// override both call this to persist a resolved trailer link. Passing an
// empty url clears the trailer (so an operator who removes the sidecar file
// can un-set it on the next rescan). Returns ErrGameNotFound if no game
// matches the package name.
func (d *DB) UpdateTrailerURL(packageName, url string) error {
	if packageName == "" {
		return fmt.Errorf("update trailer_url: empty package name")
	}
	// C-09: bump last_updated so the change is visible in the admin UI's
	// "recently updated" feed and invalidates the meta.7z ETag.
	query := `UPDATE games SET trailer_url = ?, last_updated = ? WHERE package_name = ?`
	result, err := d.conn.Exec(query, url, time.Now().Unix(), packageName)
	if err != nil {
		return fmt.Errorf("update trailer_url for %q: %w", packageName, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update trailer_url for %q: %w", packageName, err)
	}
	if rowsAffected == 0 {
		return ErrGameNotFound
	}
	return nil
}
