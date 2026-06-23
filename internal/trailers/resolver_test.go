package trailers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// fakeSearcher is an injected YouTubeSearcher for the cascade tests.
type fakeSearcher struct {
	url     string
	err     error
	called  bool
	gotLang string
	gotQ    string
}

func (f *fakeSearcher) SearchTrailer(ctx context.Context, apiKey, query, language string) (string, error) {
	f.called = true
	f.gotLang = language
	f.gotQ = query
	return f.url, f.err
}

// TestResolveOverride_PerRelease verifies AC1: a "{releaseName}.trailer"
// sidecar next to the APK is read and trimmed.
func TestResolveOverride_PerRelease(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "game.apk")
	if err := os.WriteFile(apk, []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	const want = "https://www.youtube.com/watch?v=PERREL00001"
	// Trailing newline + whitespace must be trimmed.
	if err := os.WriteFile(filepath.Join(dir, "com.test.game.trailer"), []byte("  "+want+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New("")
	game := types.GameEntry{ReleaseName: "com.test.game", PackageName: "com.test.game", ApkPath: apk}
	got, err := r.Resolve(context.Background(), game, &types.Config{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
}

// TestResolveOverride_GenericFile verifies the generic "trailer.url" fallback
// override is honored when no per-release file exists.
func TestResolveOverride_GenericFile(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "game.apk")
	if err := os.WriteFile(apk, []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	const want = "https://youtu.be/GENERIC0001"
	if err := os.WriteFile(filepath.Join(dir, genericOverrideFileName), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ReadOverrideForDir(dir, "com.test.game")
	if got != want {
		t.Errorf("ReadOverrideForDir = %q, want %q", got, want)
	}
}

// TestResolveOverride_PerReleaseWinsOverGeneric verifies precedence: when both
// sidecars exist, the per-release file wins.
func TestResolveOverride_PerReleaseWinsOverGeneric(t *testing.T) {
	dir := t.TempDir()
	const perRelease = "https://www.youtube.com/watch?v=WINNER00001"
	if err := os.WriteFile(filepath.Join(dir, "rel.trailer"), []byte(perRelease), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, genericOverrideFileName), []byte("https://youtu.be/LOSER"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadOverrideForDir(dir, "rel"); got != perRelease {
		t.Errorf("ReadOverrideForDir = %q, want per-release %q", got, perRelease)
	}
}

// TestResolveOverride_NoApkPath returns "" when ApkPath is empty (legacy game
// not yet backfilled) — there is nothing to look next to.
func TestResolveOverride_NoApkPath(t *testing.T) {
	r := New("")
	game := types.GameEntry{ReleaseName: "rel", PackageName: "rel"} // no ApkPath
	got, err := r.Resolve(context.Background(), game, &types.Config{})
	if err != nil || got != "" {
		t.Errorf("Resolve = (%q, %v), want (\"\", nil)", got, err)
	}
}

// TestResolveCascade_Empty verifies AC5: no override, no oculusdb field, no API
// key → "" with no error.
func TestResolveCascade_Empty(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "game.apk")
	if err := os.WriteFile(apk, []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(t.TempDir()) // empty metadata dir → no oculusdb
	game := types.GameEntry{ReleaseName: "rel", PackageName: "com.no.trailer", GameName: "No Trailer", ApkPath: apk}
	got, err := r.Resolve(context.Background(), game, &types.Config{}) // no API key
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "" {
		t.Errorf("Resolve = %q, want \"\" (graceful empty)", got)
	}
}

// TestResolveOculusDB_NoVideoField verifies the best-effort step is a logged
// no-op against real-shaped oculusdb JSON (no structured video field exists in
// MetaMetadata data — confirmed by 2026-06 research over 15,162 files).
func TestResolveOculusDB_NoVideoField(t *testing.T) {
	metaDir := t.TempDir()
	oculusDir := filepath.Join(metaDir, "data", "oculusdb")
	if err := os.MkdirAll(oculusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Realistic oculusdb JSON (subset of real keys) — NO video/trailer field.
	realish := `{"packageName":"com.real.game","display_name":"Real Game","display_long_description":"A great game. Watch on youtube.com/watch?v=marketing","website_url":"https://example.com"}`
	if err := os.WriteFile(filepath.Join(oculusDir, "com.real.game.json"), []byte(realish), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(metaDir)
	got := r.resolveFromOculusDB(types.GameEntry{PackageName: "com.real.game", ReleaseName: "rel"})
	if got != "" {
		t.Errorf("resolveFromOculusDB = %q, want \"\" (no structured video field; descriptions are not scraped)", got)
	}
}

// TestResolveOculusDB_FutureField verifies forward-compatibility: if a future
// MetaMetadata schema adds a structured trailer_url field, the resolver picks
// it up with no further changes.
func TestResolveOculusDB_FutureField(t *testing.T) {
	metaDir := t.TempDir()
	oculusDir := filepath.Join(metaDir, "data", "oculusdb")
	if err := os.MkdirAll(oculusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const want = "https://www.youtube.com/watch?v=FUTURE00001"
	// Dot-prefixed naming variant is also supported.
	if err := os.WriteFile(filepath.Join(oculusDir, ".com.future.game.json"), []byte(`{"trailer_url":"`+want+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(metaDir)
	got := r.resolveFromOculusDB(types.GameEntry{PackageName: "com.future.game", ReleaseName: "rel"})
	if got != want {
		t.Errorf("resolveFromOculusDB = %q, want %q", got, want)
	}
}

// TestResolveYouTube_UsesLanguageAndQuery verifies AC4: when an API key is set
// the YouTube step runs with relevanceLanguage from cfg.Trailer.Language and a
// "{gameName}" query, and returns its result.
func TestResolveYouTube_UsesLanguageAndQuery(t *testing.T) {
	const want = "https://www.youtube.com/watch?v=YTRESULT001"
	fake := &fakeSearcher{url: want}
	r := NewWithSearcher(t.TempDir(), fake)

	cfg := &types.Config{}
	cfg.Trailer.YouTubeAPIKey = "key-123"
	cfg.Trailer.Language = "fr"

	game := types.GameEntry{ReleaseName: "rel", PackageName: "com.yt.game", GameName: "Cool Game"}
	got, err := r.Resolve(context.Background(), game, cfg)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
	if !fake.called {
		t.Error("expected YouTube searcher to be called")
	}
	if fake.gotLang != "fr" {
		t.Errorf("relevanceLanguage = %q, want \"fr\"", fake.gotLang)
	}
	if fake.gotQ != "Cool Game" {
		t.Errorf("query = %q, want \"Cool Game\"", fake.gotQ)
	}
}

// TestResolveYouTube_NotCalledWithoutKey verifies the YouTube step is skipped
// when no API key is configured (AC5 — pure override path).
func TestResolveYouTube_NotCalledWithoutKey(t *testing.T) {
	fake := &fakeSearcher{url: "https://should.not/be-used"}
	r := NewWithSearcher(t.TempDir(), fake)

	game := types.GameEntry{ReleaseName: "rel", PackageName: "com.yt.game", GameName: "Cool Game"}
	got, err := r.Resolve(context.Background(), game, &types.Config{}) // no key
	if err != nil || got != "" {
		t.Errorf("Resolve = (%q, %v), want (\"\", nil)", got, err)
	}
	if fake.called {
		t.Error("YouTube searcher must NOT be called without an API key")
	}
}

// TestResolveYouTube_ErrorPropagated verifies a real YouTube fault is returned
// (caller treats it as best-effort) and leaves the URL empty.
func TestResolveYouTube_ErrorPropagated(t *testing.T) {
	fake := &fakeSearcher{err: errors.New("boom")}
	r := NewWithSearcher(t.TempDir(), fake)

	cfg := &types.Config{}
	cfg.Trailer.YouTubeAPIKey = "key-123"

	game := types.GameEntry{ReleaseName: "rel", PackageName: "com.yt.game", GameName: "Cool Game"}
	got, err := r.Resolve(context.Background(), game, cfg)
	if err == nil {
		t.Error("expected error to propagate")
	}
	if got != "" {
		t.Errorf("Resolve = %q, want \"\" on error", got)
	}
}

// --- ResolveMissing (batch) ---

type fakeStore struct {
	games   []types.GameEntry
	listErr error
	updated map[string]string
}

func (s *fakeStore) ListAllGamesOrderedByName() ([]types.GameEntry, error) {
	return s.games, s.listErr
}
func (s *fakeStore) UpdateTrailerURL(packageName, url string) error {
	if s.updated == nil {
		s.updated = map[string]string{}
	}
	s.updated[packageName] = url
	return nil
}

// TestResolveMissing_SkipsAlreadyResolved verifies the caching rule: only
// games with an empty trailer_url are (re)resolved; ones that already have a
// URL (e.g. from an import-time override) are left untouched.
func TestResolveMissing_SkipsAlreadyResolved(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "game.apk")
	if err := os.WriteFile(apk, []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	const overrideURL = "https://www.youtube.com/watch?v=OVERRIDE001"
	if err := os.WriteFile(filepath.Join(dir, "needs.trailer"), []byte(overrideURL), 0o644); err != nil {
		t.Fatal(err)
	}

	store := &fakeStore{games: []types.GameEntry{
		// Already has a trailer → must be skipped.
		{PackageName: "com.has.trailer", ReleaseName: "has", TrailerURL: "https://existing/url", ApkPath: apk},
		// Empty trailer + an override sidecar → must be resolved + persisted.
		{PackageName: "com.needs.trailer", ReleaseName: "needs", ApkPath: apk},
	}}

	r := New("")
	n, err := r.ResolveMissing(context.Background(), store, &types.Config{})
	if err != nil {
		t.Fatalf("ResolveMissing: %v", err)
	}
	if n != 1 {
		t.Errorf("resolved count = %d, want 1", n)
	}
	if _, ok := store.updated["com.has.trailer"]; ok {
		t.Error("game with existing trailer must not be updated")
	}
	if store.updated["com.needs.trailer"] != overrideURL {
		t.Errorf("com.needs.trailer updated to %q, want %q", store.updated["com.needs.trailer"], overrideURL)
	}
}

// TestResolveMissing_ListError surfaces an up-front list failure.
func TestResolveMissing_ListError(t *testing.T) {
	store := &fakeStore{listErr: errors.New("db down")}
	r := New("")
	if _, err := r.ResolveMissing(context.Background(), store, &types.Config{}); err == nil {
		t.Error("expected list error to be returned")
	}
}
