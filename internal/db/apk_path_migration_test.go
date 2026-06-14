package db

import (
	"database/sql"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestMigrateAddApkPath_Idempotent verifies the AC1 idempotency gate:
// running Migrate() twice (or re-opening the DB) must not fail. The
// hasColumn PRAGMA check must short-circuit the second run.
//
// Story 9.10 T1 / Subtask 1.5.
func TestMigrateAddApkPath_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// First Migrate (called by Open) — should succeed.
	if !columnExists(t, d, "apk_path") {
		t.Fatal("apk_path column should exist after first Migrate")
	}

	// Second explicit Migrate must not fail.
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate (second) failed: %v", err)
	}

	if !columnExists(t, d, "apk_path") {
		t.Error("apk_path column should still exist after second Migrate")
	}

	d.Close()

	// Re-open — third Migrate run, must not fail.
	d2, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer d2.Close()

	if !columnExists(t, d2, "apk_path") {
		t.Error("apk_path column should still exist after reopen Migrate")
	}
}

// TestMigrateAddApkPath_LegacyDB simulates a pre-9.10 DB (one whose
// CREATE TABLE predates the apk_path column) and verifies that the
// ALTER TABLE migration adds the column on the next Open.
//
// We craft the legacy DB by opening it with a known schema that
// EXCLUDES apk_path, then closing + reopening — the second Open
// triggers Migrate, which should run the ALTER TABLE helper and add
// the column. The DB is then INSERT-able and SELECT-able for
// apk_path (the column exists, default ”).
//
// Story 9.10 T1 / Subtask 1.5 (legacy DB path).
func TestMigrateAddApkPath_LegacyDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	// Open once with a known schema that EXCLUDES apk_path,
	// simulating a pre-9.10 DB.
	legacyConn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	// Pre-9.10 schema (no apk_path column).
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
		    obb_path       TEXT DEFAULT '',
		    download_count       INTEGER NOT NULL DEFAULT 0,
		    total_bandwidth_bytes INTEGER NOT NULL DEFAULT 0,
		    last_download_at      INTEGER NOT NULL DEFAULT 0
		);
	`
	if _, err := legacyConn.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := legacyConn.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	// Reopen via the project's Open — should run Migrate, which adds
	// the apk_path column to the legacy table.
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db (post-migrate): %v", err)
	}
	defer d.Close()

	if !columnExists(t, d, "apk_path") {
		t.Errorf("expected games.apk_path to exist after Migrate on legacy DB")
	}

	// Round-trip: INSERT then SELECT with the new column works.
	if err := d.InsertGame(types.GameEntry{
		ReleaseName: "com.legacy.game",
		GameName:    "Legacy Game",
		PackageName: "com.legacy.game",
		VersionCode: 1,
		SizeBytes:   100,
		Hash:        "legacyhash0000000000000000000000ab",
		Exposed:     true,
		// ApkPath deliberately left empty (legacy behavior).
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := d.GetGameByPackage("com.legacy.game")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ApkPath != "" {
		t.Errorf("legacy game ApkPath = %q, want \"\" (DEFAULT '' fallback)", got.ApkPath)
	}
}

// TestInsertGame_ApkPathPersisted verifies AC2 round-trip: a game
// inserted with a non-empty apk_path returns the same path on
// GetGameByPackage. This is the basic persistence contract — every
// other AC depends on it.
//
// Story 9.10 T1 / Subtask 1.5.
func TestInsertGame_ApkPathPersisted(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	apkPath := `D:\Documents\Jeux\VR\Test\AkiBonbon_v1.0.apk`
	if err := d.InsertGame(types.GameEntry{
		ReleaseName: "com.gcBronze.AkiBonbon",
		GameName:    "AkiBonbon",
		PackageName: "com.gcBronze.AkiBonbon",
		VersionCode: 1,
		SizeBytes:   1024,
		Hash:        "akihash000000000000000000000000abcd",
		Exposed:     true,
		ApkPath:     apkPath,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := d.GetGameByPackage("com.gcBronze.AkiBonbon")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ApkPath != apkPath {
		t.Errorf("ApkPath = %q, want %q (round-trip failed)", got.ApkPath, apkPath)
	}
}

// TestGetGameByHash_ApkPathReturned verifies the SELECT in
// GetGameByHash populates ApkPath (used by the file-server route).
// Mirrors TestInsertGame_ApkPathPersisted but exercises the file
// server's primary lookup path.
//
// Story 9.10 T1 / Subtask 1.5.
func TestGetGameByHash_ApkPathReturned(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	apkPath := `/home/user/games/MyGame.apk`
	hash := "akihash0000000000000000000000000f00d"
	if err := d.InsertGame(types.GameEntry{
		ReleaseName: "com.example.mygame",
		GameName:    "MyGame",
		PackageName: "com.example.mygame",
		VersionCode: 1,
		SizeBytes:   2048,
		Hash:        hash,
		Exposed:     true,
		ApkPath:     apkPath,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := d.GetGameByHash(hash)
	if err != nil {
		t.Fatalf("get by hash: %v", err)
	}
	if got.ApkPath != apkPath {
		t.Errorf("ApkPath = %q, want %q", got.ApkPath, apkPath)
	}
}
