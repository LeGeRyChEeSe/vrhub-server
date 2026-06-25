package db

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// TestMigrateAddTrailerURL_LegacyDB simulates a DB created before the
// trailer_url column existed and verifies that Open → Migrate adds it via the
// idempotent PRAGMA table_info + ALTER TABLE pattern (Story 11.1, AC6).
func TestMigrateAddTrailerURL_LegacyDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	legacyConn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	// Pre-11.1 schema (has apk_path from 9.10 but no trailer_url).
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
		    apk_path       TEXT DEFAULT '',
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

	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db (post-migrate): %v", err)
	}
	defer d.Close()

	if !columnExists(t, d, "trailer_url") {
		t.Fatalf("expected games.trailer_url to exist after Migrate on legacy DB")
	}

	// Existing-row round-trip: a legacy insert with no trailer returns "".
	if err := d.InsertGame(types.GameEntry{
		ReleaseName: "com.legacy.game",
		GameName:    "Legacy Game",
		PackageName: "com.legacy.game",
		VersionCode: 1,
		SizeBytes:   100,
		Hash:        "legacyhash0000000000000000000000ab",
		Exposed:     true,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := d.GetGameByPackage("com.legacy.game")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TrailerURL != "" {
		t.Errorf("legacy game TrailerURL = %q, want \"\" (DEFAULT '' fallback)", got.TrailerURL)
	}
}

// TestMigrateAddTrailerURL_Idempotent verifies running Migrate repeatedly is a
// no-op once the column exists (Story 11.1, AC6).
func TestMigrateAddTrailerURL_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if !columnExists(t, d, "trailer_url") {
		t.Fatal("trailer_url column should exist after first Migrate")
	}
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate (second) failed: %v", err)
	}
	if !columnExists(t, d, "trailer_url") {
		t.Error("trailer_url column should still exist after second Migrate")
	}
}

// TestInsertGame_TrailerURLPersisted verifies the round-trip contract: a game
// inserted with a trailer URL returns the same URL via GetGameByPackage,
// GetGameByHash, and ListGamesForMeta7z (Story 11.1, AC1/AC2 plumbing).
func TestInsertGame_TrailerURLPersisted(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	const wantURL = "https://www.youtube.com/watch?v=ABCDEFGHIJK"
	if err := d.InsertGame(types.GameEntry{
		ReleaseName: "com.test.game",
		GameName:    "Test Game",
		PackageName: "com.test.game",
		VersionCode: 1,
		SizeBytes:   100,
		Hash:        "trailerhash00000000000000000000abcd",
		Exposed:     true,
		TrailerURL:  wantURL,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	byPkg, err := d.GetGameByPackage("com.test.game")
	if err != nil {
		t.Fatalf("GetGameByPackage: %v", err)
	}
	if byPkg.TrailerURL != wantURL {
		t.Errorf("GetGameByPackage TrailerURL = %q, want %q", byPkg.TrailerURL, wantURL)
	}

	byHash, err := d.GetGameByHash("trailerhash00000000000000000000abcd")
	if err != nil {
		t.Fatalf("GetGameByHash: %v", err)
	}
	if byHash.TrailerURL != wantURL {
		t.Errorf("GetGameByHash TrailerURL = %q, want %q", byHash.TrailerURL, wantURL)
	}

	meta, err := d.ListGamesForMeta7z()
	if err != nil {
		t.Fatalf("ListGamesForMeta7z: %v", err)
	}
	if len(meta) != 1 || meta[0].TrailerURL != wantURL {
		t.Errorf("ListGamesForMeta7z[0].TrailerURL = %v, want %q", meta, wantURL)
	}
}

// TestUpdateTrailerURL verifies the setter used by the resolver/scanner:
// it updates an existing game, can clear the value, and returns
// ErrGameNotFound for an unknown package (Story 11.1, Task 1).
func TestUpdateTrailerURL(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if err := d.InsertGame(types.GameEntry{
		ReleaseName: "com.test.game",
		GameName:    "Test Game",
		PackageName: "com.test.game",
		VersionCode: 1,
		Hash:        "updhash000000000000000000000000abcd",
		Exposed:     true,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	const url = "https://www.youtube.com/watch?v=ZZZZZZZZZZZ"
	if err := d.UpdateTrailerURL("com.test.game", url); err != nil {
		t.Fatalf("UpdateTrailerURL: %v", err)
	}
	got, _ := d.GetGameByPackage("com.test.game")
	if got.TrailerURL != url {
		t.Errorf("after update TrailerURL = %q, want %q", got.TrailerURL, url)
	}

	// Clearing must work (operator removed the sidecar).
	if err := d.UpdateTrailerURL("com.test.game", ""); err != nil {
		t.Fatalf("UpdateTrailerURL (clear): %v", err)
	}
	got, _ = d.GetGameByPackage("com.test.game")
	if got.TrailerURL != "" {
		t.Errorf("after clear TrailerURL = %q, want \"\"", got.TrailerURL)
	}

	// Unknown package → ErrGameNotFound.
	if err := d.UpdateTrailerURL("com.unknown.game", url); !errors.Is(err, ErrGameNotFound) {
		t.Errorf("UpdateTrailerURL(unknown) err = %v, want ErrGameNotFound", err)
	}

	// Empty package name → error (guard).
	if err := d.UpdateTrailerURL("", url); err == nil {
		t.Error("UpdateTrailerURL(\"\") should error")
	}
}
