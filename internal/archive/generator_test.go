package archive

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

func TestGenerateVRPGameListContent_SingleGame(t *testing.T) {
	games := []types.GameEntry{
		{
			GameName:    "Test Game",
			ReleaseName: "test_v1.0",
			PackageName: "com.test.game",
			VersionCode: 42,
			SizeBytes:   104857600,
			Popularity:  50,
		},
	}

	result := GenerateVRPGameListContent(games)
	expected := "Test Game;test_v1.0;com.test.game;42;104857600;50\n"

	if result != expected {
		t.Errorf("VRP-GameList.txt content = %q, want %q", result, expected)
	}
}

func TestGenerateVRPGameListContent_MultipleGames(t *testing.T) {
	games := []types.GameEntry{
		{
			GameName:    "Game A",
			ReleaseName: "game_a_v1",
			PackageName: "com.game.a",
			VersionCode: 1,
			SizeBytes:   50000000,
			Popularity:  30,
		},
		{
			GameName:    "Game B",
			ReleaseName: "game_b_v2",
			PackageName: "com.game.b",
			VersionCode: 2,
			SizeBytes:   80000000,
			Popularity:  70,
		},
	}

	result := GenerateVRPGameListContent(games)

	if !strings.Contains(result, "Game A;game_a_v1;com.game.a;1;50000000;30\n") {
		t.Errorf("missing Game A entry in: %q", result)
	}
	if !strings.Contains(result, "Game B;game_b_v2;com.game.b;2;80000000;70\n") {
		t.Errorf("missing Game B entry in: %q", result)
	}
}

func TestGenerateVRPGameListContent_Empty(t *testing.T) {
	result := GenerateVRPGameListContent(nil)
	if result != "\n" {
		t.Errorf("empty games list should produce single newline, got %q", result)
	}
}

func TestBuildGameListForMeta7z_FiltersCorrupted(t *testing.T) {
	games := []types.GameEntry{
		{GameName: "Good Game", PackageName: "com.good.game", Corrupted: false, Exposed: true, Popularity: 10},
		{GameName: "Bad Game", PackageName: "com.bad.game", Corrupted: true, Exposed: true, Popularity: 20},
	}

	result := BuildGameListForMeta7z(games)

	if len(result) != 1 {
		t.Fatalf("expected 1 game after filtering corrupted, got %d", len(result))
	}
	if result[0].PackageName != "com.good.game" {
		t.Errorf("expected com.good.game, got %s", result[0].PackageName)
	}
}

func TestBuildGameListForMeta7z_FiltersNotExposed(t *testing.T) {
	games := []types.GameEntry{
		{GameName: "Exposed Game", PackageName: "com.exposed.game", Corrupted: false, Exposed: true, Popularity: 10},
		{GameName: "Hidden Game", PackageName: "com.hidden.game", Corrupted: false, Exposed: false, Popularity: 20},
	}

	result := BuildGameListForMeta7z(games)

	if len(result) != 1 {
		t.Fatalf("expected 1 game after filtering not exposed, got %d", len(result))
	}
	if result[0].PackageName != "com.exposed.game" {
		t.Errorf("expected com.exposed.game, got %s", result[0].PackageName)
	}
}

func TestBuildGameListForMeta7z_SortsByPopularity(t *testing.T) {
	games := []types.GameEntry{
		{GameName: "Low Pop", PackageName: "com.low.pop", Corrupted: false, Exposed: true, Popularity: 5},
		{GameName: "High Pop", PackageName: "com.high.pop", Corrupted: false, Exposed: true, Popularity: 90},
		{GameName: "Mid Pop", PackageName: "com.mid.pop", Corrupted: false, Exposed: true, Popularity: 50},
	}

	result := BuildGameListForMeta7z(games)

	if len(result) != 3 {
		t.Fatalf("expected 3 games, got %d", len(result))
	}
	if result[0].PackageName != "com.high.pop" {
		t.Errorf("first game should be highest popularity, got %s", result[0].PackageName)
	}
	if result[1].PackageName != "com.mid.pop" {
		t.Errorf("second game should be mid popularity, got %s", result[1].PackageName)
	}
	if result[2].PackageName != "com.low.pop" {
		t.Errorf("third game should be lowest popularity, got %s", result[2].PackageName)
	}
}

func TestBuildGameListForMeta7z_EmptyResult(t *testing.T) {
	games := []types.GameEntry{
		{GameName: "Corrupted Only", PackageName: "com.corrupt", Corrupted: true, Exposed: true},
	}

	result := BuildGameListForMeta7z(games)

	if len(result) != 0 {
		t.Errorf("expected 0 games for all corrupted, got %d", len(result))
	}
}

func TestGenerateMeta7z_CreatesValidArchive(t *testing.T) {
	sevenZipPath, lookErr := sevenZipBinaryPath(context.Background(), t.TempDir())
	if lookErr != nil {
		t.Skip("7z/7zz not in PATH, skipping AES-256 archive test:", lookErr)
	}

	tmpDir := t.TempDir()
	metadata, err := NewMetadataCache(tmpDir)
	if err != nil {
		t.Fatalf("NewMetadataCache: %v", err)
	}

	games := []types.GameEntry{
		{
			GameName:    "Test Game",
			ReleaseName: "test_v1.0",
			PackageName: "com.test.game",
			VersionCode: 42,
			SizeBytes:   104857600,
			Popularity:  50,
		},
	}

	var buf bytes.Buffer
	err = GenerateMeta7z(context.Background(), games, metadata, &buf, "test-password-123")
	if err != nil {
		t.Fatalf("GenerateMeta7z failed: %v", err)
	}

	if buf.Len() == 0 {
		t.Error("expected non-empty archive")
	}

	// Verify the buffer starts with 7z magic bytes (37 7A BC AF 27 1C)
	sevenZMagic := []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}
	if !bytes.HasPrefix(buf.Bytes(), sevenZMagic) {
		t.Errorf("expected archive to start with 7z magic bytes, got first 6 bytes: %x", buf.Bytes()[:min(6, len(buf.Bytes()))])
	}

	// Verify we can list the archive and find AES-256 in the method line.
	archivePath := filepath.Join(tmpDir, "test.7z")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	cmd := exec.Command(sevenZipPath, "l", "-slt", "-ptest-password-123", archivePath)
	listOut, listErr := cmd.Output()
	if listErr != nil {
		if exitErr, ok := listErr.(*exec.ExitError); ok {
			t.Fatalf("7z list failed: %v (stderr: %s)", listErr, string(exitErr.Stderr))
		}
		t.Fatalf("7z list failed: %v", listErr)
	}
	listStr := string(listOut)
	if !strings.Contains(listStr, "7zAES") {
		t.Errorf("expected 7zAES encryption in archive listing, got:\n%s", listStr)
	}
	if !strings.Contains(listStr, "VRP-GameList.txt") {
		t.Errorf("expected VRP-GameList.txt in archive listing, got:\n%s", listStr)
	}
}

func TestGenerateMeta7z_EmptyGameList(t *testing.T) {
	if _, err := sevenZipBinaryPath(context.Background(), t.TempDir()); err != nil {
		t.Skip("7z/7zz not in PATH, skipping AES-256 archive test:", err)
	}

	tmpDir := t.TempDir()
	metadata, err := NewMetadataCache(tmpDir)
	if err != nil {
		t.Fatalf("NewMetadataCache: %v", err)
	}

	var buf bytes.Buffer
	err = GenerateMeta7z(context.Background(), nil, metadata, &buf, "test-pw")
	if err != nil {
		t.Fatalf("GenerateMeta7z with empty games should not error: %v", err)
	}

	if buf.Len() == 0 {
		t.Error("expected non-empty archive even with empty game list")
	}
}

func TestGenerateMeta7z_NilMetadata(t *testing.T) {
	if _, err := sevenZipBinaryPath(context.Background(), t.TempDir()); err != nil {
		t.Skip("7z/7zz not in PATH, skipping AES-256 archive test:", err)
	}

	games := []types.GameEntry{
		{GameName: "Test Game", ReleaseName: "test_v1.0", PackageName: "com.test.game", VersionCode: 1, SizeBytes: 1024, Popularity: 50},
	}

	var buf bytes.Buffer
	err := GenerateMeta7z(context.Background(), games, nil, &buf, "test-pw")
	if err != nil {
		t.Fatalf("GenerateMeta7z with nil metadata should not error: %v", err)
	}

	if buf.Len() == 0 {
		t.Fatal("expected non-empty archive even with nil metadata")
	}

	// Verify the buffer starts with 7z magic bytes.
	sevenZMagic := []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}
	bufBytes := buf.Bytes()
	if !bytes.HasPrefix(bufBytes, sevenZMagic) {
		t.Fatalf("expected archive to start with 7z magic bytes, got first 6 bytes: %x", bufBytes[:min(6, len(bufBytes))])
	}
}

func TestMetadataCache_ThumbnailPath(t *testing.T) {
	cache, err := NewMetadataCache("/data/metadata")
	if err != nil {
		t.Fatalf("NewMetadataCache: %v", err)
	}
	got := cache.ThumbnailPath("com.test.game")
	if !strings.HasSuffix(got, "thumbnails/com.test.game.jpg") && !strings.HasSuffix(got, `thumbnails\com.test.game.jpg`) {
		t.Errorf("ThumbnailPath = %q, expected path ending with thumbnails/com.test.game.jpg", got)
	}
}

func TestMetadataCache_NotesPath(t *testing.T) {
	cache, err := NewMetadataCache("/data/metadata")
	if err != nil {
		t.Fatalf("NewMetadataCache: %v", err)
	}
	got := cache.NotesPath("com.test.game")
	if !strings.HasSuffix(got, "notes/com.test.game.txt") && !strings.HasSuffix(got, `notes\com.test.game.txt`) {
		t.Errorf("NotesPath = %q, expected path ending with notes/com.test.game.txt", got)
	}
}

func TestMetadataCache_IconPath(t *testing.T) {
	cache, err := NewMetadataCache("/data/metadata")
	if err != nil {
		t.Fatalf("NewMetadataCache: %v", err)
	}
	got := cache.IconPath("com.test.game")
	if !strings.HasSuffix(got, "icons/com.test.game.png") && !strings.HasSuffix(got, `icons\com.test.game.png`) {
		t.Errorf("IconPath = %q, expected path ending with icons/com.test.game.png", got)
	}
}

func TestMetadataCache_ThumbnailExists_True(t *testing.T) {
	tmpDir := t.TempDir()
	thumbsDir := filepath.Join(tmpDir, "thumbnails")
	os.MkdirAll(thumbsDir, 0755)
	testFile := filepath.Join(thumbsDir, "com.test.game.jpg")
	os.WriteFile(testFile, []byte("fake thumbnail"), 0644)

	cache, err := NewMetadataCache(tmpDir)
	if err != nil {
		t.Fatalf("NewMetadataCache: %v", err)
	}
	if !cache.ThumbnailExists("com.test.game") {
		t.Error("expected ThumbnailExists to return true for existing file")
	}
}

func TestMetadataCache_ThumbnailExists_False(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewMetadataCache(tmpDir)
	if err != nil {
		t.Fatalf("NewMetadataCache: %v", err)
	}
	if cache.ThumbnailExists("nonexistent.game") {
		t.Error("expected ThumbnailExists to return false for missing file")
	}
}

func TestMetadataCache_NotesExists_True(t *testing.T) {
	tmpDir := t.TempDir()
	notesDir := filepath.Join(tmpDir, "notes")
	os.MkdirAll(notesDir, 0755)
	testFile := filepath.Join(notesDir, "com.test.game.txt")
	os.WriteFile(testFile, []byte("fake notes"), 0644)

	cache, err := NewMetadataCache(tmpDir)
	if err != nil {
		t.Fatalf("NewMetadataCache: %v", err)
	}
	if !cache.NotesExists("com.test.game") {
		t.Error("expected NotesExists to return true for existing file")
	}
}

func TestMetadataCache_IconExists_True(t *testing.T) {
	tmpDir := t.TempDir()
	iconsDir := filepath.Join(tmpDir, "icons")
	os.MkdirAll(iconsDir, 0755)
	testFile := filepath.Join(iconsDir, "com.test.game.png")
	os.WriteFile(testFile, []byte("fake icon"), 0644)

	cache, err := NewMetadataCache(tmpDir)
	if err != nil {
		t.Fatalf("NewMetadataCache: %v", err)
	}
	if !cache.IconExists("com.test.game") {
		t.Error("expected IconExists to return true for existing file")
	}
}

func TestGenerateMeta7z_WithMetadataFiles(t *testing.T) {
	if _, err := sevenZipBinaryPath(context.Background(), t.TempDir()); err != nil {
		t.Skip("7z/7zz not in PATH, skipping AES-256 archive test:", err)
	}

	tmpDir := t.TempDir()

	// Create metadata directories and files.
	thumbsDir := filepath.Join(tmpDir, "thumbnails")
	os.MkdirAll(thumbsDir, 0755)
	os.WriteFile(filepath.Join(thumbsDir, "com.test.game.jpg"), []byte("thumbnail data"), 0644)

	notesDir := filepath.Join(tmpDir, "notes")
	os.MkdirAll(notesDir, 0755)
	os.WriteFile(filepath.Join(notesDir, "com.test.game.txt"), []byte("game notes content"), 0644)

	iconsDir := filepath.Join(tmpDir, "icons")
	os.MkdirAll(iconsDir, 0755)
	os.WriteFile(filepath.Join(iconsDir, "com.test.game.png"), []byte("icon data"), 0644)

	metadata, err := NewMetadataCache(tmpDir)
	if err != nil {
		t.Fatalf("NewMetadataCache: %v", err)
	}

	games := []types.GameEntry{
		{
			GameName:    "Test Game",
			ReleaseName: "test_v1.0",
			PackageName: "com.test.game",
			VersionCode: 42,
			SizeBytes:   104857600,
			Popularity:  50,
		},
	}

	var buf bytes.Buffer
	err = GenerateMeta7z(context.Background(), games, metadata, &buf, "test-pw")
	if err != nil {
		t.Fatalf("GenerateMeta7z with metadata failed: %v", err)
	}

	if buf.Len() == 0 {
		t.Error("expected non-empty archive")
	}
}

func TestNewGenerator(t *testing.T) {
	gen := NewGenerator(nil)

	if gen == nil {
		t.Error("expected non-nil Generator")
	}
}

func TestSanitizeArchivePath_Valid(t *testing.T) {
	validPaths := []string{
		"VRP-GameList.txt",
		"thumbnails/com.test.game.jpg",
		"notes/game.txt",
		"icons/icon.png",
		"subdir/file.txt",
	}

	for _, path := range validPaths {
		if err := sanitizeArchivePath(path); err != nil {
			t.Errorf("sanitizeArchivePath(%q) = error %v, want nil", path, err)
		}
	}
}

func TestSanitizeArchivePath_Traversal(t *testing.T) {
	traversalPaths := []string{
		"../etc/passwd",
		"..\\windows\\system32",
		"../../secret",
		"/absolute/path",
		"subdir/../../../escape",
		// S-10 hardening: null byte injection (the classic
		// "good\x00bad" trick to truncate a C-level path).
		"good\x00../../etc/passwd",
		"good\x00",
		"\x00",
		// Unicode look-alikes (full-width period) — Go's
		// filepath.Clean doesn't normalize these, but they're
		// not `..` either, so they pass Clean. This is a
		// defense-in-depth: log them at Warn if observed.
		// (Not rejected — that's a stricter policy; for now
		// we just want to verify they don't bypass the
		// traversal check.)
	}

	for _, path := range traversalPaths {
		if err := sanitizeArchivePath(path); err == nil {
			t.Errorf("sanitizeArchivePath(%q) = nil, want error (path traversal)", path)
		}
	}
}

func TestSanitizeArchivePath_Empty(t *testing.T) {
	if err := sanitizeArchivePath(""); err == nil {
		t.Error("sanitizeArchivePath(\"\") = nil, want error")
	}
}

// TestNewMetadataCache_EmptyRoot_ReturnsError is the C-18 regression
// gate: NewMetadataCache("") must fail fast. The previous constructor
// silently accepted an empty root, producing nonsensical paths like
// "thumbnails/pkg.jpg" via filepath.Join("", ...). Callers only
// learned of the misconfiguration when Validate() was invoked later,
// by which point the bad paths may already have been passed to
// downstream consumers.
func TestNewMetadataCache_EmptyRoot_ReturnsError(t *testing.T) {
	cache, err := NewMetadataCache("")
	if err == nil {
		t.Fatal("NewMetadataCache(\"\") = nil error, want error")
	}
	if cache != nil {
		t.Errorf("NewMetadataCache(\"\") returned non-nil cache: %v", cache)
	}
}

// TestNewMetadataCache_ValidRoot_OK covers the happy path: a non-empty
// root produces a usable cache (the previous tests already exercise
// ThumbnailPath/NotesPath/IconPath, this is the constructor round-trip).
func TestNewMetadataCache_ValidRoot_OK(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewMetadataCache(tmpDir)
	if err != nil {
		t.Fatalf("NewMetadataCache(%q) = %v, want nil", tmpDir, err)
	}
	if cache == nil {
		t.Fatal("NewMetadataCache returned nil cache for non-empty root")
	}
	// Sanity: the resulting path helpers are non-empty.
	if path := cache.ThumbnailPath("com.test"); path == "" {
		t.Error("ThumbnailPath returned empty for non-empty root")
	}
}
