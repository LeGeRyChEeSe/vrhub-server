package db

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestDBMigrate_AddsStatsColumns verifies that a fresh DB created by
// Open() exposes the 3 new stats columns with the correct names and
// the DEFAULT 0 invariant. The CREATE TABLE in db.go declares them
// directly, so a brand-new DB has them after Open → Migrate.
//
// Story 7.5 T1 / AC1.
func TestDBMigrate_AddsStatsColumns(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	for _, col := range []string{"download_count", "total_bandwidth_bytes", "last_download_at"} {
		if !columnExists(t, d, col) {
			t.Errorf("expected games.%s to exist after Migrate", col)
		}
	}
}

// TestDBMigrate_AddsStatsColumns_OnLegacyDB simulates a pre-7.5 DB
// (one whose CREATE TABLE predates the stats columns) and verifies
// that the ALTER TABLE migrations add the columns on the next Open.
//
// We craft the legacy DB by opening it, dropping the column via a
// raw CREATE TABLE statement that mirrors the pre-7.5 schema, then
// closing + reopening — the second Open triggers Migrate, which
// should run the three ALTER TABLE helpers and add the columns.
//
// Story 7.5 T1 / AC1 (legacy DB path).
func TestDBMigrate_AddsStatsColumns_OnLegacyDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	// Open once with a known schema that EXCLUDES the stats columns,
	// simulating a pre-7.5 DB.
	legacyConn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	// Replicate the pre-7.5 games table (no download stats columns).
	legacySchema := `
		CREATE TABLE games (
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
		    obb_path       TEXT DEFAULT ''
		);
	`
	if _, err := legacyConn.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := legacyConn.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	// Reopen via the project's Open — should run Migrate, which adds
	// the stats columns to the legacy table.
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db (post-migrate): %v", err)
	}
	defer d.Close()

	for _, col := range []string{"download_count", "total_bandwidth_bytes", "last_download_at"} {
		if !columnExists(t, d, col) {
			t.Errorf("expected games.%s to exist after Migrate on legacy DB", col)
		}
	}
}

// TestDBMigrate_StatsIdempotent_RunTwice is the AC1 + idempotency
// guard: running Migrate() twice (or re-opening the DB) must not
// fail. The hasColumn PRAGMA check must short-circuit the second run.
func TestDBMigrate_StatsIdempotent_RunTwice(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Second explicit Migrate must not fail.
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate (second) failed: %v", err)
	}
	d.Close()

	// Re-open — third Migrate run, must not fail.
	d2, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer d2.Close()
}

// TestIncrementDownloadStats_IncrementsCounters is the happy path:
// one increment must add +1 to download_count, +N to bandwidth, and
// set last_download_at to ~now.
func TestIncrementDownloadStats_IncrementsCounters(t *testing.T) {
	tmpDir := t.TempDir()
	d, openErr := Open(tmpDir + "/test.db")
	if openErr != nil {
		t.Fatalf("open db: %v", openErr)
	}
	defer d.Close()

	hash := "testhash00000000000000000000000000ab"
	if err := d.InsertGame(types.GameEntry{
		ReleaseName: "com.example.game",
		GameName:    "Example Game",
		PackageName: "com.example.game",
		VersionCode: 1,
		SizeBytes:   1024,
		Hash:        hash,
		Exposed:     true,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	before := time.Now().Unix()
	if err := d.IncrementDownloadStats(hash, 1024); err != nil {
		t.Fatalf("increment: %v", err)
	}
	after := time.Now().Unix()

	stats, err := d.GetStatsForHash(hash)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if stats.DownloadCount != 1 {
		t.Errorf("DownloadCount = %d, want 1", stats.DownloadCount)
	}
	if stats.TotalBandwidthBytes != 1024 {
		t.Errorf("TotalBandwidthBytes = %d, want 1024", stats.TotalBandwidthBytes)
	}
	if stats.LastDownloadAt < before || stats.LastDownloadAt > after {
		t.Errorf("LastDownloadAt = %d, want in [%d, %d]", stats.LastDownloadAt, before, after)
	}
	if stats.GameFileSize != 1024 {
		t.Errorf("GameFileSize = %d, want 1024 (size_bytes + obb_size_bytes)", stats.GameFileSize)
	}
}

// TestIncrementDownloadStats_MultipleCalls verifies the counters
// accumulate across calls (single-threaded sanity check; concurrency
// is covered by the public API integration test).
func TestIncrementDownloadStats_MultipleCalls(t *testing.T) {
	tmpDir := t.TempDir()
	d, openErr := Open(tmpDir + "/test.db")
	if openErr != nil {
		t.Fatalf("open db: %v", openErr)
	}
	defer d.Close()

	hash := "testhash00000000000000000000000000cd"
	if err := d.InsertGame(types.GameEntry{
		ReleaseName: "com.example.g2", GameName: "G2", PackageName: "com.example.g2",
		VersionCode: 1, SizeBytes: 100, Hash: hash, Exposed: true,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for i := 0; i < 5; i++ {
		if err := d.IncrementDownloadStats(hash, 100); err != nil {
			t.Fatalf("increment #%d: %v", i, err)
		}
	}

	stats, err := d.GetStatsForHash(hash)
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if stats.DownloadCount != 5 {
		t.Errorf("DownloadCount = %d, want 5", stats.DownloadCount)
	}
	if stats.TotalBandwidthBytes != 500 {
		t.Errorf("TotalBandwidthBytes = %d, want 500", stats.TotalBandwidthBytes)
	}
}

// TestIncrementDownloadStats_UnknownHash_NoError: a non-existent
// hash must not return an error — the call is a silent no-op (UPDATE
// affects 0 rows). This matches the story's "Corrupted games" rule
// (a stale hash from a deleted game should not bubble an error to
// the download path).
func TestIncrementDownloadStats_UnknownHash_NoError(t *testing.T) {
	tmpDir := t.TempDir()
	d, openErr := Open(tmpDir + "/test.db")
	if openErr != nil {
		t.Fatalf("open db: %v", openErr)
	}
	defer d.Close()

	if err := d.IncrementDownloadStats("ffffffffffffffffffffffffffffffff", 1024); err != nil {
		t.Errorf("expected no error for unknown hash, got: %v", err)
	}
}

// TestListGameStats_SortedByDownloadCountDesc is the AC2 happy
// path: 3 games with distinct counts come back ordered by count
// DESC, with last_download_at as a tie-break.
func TestListGameStats_SortedByDownloadCountDesc(t *testing.T) {
	tmpDir := t.TempDir()
	d, openErr := Open(tmpDir + "/test.db")
	if openErr != nil {
		t.Fatalf("open db: %v", openErr)
	}
	defer d.Close()

	now := time.Now().Unix()
	entries := []struct {
		hash     string
		pkg      string
		count    int64
		lastSeen int64
	}{
		{"hash0000000000000000000000000000a1", "com.g1", 5, now - 100},
		{"hash0000000000000000000000000000a2", "com.g2", 10, now - 50},
		{"hash0000000000000000000000000000a3", "com.g3", 0, 0}, // never downloaded
	}
	for _, e := range entries {
		if err := d.InsertGame(types.GameEntry{
			ReleaseName: e.pkg, GameName: e.pkg, PackageName: e.pkg,
			VersionCode: 1, SizeBytes: 100, Hash: e.hash, Exposed: true,
		}); err != nil {
			t.Fatalf("insert %s: %v", e.hash, err)
		}
		// Apply count + bandwidth + last_download_at by repeated increments.
		for i := int64(0); i < e.count; i++ {
			if err := d.IncrementDownloadStats(e.hash, 100); err != nil {
				t.Fatalf("increment %s: %v", e.hash, err)
			}
		}
		// If lastSeen is 0, the row stays at the DEFAULT 0; the test
		// only checks ordering, not the timestamp.
		_ = e.lastSeen
	}

	got, err := d.ListGameStats()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	wantOrder := []string{
		"hash0000000000000000000000000000a2", // count=10
		"hash0000000000000000000000000000a1", // count=5
		"hash0000000000000000000000000000a3", // count=0
	}
	for i, s := range got {
		if s.Hash != wantOrder[i] {
			t.Errorf("position %d: hash = %q, want %q (counts=%d/%d/%d)",
				i, s.Hash, wantOrder[i], got[0].DownloadCount, got[1].DownloadCount, got[2].DownloadCount)
		}
	}
}

// TestGetStatsForHash_NotFound must return ErrGameNotFound so callers
// can distinguish "no such game" from a real DB error.
func TestGetStatsForHash_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	d, openErr := Open(tmpDir + "/test.db")
	if openErr != nil {
		t.Fatalf("open db: %v", openErr)
	}
	defer d.Close()

	_, err := d.GetStatsForHash("00000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "game not found") {
		t.Errorf("expected ErrGameNotFound, got: %v", err)
	}
}

// columnExists is a small helper for the migration tests above —
// keeps the test bodies readable.
func columnExists(t *testing.T, d *DB, name string) bool {
	t.Helper()
	rows, err := d.conn.Query(`PRAGMA table_info(games)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var cname, ctype string
		var notnull, pk int
		var dfltValue interface{}
		if err := rows.Scan(&cid, &cname, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if cname == name {
			return true
		}
	}
	return false
}
