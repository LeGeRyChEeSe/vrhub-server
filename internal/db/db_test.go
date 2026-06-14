package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

func TestOpen_CreatesDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Errorf("database file not created at %s: %v", dbPath, statErr)
	}
}

func TestInsertGame_Success(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game := types.GameEntry{
		ReleaseName: "com.example.game",
		GameName:    "Example Game",
		PackageName: "com.example.game",
		VersionCode: 42,
		SizeBytes:   1024,
		Hash:        "testhash",
	}

	err = d.InsertGame(game)
	if err != nil {
		t.Fatalf("insert game: %v", err)
	}
}

func TestGetGameByPackage_Found(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game := types.GameEntry{
		ReleaseName: "com.test.app",
		GameName:    "Test App",
		PackageName: "com.test.app",
		VersionCode: 10,
		SizeBytes:   2048,
		Hash:        "testhash2",
	}

	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	retrieved, err := d.GetGameByPackage("com.test.app")
	if err != nil {
		t.Fatalf("get game by package: %v", err)
	}

	if retrieved.PackageName != "com.test.app" {
		t.Errorf("package_name = %q, want %q", retrieved.PackageName, "com.test.app")
	}
	if retrieved.VersionCode != 10 {
		t.Errorf("version_code = %d, want %d", retrieved.VersionCode, 10)
	}
}

func TestGetGameByPackage_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	_, err = d.GetGameByPackage("nonexistent.package")
	if err == nil {
		t.Fatal("expected error for nonexistent package, got nil")
	}
}

func TestListGames_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	games, err := d.ListGames(nil)
	if err != nil {
		t.Fatalf("list games: %v", err)
	}

	if len(games) != 0 {
		t.Errorf("games count = %d, want 0", len(games))
	}
}

func TestListGames_WithGames(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	for i := 0; i < 3; i++ {
		game := types.GameEntry{
			ReleaseName: "com.example.game" + string(rune('0'+i)),
			GameName:    "Game " + string(rune('0'+i)),
			PackageName: "com.example.game" + string(rune('0'+i)),
			VersionCode: int64(10 + i),
			SizeBytes:   1024 * int64(i+1),
			Hash:        "hash" + string(rune('0'+i)),
		}
		if err := d.InsertGame(game); err != nil {
			t.Fatalf("insert game %d: %v", i, err)
		}
	}

	games, err := d.ListGames(nil)
	if err != nil {
		t.Fatalf("list games: %v", err)
	}

	if len(games) != 3 {
		t.Errorf("games count = %d, want 3", len(games))
	}
}

func TestUpdateGameExposed(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game := types.GameEntry{
		ReleaseName: "com.expose.test",
		GameName:    "Expose Test",
		PackageName: "com.expose.test",
		VersionCode: 1,
		SizeBytes:   512,
		Hash:        "exposehash",
	}

	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	err = d.UpdateGameExposed("com.expose.test", false)
	if err != nil {
		t.Fatalf("update game exposed: %v", err)
	}
}

func TestDeleteGame(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game := types.GameEntry{
		ReleaseName: "com.delete.test",
		GameName:    "Delete Test",
		PackageName: "com.delete.test",
		VersionCode: 1,
		SizeBytes:   256,
		Hash:        "deletehash",
	}

	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	err = d.DeleteGame("com.delete.test")
	if err != nil {
		t.Fatalf("delete game: %v", err)
	}

	games, err := d.ListGames(nil)
	if err != nil {
		t.Fatalf("list games after delete: %v", err)
	}

	if len(games) != 0 {
		t.Errorf("games count after delete = %d, want 0", len(games))
	}
}

func TestCountGames(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	count, err := d.CountGames()
	if err != nil {
		t.Fatalf("count games: %v", err)
	}

	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}

	game := types.GameEntry{
		ReleaseName: "com.count.test",
		GameName:    "Count Test",
		PackageName: "com.count.test",
		VersionCode: 1,
		SizeBytes:   128,
		Hash:        "counthash",
	}

	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	count, err = d.CountGames()
	if err != nil {
		t.Fatalf("count games after insert: %v", err)
	}

	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestNewGameEntryFromScan(t *testing.T) {
	entry := NewGameEntryFromScan("com.test.app", 5, 4096, 8192)

	if entry.ReleaseName != "com.test.app" {
		t.Errorf("release_name = %q, want %q", entry.ReleaseName, "com.test.app")
	}
	if entry.PackageName != "com.test.app" {
		t.Errorf("package_name = %q, want %q", entry.PackageName, "com.test.app")
	}
	if entry.VersionCode != 5 {
		t.Errorf("version_code = %d, want %d", entry.VersionCode, 5)
	}
	if entry.SizeBytes != 4096 {
		t.Errorf("size_bytes = %d, want %d", entry.SizeBytes, 4096)
	}
	if entry.OBBSizeBytes != 8192 {
		t.Errorf("obb_size_bytes = %d, want %d", entry.OBBSizeBytes, 8192)
	}
	if !entry.Exposed {
		t.Error("expected exposed = true")
	}
	if entry.Hash == "" {
		t.Error("hash should not be empty")
	}
}

func TestComputeHash_Deterministic(t *testing.T) {
	hash1 := computeHash("com.test.app")
	hash2 := computeHash("com.test.app")

	if hash1 != hash2 {
		t.Errorf("hashes differ for same input: %q vs %q", hash1, hash2)
	}
}

func TestComputeHash_DifferentInputs(t *testing.T) {
	hash1 := computeHash("com.test.app")
	hash2 := computeHash("com.other.app")

	if hash1 == hash2 {
		t.Errorf("hashes should differ for different inputs: %q vs %q", hash1, hash2)
	}
}

func TestBeginTx_Rollback_DiscardsChanges(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game := types.GameEntry{
		ReleaseName: "com.rollback.test",
		GameName:    "Rollback Test",
		PackageName: "com.rollback.test",
		VersionCode: 1,
		SizeBytes:   1024,
		Hash:        "rollbackhash",
	}

	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	err = d.InsertGameTx(tx, game)
	if err != nil {
		t.Fatalf("insert game in tx: %v", err)
	}

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	games, err := d.ListGames(nil)
	if err != nil {
		t.Fatalf("list games after rollback: %v", err)
	}

	if len(games) != 0 {
		t.Errorf("games count after rollback = %d, want 0", len(games))
	}
}

func TestInsertGameTx_Rollback_DiscardsChanges(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	game1 := types.GameEntry{
		ReleaseName: "com.tx.game1",
		GameName:    "TX Game 1",
		PackageName: "com.tx.game1",
		VersionCode: 1,
		SizeBytes:   512,
		Hash:        "txhash1",
	}

	game2 := types.GameEntry{
		ReleaseName: "com.tx.game2",
		GameName:    "TX Game 2",
		PackageName: "com.tx.game2",
		VersionCode: 2,
		SizeBytes:   1024,
		Hash:        "txhash2",
	}

	err = d.InsertGameTx(tx, game1)
	if err != nil {
		t.Fatalf("insert game1 in tx: %v", err)
	}

	err = d.InsertGameTx(tx, game2)
	if err != nil {
		t.Fatalf("insert game2 in tx: %v", err)
	}

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	games, err := d.ListGames(nil)
	if err != nil {
		t.Fatalf("list games after rollback: %v", err)
	}

	if len(games) != 0 {
		t.Errorf("games count after rollback = %d, want 0 (both inserts should be discarded)", len(games))
	}
}

func TestInsertGameTx_Commit_PersistsChanges(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	game1 := types.GameEntry{
		ReleaseName: "com.commit.game1",
		GameName:    "Commit Game 1",
		PackageName: "com.commit.game1",
		VersionCode: 1,
		SizeBytes:   512,
		Hash:        "commithash1",
	}

	game2 := types.GameEntry{
		ReleaseName: "com.commit.game2",
		GameName:    "Commit Game 2",
		PackageName: "com.commit.game2",
		VersionCode: 2,
		SizeBytes:   1024,
		Hash:        "commithash2",
	}

	err = d.InsertGameTx(tx, game1)
	if err != nil {
		t.Fatalf("insert game1 in tx: %v", err)
	}

	err = d.InsertGameTx(tx, game2)
	if err != nil {
		t.Fatalf("insert game2 in tx: %v", err)
	}

	err = tx.Commit()
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	games, err := d.ListGames(nil)
	if err != nil {
		t.Fatalf("list games after commit: %v", err)
	}

	if len(games) != 2 {
		t.Errorf("games count after commit = %d, want 2", len(games))
	}
}

func TestUpdateGamesExposedTx_Rollback_DiscardsChanges(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game := types.GameEntry{
		ReleaseName: "com.updateexpose.test",
		GameName:    "Update Expose Test",
		PackageName: "com.updateexpose.test",
		VersionCode: 1,
		SizeBytes:   512,
		Hash:        "updateexposehash",
		Exposed:     true,
	}

	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	excludedSet := map[string]bool{
		"com.updateexpose.test": true,
	}

	rowsAffected, err := d.UpdateGamesExposedTx(tx, excludedSet)
	if err != nil {
		t.Fatalf("update games exposed in tx: %v", err)
	}

	if rowsAffected != 1 {
		t.Errorf("rows affected = %d, want 1", rowsAffected)
	}

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	retrieved, err := d.GetGameByPackage("com.updateexpose.test")
	if err != nil {
		t.Fatalf("get game after rollback: %v", err)
	}

	if !retrieved.Exposed {
		t.Error("expected exposed=true after rollback (change should be discarded)")
	}
}

func TestUpdateGamesExposedTx_Commit_PersistsChanges(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game1 := types.GameEntry{
		ReleaseName: "com.txexpose.game1",
		GameName:    "TX Expose Game 1",
		PackageName: "com.txexpose.game1",
		VersionCode: 1,
		SizeBytes:   512,
		Hash:        "txexposehash1",
		Exposed:     true,
	}

	game2 := types.GameEntry{
		ReleaseName: "com.txexpose.game2",
		GameName:    "TX Expose Game 2",
		PackageName: "com.txexpose.game2",
		VersionCode: 2,
		SizeBytes:   1024,
		Hash:        "txexposehash2",
		Exposed:     true,
	}

	if err := d.InsertGame(game1); err != nil {
		t.Fatalf("insert game1: %v", err)
	}

	if err := d.InsertGame(game2); err != nil {
		t.Fatalf("insert game2: %v", err)
	}

	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	excludedSet := map[string]bool{
		"com.txexpose.game1": true,
	}

	rowsAffected, err := d.UpdateGamesExposedTx(tx, excludedSet)
	if err != nil {
		t.Fatalf("update games exposed in tx: %v", err)
	}

	if rowsAffected != 2 {
		t.Errorf("rows affected = %d, want 2", rowsAffected)
	}

	err = tx.Commit()
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	game1Retrieved, _ := d.GetGameByPackage("com.txexpose.game1")
	if game1Retrieved.Exposed {
		t.Error("expected com.txexpose.game1 to be exposed=false after commit")
	}

	game2Retrieved, _ := d.GetGameByPackage("com.txexpose.game2")
	if !game2Retrieved.Exposed {
		t.Error("expected com.txexpose.game2 to be exposed=true after commit")
	}
}

func TestUpdateGamesExposedTx_EmptySet_SetsAllExposed(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game1 := types.GameEntry{
		ReleaseName: "com.emptyset.game1",
		GameName:    "Empty Set Game 1",
		PackageName: "com.emptyset.game1",
		VersionCode: 1,
		SizeBytes:   512,
		Hash:        "emptysethash1",
		Exposed:     false,
	}

	game2 := types.GameEntry{
		ReleaseName: "com.emptyset.game2",
		GameName:    "Empty Set Game 2",
		PackageName: "com.emptyset.game2",
		VersionCode: 2,
		SizeBytes:   1024,
		Hash:        "emptysethash2",
		Exposed:     false,
	}

	if err := d.InsertGame(game1); err != nil {
		t.Fatalf("insert game1: %v", err)
	}

	if err := d.InsertGame(game2); err != nil {
		t.Fatalf("insert game2: %v", err)
	}

	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	excludedSet := map[string]bool{}

	rowsAffected, err := d.UpdateGamesExposedTx(tx, excludedSet)
	if err != nil {
		t.Fatalf("update games exposed in tx: %v", err)
	}

	if rowsAffected != 2 {
		t.Errorf("rows affected = %d, want 2", rowsAffected)
	}

	err = tx.Commit()
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	game1Retrieved, _ := d.GetGameByPackage("com.emptyset.game1")
	if !game1Retrieved.Exposed {
		t.Error("expected com.emptyset.game1 to be exposed=true after empty excluded set")
	}

	game2Retrieved, _ := d.GetGameByPackage("com.emptyset.game2")
	if !game2Retrieved.Exposed {
		t.Error("expected com.emptyset.game2 to be exposed=true after empty excluded set")
	}
}

func TestGetGameByHash_Found(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game := types.GameEntry{
		ReleaseName: "com.test.hashgame",
		GameName:    "Hash Game Test",
		PackageName: "com.test.hashgame",
		VersionCode: 1,
		SizeBytes:   1024,
		Hash:        "abc123def456789012345678abcdef00",
		Exposed:     true,
	}

	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	retrieved, err := d.GetGameByHash("abc123def456789012345678abcdef00")
	if err != nil {
		t.Fatalf("get game by hash: %v", err)
	}

	if retrieved.Hash != "abc123def456789012345678abcdef00" {
		t.Errorf("hash = %q, want %q", retrieved.Hash, "abc123def456789012345678abcdef00")
	}
	if retrieved.PackageName != "com.test.hashgame" {
		t.Errorf("package_name = %q, want %q", retrieved.PackageName, "com.test.hashgame")
	}
	if !retrieved.Exposed {
		t.Error("expected exposed=true")
	}
}

func TestGetGameByHash_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	_, err = d.GetGameByHash("nonexistenthash00000000000000000000000")
	if err == nil {
		t.Fatal("expected error for nonexistent hash, got nil")
	}
}

func TestGetGameByHash_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	_, err = d.GetGameByHash("")
	if err == nil {
		t.Fatal("expected error for empty hash, got nil")
	}
}

func TestGetGameByHash_NonExposed(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game := types.GameEntry{
		ReleaseName: "com.hidden.hashgame",
		GameName:    "Hidden Hash Game",
		PackageName: "com.hidden.hashgame",
		VersionCode: 1,
		SizeBytes:   512,
		Hash:        "hiddenhash000000000000000000000000",
		Exposed:     false,
	}

	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	_, err = d.GetGameByHash("hiddenhash000000000000000000000000")
	if err == nil {
		t.Fatal("expected error for non-exposed game hash, got nil")
	}
}

func TestListPackagesByHash_Found(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// C-05: hash now has a UNIQUE constraint, so each game must have a
	// different hash. The test inserts 3 games with distinct hashes and
	// queries each one in turn.
	games := []types.GameEntry{
		{ReleaseName: "game.z", GameName: "Game Z", PackageName: "com.z.game", VersionCode: 1, SizeBytes: 100, Hash: "listpkg.z.000000000000000000000", Exposed: true},
		{ReleaseName: "game.a", GameName: "Game A", PackageName: "com.a.game", VersionCode: 1, SizeBytes: 200, Hash: "listpkg.a.000000000000000000000", Exposed: true},
		{ReleaseName: "game.m", GameName: "Game M", PackageName: "com.m.game", VersionCode: 1, SizeBytes: 300, Hash: "listpkg.m.000000000000000000000", Exposed: true},
	}

	for _, g := range games {
		if err := d.InsertGame(g); err != nil {
			t.Fatalf("insert game %s: %v", g.PackageName, err)
		}
	}

	// Each hash is unique, so each query returns 1 package. The order is
	// not guaranteed by the query (no ORDER BY in ListPackagesByHash), so
	// we just check the set membership and length.
	seen := make(map[string]bool)
	for _, g := range games {
		packages, err := d.ListPackagesByHash(g.Hash)
		if err != nil {
			t.Fatalf("list packages by hash %s: %v", g.Hash, err)
		}
		if len(packages) != 1 {
			t.Errorf("packages for hash %s: count = %d, want 1", g.Hash, len(packages))
			continue
		}
		if packages[0] != g.PackageName {
			t.Errorf("packages[0] for hash %s = %q, want %q", g.Hash, packages[0], g.PackageName)
		}
		seen[g.PackageName] = true
	}

	if len(seen) != 3 {
		t.Errorf("expected 3 distinct packages, got %d", len(seen))
	}
}

func TestListPackagesByHash_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	packages, err := d.ListPackagesByHash("nonexistenthash00000000000000000000")
	if err != nil {
		t.Fatalf("list packages by hash: %v", err)
	}

	if len(packages) != 0 {
		t.Errorf("packages count = %d, want 0", len(packages))
	}
}

func TestListPackagesByHash_EmptyHash(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	_, err = d.ListPackagesByHash("")
	if err == nil {
		t.Fatal("expected error for empty hash, got nil")
	}
}

func TestListPackagesByHash_ExcludesNonExposed(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	// C-05: hash is UNIQUE, so each game has its own hash. To test the
	// "exclude non-exposed" filter, we query each game's hash and
	// verify that hidden games don't appear in the result.
	exposed := types.GameEntry{ReleaseName: "game.exposed", GameName: "Exposed Game", PackageName: "com.exposed.pkg", VersionCode: 1, SizeBytes: 100, Hash: "hash.exposed.00000000000000000000000", Exposed: true}
	hidden := types.GameEntry{ReleaseName: "game.hidden", GameName: "Hidden Game", PackageName: "com.hidden.pkg", VersionCode: 1, SizeBytes: 200, Hash: "hash.hidden.00000000000000000000000", Exposed: false}

	if err := d.InsertGame(exposed); err != nil {
		t.Fatalf("insert exposed game: %v", err)
	}
	if err := d.InsertGame(hidden); err != nil {
		t.Fatalf("insert hidden game: %v", err)
	}

	// Exposed game: query by its hash should return 1 package.
	packages, err := d.ListPackagesByHash(exposed.Hash)
	if err != nil {
		t.Fatalf("list packages by exposed hash: %v", err)
	}
	if len(packages) != 1 {
		t.Errorf("exposed: packages count = %d, want 1", len(packages))
	}
	if len(packages) > 0 && packages[0] != "com.exposed.pkg" {
		t.Errorf("exposed: packages[0] = %q, want %q", packages[0], "com.exposed.pkg")
	}

	// Hidden game: query by its hash should return 0 packages
	// (exposed=false is filtered out).
	packages, err = d.ListPackagesByHash(hidden.Hash)
	if err != nil {
		t.Fatalf("list packages by hidden hash: %v", err)
	}
	if len(packages) != 0 {
		t.Errorf("hidden: packages count = %d, want 0 (non-exposed should be excluded)", len(packages))
	}
}

// TestInsertGame_HashUnique verifies the UNIQUE constraint on games.hash
// (debt-triage-2026-06-06 C-05). Two games with different release_name but
// the same hash should fail to insert the second one.
func TestInsertGame_HashUnique(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	hash := "duplicatehash000000000000000000000"

	// First insert: succeeds.
	g1 := types.GameEntry{
		ReleaseName: "com.first.game", GameName: "First", PackageName: "com.first.game",
		VersionCode: 1, SizeBytes: 100, Hash: hash, Exposed: true,
	}
	if err := d.InsertGame(g1); err != nil {
		t.Fatalf("insert first game (should succeed): %v", err)
	}

	// Second insert: different release_name, same hash. Should fail.
	g2 := types.GameEntry{
		ReleaseName: "com.second.game", GameName: "Second", PackageName: "com.second.game",
		VersionCode: 1, SizeBytes: 200, Hash: hash, Exposed: true,
	}
	err = d.InsertGame(g2)
	if err == nil {
		t.Fatal("insert second game (duplicate hash) succeeded; expected UNIQUE constraint error")
	}
	if !strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "unique") {
		t.Errorf("error message should mention UNIQUE constraint, got: %v", err)
	}

	// Verify the first game is still there, unchanged.
	got, err := d.GetGameByPackage("com.first.game")
	if err != nil {
		t.Fatalf("get first game: %v", err)
	}
	if got.Hash != hash {
		t.Errorf("first game hash = %q, want %q", got.Hash, hash)
	}
}

// TestMigrate_AddHashUnique_Idempotent verifies that the migration is
// idempotent: running Migrate() twice on the same DB does not fail.
// This guards against the "constraint already exists" error path in
// migrateAddHashUnique.
func TestMigrate_AddHashUnique_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db (first): %v", err)
	}
	// Re-run Migrate explicitly (Open already calls it, but we want
	// to verify a second call is also safe).
	if err := d.Migrate(); err != nil {
		t.Fatalf("migrate (second): %v", err)
	}
	d.Close()

	// Open again, which re-runs Migrate.
	d2, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db (second): %v", err)
	}
	defer d2.Close()

	// Verify the constraint is still in place by attempting a duplicate hash.
	hash := "migrate.duplicate.hash.0000000000000000"
	if err := d2.InsertGame(types.GameEntry{
		ReleaseName: "com.migrate.1", PackageName: "com.migrate.1", VersionCode: 1, Hash: hash, Exposed: true,
	}); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if err := d2.InsertGame(types.GameEntry{
		ReleaseName: "com.migrate.2", PackageName: "com.migrate.2", VersionCode: 1, Hash: hash, Exposed: true,
	}); err == nil {
		t.Fatal("insert 2 (duplicate hash) succeeded; constraint not enforced after re-migrate")
	}
}

// TestUpdateCorruptionStatus_GameNotFound is the C-14 regression gate:
// updating corruption status for a non-existent package must return
// ErrGameNotFound so the caller knows the row was not actually changed.
func TestUpdateCorruptionStatus_GameNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	err = d.UpdateCorruptionStatus("com.does.not.exist", true, "phantom")
	if !errors.Is(err, ErrGameNotFound) {
		t.Errorf("UpdateCorruptionStatus(missing) = %v, want ErrGameNotFound", err)
	}
}

// TestUpdateLastUpdated_GameNotFound is the C-14 regression gate:
// touching last_updated on a missing row must return ErrGameNotFound.
func TestUpdateLastUpdated_GameNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	err = d.UpdateLastUpdated("com.does.not.exist")
	if !errors.Is(err, ErrGameNotFound) {
		t.Errorf("UpdateLastUpdated(missing) = %v, want ErrGameNotFound", err)
	}
}

// TestUpdateCorruptionStatusTx_GameNotFound is the C-14 regression gate
// for the transactional variant.
func TestUpdateCorruptionStatusTx_GameNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	err = d.UpdateCorruptionStatusTx(tx, "com.does.not.exist", true, "phantom")
	if !errors.Is(err, ErrGameNotFound) {
		t.Errorf("UpdateCorruptionStatusTx(missing) = %v, want ErrGameNotFound", err)
	}
}

// TestUpdateGameLastUpdatedTx_GameNotFound is the C-14 regression gate
// for the last-updated transactional variant.
func TestUpdateGameLastUpdatedTx_GameNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	err = d.UpdateGameLastUpdatedTx(tx, "com.does.not.exist")
	if !errors.Is(err, ErrGameNotFound) {
		t.Errorf("UpdateGameLastUpdatedTx(missing) = %v, want ErrGameNotFound", err)
	}
}

// TestAllUpdateFunctionsReturnErrGameNotFound is a structural table-
// driven test for C-14: every public Update* function in the db
// package must return ErrGameNotFound when the target row does not
// exist. Bulk updates (UpdateGamesExposedTx) are intentionally
// excluded — they operate on multiple rows and the spec for them
// returns rowsAffected, not ErrGameNotFound.
func TestAllUpdateFunctionsReturnErrGameNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	cases := []struct {
		name string
		call func(t *testing.T) error
	}{
		{"UpdateCorruptionStatus", func(t *testing.T) error { return d.UpdateCorruptionStatus("com.phantom", true, "x") }},
		{"UpdateLastUpdated", func(t *testing.T) error { return d.UpdateLastUpdated("com.phantom") }},
		{"UpdateCorruptionStatusTx", func(t *testing.T) error {
			tx, err := d.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			defer tx.Rollback()
			return d.UpdateCorruptionStatusTx(tx, "com.phantom", true, "x")
		}},
		{"UpdateGameLastUpdatedTx", func(t *testing.T) error {
			tx, err := d.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			defer tx.Rollback()
			return d.UpdateGameLastUpdatedTx(tx, "com.phantom")
		}},
		// UpdateGameExposed already had the check (pre-C-14); included to
		// guard against regression.
		{"UpdateGameExposed", func(t *testing.T) error { return d.UpdateGameExposed("com.phantom", false) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(t)
			if !errors.Is(err, ErrGameNotFound) {
				t.Errorf("%s(missing) = %v, want ErrGameNotFound", tc.name, err)
			}
		})
	}
}

// silence unused-import warning when sql is only used in some test files.
var _ = sql.ErrNoRows

// TestAllUpdatesIncludeLastUpdated is a structural regression test for C-09:
// it walks the source files of internal/db and internal/game (importer) and
// verifies that every non-bulk `UPDATE games` query that targets a single
// row by `package_name = ?` also writes `last_updated = ?`. Bulk operations
// (e.g. UpdateGamesExposedTx's "UPDATE games SET exposed = 1") are
// deliberately excluded because they don't carry a per-row timestamp.
//
// R8-CR-3: the previous version joined the current line with lines[i+1] to
// form the search window. That missed UPDATE statements whose SQL string
// wrapped across 3+ lines, e.g. a long SET clause. The current version
// extracts the full backtick-delimited string literal that begins with
// "UPDATE games" so any wrap is captured. Single-line literals still work
// (regex backreference matches the same line).
func TestAllUpdatesIncludeLastUpdated(t *testing.T) {
	files := []string{
		"db.go",
		"../game/importer.go",
		"../api/admin.go",
	}
	// Match a backtick-delimited string literal whose first non-space
	// word is "UPDATE games". The (?s) flag lets . match newlines so a
	// multi-line SQL string is captured whole.
	updateStmtRe := regexp.MustCompile("(?s)`\\s*UPDATE games[^`]*`")
	for _, rel := range files {
		path := rel
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(src)

		for _, match := range updateStmtRe.FindAllString(text, -1) {
			// Skip bulk updates (no per-row WHERE clause).
			if !strings.Contains(match, "WHERE package_name = ?") {
				continue
			}
			// Per-row UPDATE must include last_updated anywhere in the
			// captured statement.
			if !strings.Contains(match, "last_updated") {
				// Compute a 1-based line number for the error report.
				idx := strings.Index(text, match)
				line := 1
				if idx >= 0 {
					line = strings.Count(text[:idx], "\n") + 1
				}
				t.Errorf("%s: line %d: per-row UPDATE missing last_updated:\n  %s",
					path, line, strings.SplitN(match, "\n", 2)[0])
			}
		}
	}
}
