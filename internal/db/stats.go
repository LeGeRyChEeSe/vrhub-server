package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GameStats is the read-side projection of per-game download metrics.
// The fields are populated by ListGameStats / GetStatsForHash and are
// what the admin /admin/api/stats endpoint returns (Story 7.5).
//
// JSON tags use snake_case to match the AC2 response contract:
//
//	{"data": {"stats": [{"hash": ..., "game_name": ..., ...}]}}
//
// (Project convention: JSON field names are snake_case, Go field
// names are MixedCase. See CLAUDE.md § "Naming Conventions".)
//
// Notes:
//   - Hash is the games.hash column (MD5 hex) and is the natural key.
//   - DownloadCount / TotalBandwidthBytes are atomically incremented by
//     IncrementDownloadStats (see stats_migration_test.go for the
//     concurrency contract).
//   - LastDownloadAt is a unix-seconds timestamp; 0 means "never
//     downloaded". The admin UI renders it as a relative time in Michel
//     mode and as an ISO string in Power User mode.
//   - GameFileSize = size_bytes + obb_size_bytes (the same expression
//     used by the meta.7z generator, so the two surfaces agree).
type GameStats struct {
	Hash                string `json:"hash"`
	GameName            string `json:"game_name"`
	PackageName         string `json:"package_name"`
	DownloadCount       int64  `json:"download_count"`
	LastDownloadAt      int64  `json:"last_download_at"`
	TotalBandwidthBytes int64  `json:"total_bandwidth_bytes"`
	GameFileSize        int64  `json:"game_file_size"`
}

// IncrementDownloadStats atomically increments the per-game download
// counters for the game identified by hash.
//
// The query is a single UPDATE with column = column + ? expressions,
// so two concurrent downloads for the same game are serialized by
// SQLite's write lock and produce the correct sum (no read-modify-
// write race).
//
// If the hash does not exist, the UPDATE affects 0 rows and no error
// is returned — the caller can treat it as a silent no-op (e.g. a
// file is downloaded for a game that was deleted between the
// GetGameByHash lookup and the increment).
//
// Story 7.5 T1 (Subtask 1.3).
func (d *DB) IncrementDownloadStats(hash string, bytesServed int64) error {
	if hash == "" {
		return fmt.Errorf("increment download stats: empty hash")
	}
	if bytesServed < 0 {
		bytesServed = 0
	}
	now := time.Now().Unix()

	query := `
		UPDATE games
		SET download_count = download_count + 1,
		    total_bandwidth_bytes = total_bandwidth_bytes + ?,
		    last_download_at = ?
		WHERE hash = ?
	`
	_, err := d.conn.Exec(query, bytesServed, now, hash)
	if err != nil {
		return fmt.Errorf("increment download stats for hash %q: %w", hash, err)
	}
	return nil
}

// ListGameStats returns all games with their download metrics,
// ordered by DownloadCount DESC, LastDownloadAt DESC (tie-break).
//
// The query is a single SELECT — no N+1. A game with 0 downloads
// (never downloaded) is still included, so the UI sees the full
// catalog.
//
// Memory: one row per game (~200 bytes), so 10k games = ~2 MB.
// Acceptable for the admin UI. Pagination is a future story.
//
// Story 7.5 T1 (Subtask 1.3) and T3 (the /admin/api/stats endpoint).
func (d *DB) ListGameStats() ([]GameStats, error) {
	query := `
		SELECT
		    hash,
		    game_name,
		    package_name,
		    download_count,
		    last_download_at,
		    total_bandwidth_bytes,
		    size_bytes + obb_size_bytes AS game_file_size
		FROM games
		ORDER BY download_count DESC, last_download_at DESC
	`

	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("list game stats: %w", err)
	}
	defer rows.Close()

	stats := make([]GameStats, 0)
	for rows.Next() {
		var s GameStats
		if err := rows.Scan(
			&s.Hash,
			&s.GameName,
			&s.PackageName,
			&s.DownloadCount,
			&s.LastDownloadAt,
			&s.TotalBandwidthBytes,
			&s.GameFileSize,
		); err != nil {
			return nil, fmt.Errorf("scan game stats: %w", err)
		}
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration for game stats: %w", err)
	}
	return stats, nil
}

// GetStatsForHash returns the download metrics for a single game.
// Returns ErrGameNotFound if no row matches the hash.
//
// Story 7.5 T1 (Subtask 1.3) — used by tests and by
// IncrementDownloadStats consumers that want to verify the value
// after a write (the production download hook is async-fire-and-
// forget, so the typical caller does not need this method).
func (d *DB) GetStatsForHash(hash string) (GameStats, error) {
	if hash == "" {
		return GameStats{}, fmt.Errorf("get stats for hash: empty hash")
	}

	query := `
		SELECT
		    hash,
		    game_name,
		    package_name,
		    download_count,
		    last_download_at,
		    total_bandwidth_bytes,
		    size_bytes + obb_size_bytes AS game_file_size
		FROM games
		WHERE hash = ?
	`

	row := d.conn.QueryRow(query, hash)
	var s GameStats
	if err := row.Scan(
		&s.Hash,
		&s.GameName,
		&s.PackageName,
		&s.DownloadCount,
		&s.LastDownloadAt,
		&s.TotalBandwidthBytes,
		&s.GameFileSize,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GameStats{}, ErrGameNotFound
		}
		return GameStats{}, fmt.Errorf("get stats for hash %q: %w", hash, err)
	}
	return s, nil
}
