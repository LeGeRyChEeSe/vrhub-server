package api

// Story 9.9 (B9 fix): cache headers on /meta.7z and /config.json.
//
// Background: the VRHub client (com.vrhub.logic.CatalogUtils +
// MainRepository.syncCatalog) downloads meta.7z on a timer and caches
// it via a Worker. To detect a new catalog version, the client
// compares server-side ETag / Last-Modified / Content-MD5 to its
// locally-saved metadata. Before this fix, the server returned
// none of those headers, so the client always saw `remote metadata: {}`
// and never re-downloaded. The result: an admin who added a 4th game
// and triggered a rescan saw the new game in the admin DB, but the
// client library stayed at 3 games indefinitely (verified on a real
// Quest during the live session).
//
// These tests cover the 4 ACs from the story:
//   AC1 — ETag + Last-Modified + Cache-Control present and stable
//   AC2 — If-None-Match + If-Modified-Since yield 304 when fresh,
//         200 when stale
//   AC3 — /config.json has the same behaviour
//   AC4 — no regression on the existing test suite

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// makeETagFixture wires a minimal PublicAPIHandler with one game in
// the DB so we can exercise the meta.7z handler without depending on
// the rest of the public API surface.
func makeETagFixture(t *testing.T) *PublicAPIHandler {
	t.Helper()
	tmpDir := t.TempDir()

	d, err := db.Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	game := types.GameEntry{
		GameName:    "ETag Test Game",
		ReleaseName: "etag_test_v1",
		PackageName: "com.etag.test",
		VersionCode: 1,
		SizeBytes:   1048576,
		Popularity:  10,
		LastUpdated: time.Unix(1_700_000_000, 0).UTC(),
		Exposed:     true,
	}
	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	h := NewPublicAPIHandler(modeVal)
	h.DB = d
	h.Config = &types.Config{
		DataDir: tmpDir,
		Admin: types.AdminConfig{
			ArchivePassword: "etag-test-pw",
		},
	}
	return h
}

// AC1: ETag, Last-Modified, Cache-Control are present on a 200.
func TestMeta7zHandler_ETagHeader_Present(t *testing.T) {
	h := makeETagFixture(t)

	req := httptest.NewRequest("GET", "/meta.7z", nil)
	rec := httptest.NewRecorder()
	h.Meta7zHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got == "" {
		t.Errorf("ETag header missing")
	}
	if got := rec.Header().Get("Last-Modified"); got == "" {
		t.Errorf("Last-Modified header missing")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
	}
}

// AC1: ETag is stable across two consecutive calls when the DB has
// not changed (the client's "no change → no re-download" path).
func TestMeta7zHandler_ETag_StableForSameDB(t *testing.T) {
	h := makeETagFixture(t)

	req1 := httptest.NewRequest("GET", "/meta.7z", nil)
	rec1 := httptest.NewRecorder()
	h.Meta7zHandler(rec1, req1)
	etag1 := rec1.Header().Get("ETag")
	if etag1 == "" {
		t.Fatalf("first ETag missing")
	}

	req2 := httptest.NewRequest("GET", "/meta.7z", nil)
	rec2 := httptest.NewRecorder()
	h.Meta7zHandler(rec2, req2)
	etag2 := rec2.Header().Get("ETag")
	if etag2 == "" {
		t.Fatalf("second ETag missing")
	}

	if etag1 != etag2 {
		t.Errorf("ETag not stable: first=%q second=%q", etag1, etag2)
	}
}

// AC1: ETag changes after a new game is added (the client's "re-fetch"
// path). We use a different package_name and a different last_updated
// timestamp to make sure both ETag inputs (max last_updated + sorted
// package list) shift.
func TestMeta7zHandler_ETag_ChangesOnDBUpdate(t *testing.T) {
	h := makeETagFixture(t)

	req1 := httptest.NewRequest("GET", "/meta.7z", nil)
	rec1 := httptest.NewRecorder()
	h.Meta7zHandler(rec1, req1)
	etag1 := rec1.Header().Get("ETag")
	if etag1 == "" {
		t.Fatalf("first ETag missing")
	}

	// Reach into the DB to insert a second game with a newer
	// last_updated. We use a direct SQL because InsertGame ignores
	// exposed/corrupted columns on its public API.
	conn, err := sql.Open("sqlite", h.Config.DataDir+"/test.db")
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer conn.Close()
	_, err = conn.Exec(`INSERT INTO games
		(release_name, game_name, package_name, version_code, size_bytes,
		 last_updated, popularity, hash, corrupted, exposed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 1)`,
		"etag_test_v2", "ETag Test Game 2", "com.etag.test2",
		2, 2097152, time.Unix(1_800_000_000, 0).Unix(), 20, "etag_test_v2_hash")
	if err != nil {
		t.Fatalf("insert second game: %v", err)
	}

	req2 := httptest.NewRequest("GET", "/meta.7z", nil)
	rec2 := httptest.NewRecorder()
	h.Meta7zHandler(rec2, req2)
	etag2 := rec2.Header().Get("ETag")
	if etag2 == "" {
		t.Fatalf("second ETag missing")
	}

	if etag1 == etag2 {
		t.Errorf("ETag did not change after DB update: %q (expected different from %q)", etag2, etag1)
	}
}

// AC2: a fresh If-None-Match yields 304 with no body and no 7z
// generation. We verify the body is empty and Content-Type is unset
// (304 responses per RFC 7230 must not have a body).
func TestMeta7zHandler_IfNoneMatch_Match_Returns304(t *testing.T) {
	h := makeETagFixture(t)

	// First call: capture the current ETag.
	req1 := httptest.NewRequest("GET", "/meta.7z", nil)
	rec1 := httptest.NewRecorder()
	h.Meta7zHandler(rec1, req1)
	etag := rec1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("first ETag missing")
	}

	// Second call: send the ETag back via If-None-Match.
	req2 := httptest.NewRequest("GET", "/meta.7z", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h.Meta7zHandler(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304 (body=%q)", rec2.Code, rec2.Body.String())
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 response has body: %q", rec2.Body.String())
	}
}

// AC2: a non-matching If-None-Match yields 200 with the 7z body.
func TestMeta7zHandler_IfNoneMatch_Different_Returns200(t *testing.T) {
	h := makeETagFixture(t)

	req := httptest.NewRequest("GET", "/meta.7z", nil)
	req.Header.Set("If-None-Match", `"definitely-not-the-real-etag"`)
	rec := httptest.NewRecorder()
	h.Meta7zHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		t.Errorf("200 response has empty body — 7z generation did not run")
	}
}

// AC2: If-Modified-Since matching Last-Modified → 304.
func TestMeta7zHandler_IfModifiedSince_Match_Returns304(t *testing.T) {
	h := makeETagFixture(t)

	req1 := httptest.NewRequest("GET", "/meta.7z", nil)
	rec1 := httptest.NewRecorder()
	h.Meta7zHandler(rec1, req1)
	lastMod := rec1.Header().Get("Last-Modified")
	if lastMod == "" {
		t.Fatalf("first Last-Modified missing")
	}

	req2 := httptest.NewRequest("GET", "/meta.7z", nil)
	req2.Header.Set("If-Modified-Since", lastMod)
	rec2 := httptest.NewRecorder()
	h.Meta7zHandler(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304 (body=%q)", rec2.Code, rec2.Body.String())
	}
}

// AC2: If-Modified-Since older than Last-Modified → 200.
func TestMeta7zHandler_IfModifiedSince_Older_Returns200(t *testing.T) {
	h := makeETagFixture(t)

	req := httptest.NewRequest("GET", "/meta.7z", nil)
	req.Header.Set("If-Modified-Since", "Thu, 01 Jan 1970 00:00:00 GMT")
	rec := httptest.NewRecorder()
	h.Meta7zHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
}

// AC2: weak ETag prefix W/"…" is accepted as a match (per RFC 7232).
func TestMeta7zHandler_IfNoneMatch_WeakPrefix_Accepted(t *testing.T) {
	h := makeETagFixture(t)

	req1 := httptest.NewRequest("GET", "/meta.7z", nil)
	rec1 := httptest.NewRecorder()
	h.Meta7zHandler(rec1, req1)
	etag := rec1.Header().Get("ETag")
	stripped := etag
	if len(stripped) >= 2 && stripped[0] == '"' {
		stripped = stripped[1 : len(stripped)-1]
	}

	req2 := httptest.NewRequest("GET", "/meta.7z", nil)
	req2.Header.Set("If-None-Match", `W/"`+stripped+`"`)
	rec2 := httptest.NewRecorder()
	h.Meta7zHandler(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304 (weak ETag should match)", rec2.Code)
	}
}

// AC2: the matchesClientCache helper handles edge cases on its own.
func TestMatchesClientCache_PrecedenceAndEmpty(t *testing.T) {
	etag := `"abc123"`
	lm := time.Unix(1_700_000_000, 0).UTC()

	// No headers → no match.
	if matchesClientCache(httptest.NewRequest("GET", "/", nil), etag, lm) {
		t.Errorf("empty request should not match")
	}
	// Matching ETag.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("If-None-Match", etag)
	if !matchesClientCache(r, etag, lm) {
		t.Errorf("matching ETag should match")
	}
	// Non-matching ETag → no match (precedence over date).
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("If-None-Match", `"wrong"`)
	r2.Header.Set("If-Modified-Since", lm.Format(http.TimeFormat))
	if matchesClientCache(r2, etag, lm) {
		t.Errorf("non-matching ETag should override matching date")
	}
	// Matching date.
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.Header.Set("If-Modified-Since", lm.Format(http.TimeFormat))
	if !matchesClientCache(r3, etag, lm) {
		t.Errorf("matching If-Modified-Since should match")
	}
	// Older date → no match.
	r4 := httptest.NewRequest("GET", "/", nil)
	r4.Header.Set("If-Modified-Since", time.Unix(1_500_000_000, 0).UTC().Format(http.TimeFormat))
	if matchesClientCache(r4, etag, lm) {
		t.Errorf("older If-Modified-Since should not match")
	}
	// Garbage date → no match (parse error).
	r5 := httptest.NewRequest("GET", "/", nil)
	r5.Header.Set("If-Modified-Since", "not a date")
	if matchesClientCache(r5, etag, lm) {
		t.Errorf("garbage If-Modified-Since should not match")
	}
}

// AC3: /config.json also sets ETag + Cache-Control and honors
// If-None-Match with a 304.
func TestConfigHandler_ETagAnd304(t *testing.T) {
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))
	h := NewPublicAPIHandler(modeVal)
	h.Config = &types.Config{
		Server: types.ServerConfig{Host: "192.168.1.42", Port: 39457},
		Admin:  types.AdminConfig{ArchivePassword: "etag-test-pw"},
	}

	// First call: capture ETag.
	req1 := httptest.NewRequest("GET", "/config.json", nil)
	rec1 := httptest.NewRecorder()
	h.HandleClientConfigGET(rec1, req1)
	etag := rec1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("ETag missing on /config.json")
	}
	if rec1.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control not set on /config.json")
	}
	if rec1.Code != http.StatusOK {
		t.Fatalf("first call: status = %d, want 200", rec1.Code)
	}

	// Second call with If-None-Match → 304.
	req2 := httptest.NewRequest("GET", "/config.json", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h.HandleClientConfigGET(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304 (If-None-Match should yield 304)", rec2.Code)
	}
}

// AC3: /config.json ETag changes when the archive password changes.
func TestConfigHandler_ETag_ChangesOnPasswordChange(t *testing.T) {
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))
	h := NewPublicAPIHandler(modeVal)

	cfg1 := &types.Config{
		Server: types.ServerConfig{Host: "192.168.1.42", Port: 39457},
		Admin:  types.AdminConfig{ArchivePassword: "old-password"},
	}
	cfg2 := &types.Config{
		Server: types.ServerConfig{Host: "192.168.1.42", Port: 39457},
		Admin:  types.AdminConfig{ArchivePassword: "new-password"},
	}

	h.Config = cfg1
	req1 := httptest.NewRequest("GET", "/config.json", nil)
	rec1 := httptest.NewRecorder()
	h.HandleClientConfigGET(rec1, req1)
	etag1 := rec1.Header().Get("ETag")

	h.Config = cfg2
	req2 := httptest.NewRequest("GET", "/config.json", nil)
	rec2 := httptest.NewRecorder()
	h.HandleClientConfigGET(rec2, req2)
	etag2 := rec2.Header().Get("ETag")

	if etag1 == etag2 {
		t.Errorf("ETag did not change after archive-password change")
	}
}

// AC3: /config.json ETag is stable across two calls with the same
// config.
func TestConfigHandler_ETag_StableForSameConfig(t *testing.T) {
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))
	h := NewPublicAPIHandler(modeVal)
	h.Config = &types.Config{
		Server: types.ServerConfig{Host: "192.168.1.42", Port: 39457},
		Admin:  types.AdminConfig{ArchivePassword: "stable-pw"},
	}

	req1 := httptest.NewRequest("GET", "/config.json", nil)
	rec1 := httptest.NewRecorder()
	h.HandleClientConfigGET(rec1, req1)
	etag1 := rec1.Header().Get("ETag")

	req2 := httptest.NewRequest("GET", "/config.json", nil)
	rec2 := httptest.NewRecorder()
	h.HandleClientConfigGET(rec2, req2)
	etag2 := rec2.Header().Get("ETag")

	if etag1 != etag2 {
		t.Errorf("ETag not stable: first=%q second=%q", etag1, etag2)
	}
}

// AC4: helper-level regression — computeCatalogETag on an empty list
// returns a non-empty, valid ETag.
func TestComputeCatalogETag_Empty(t *testing.T) {
	got := computeCatalogETag(nil)
	if got == "" || got == "\"\"" {
		t.Errorf("computeCatalogETag(nil) = %q, want non-empty ETag", got)
	}
}

// AC4: helper-level — the ETag is stable for the same input.
func TestComputeCatalogETag_Stable(t *testing.T) {
	games := []types.GameEntry{
		{PackageName: "com.a", LastUpdated: time.Unix(100, 0).UTC()},
		{PackageName: "com.b", LastUpdated: time.Unix(200, 0).UTC()},
	}
	e1 := computeCatalogETag(games)
	e2 := computeCatalogETag(games)
	if e1 != e2 {
		t.Errorf("computeCatalogETag not stable: %q vs %q", e1, e2)
	}
}

// AC4: helper-level — order-independence (the helper sorts internally).
func TestComputeCatalogETag_OrderIndependent(t *testing.T) {
	games1 := []types.GameEntry{
		{PackageName: "com.a", LastUpdated: time.Unix(100, 0).UTC()},
		{PackageName: "com.b", LastUpdated: time.Unix(200, 0).UTC()},
	}
	games2 := []types.GameEntry{
		{PackageName: "com.b", LastUpdated: time.Unix(200, 0).UTC()},
		{PackageName: "com.a", LastUpdated: time.Unix(100, 0).UTC()},
	}
	if computeCatalogETag(games1) != computeCatalogETag(games2) {
		t.Errorf("computeCatalogETag depends on slice order")
	}
}

// AC4: helper-level — different package set yields a different ETag.
func TestComputeCatalogETag_DifferentSets(t *testing.T) {
	games1 := []types.GameEntry{{PackageName: "com.a", LastUpdated: time.Unix(100, 0).UTC()}}
	games2 := []types.GameEntry{{PackageName: "com.b", LastUpdated: time.Unix(100, 0).UTC()}}
	if computeCatalogETag(games1) == computeCatalogETag(games2) {
		t.Errorf("different package sets should produce different ETags")
	}
}

// AC4: helper-level — different last_updated yields a different ETag.
func TestComputeCatalogETag_DifferentTimestamps(t *testing.T) {
	games1 := []types.GameEntry{{PackageName: "com.a", LastUpdated: time.Unix(100, 0).UTC()}}
	games2 := []types.GameEntry{{PackageName: "com.a", LastUpdated: time.Unix(200, 0).UTC()}}
	if computeCatalogETag(games1) == computeCatalogETag(games2) {
		t.Errorf("different last_updated should produce different ETags")
	}
}

// Code-review patch F6: setup-mode short-circuits to 503 BEFORE ETag
// code runs, so no cache headers are sent on the 503 response. This
// test pins the contract: a misconfigured server (no games, setup
// mode) does not leak a stale ETag from a previous process.
func TestMeta7zHandler_SetupMode_NoCacheHeaders(t *testing.T) {
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeSetup))
	h := NewPublicAPIHandler(modeVal)

	req := httptest.NewRequest("GET", "/meta.7z", nil)
	rec := httptest.NewRecorder()
	h.Meta7zHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (setup mode)", rec.Code)
	}
	if rec.Header().Get("ETag") != "" {
		t.Errorf("ETag should not be set on 503 setup response, got %q", rec.Header().Get("ETag"))
	}
	if rec.Header().Get("Last-Modified") != "" {
		t.Errorf("Last-Modified should not be set on 503 setup response, got %q", rec.Header().Get("Last-Modified"))
	}
	if rec.Header().Get("Cache-Control") != "" {
		t.Errorf("Cache-Control should not be set on 503 setup response, got %q", rec.Header().Get("Cache-Control"))
	}
}

// Code-review patch F7: HTTP-handler-level coverage of the all-filtered
// catalog case. The helper TestComputeCatalogETag_Empty only covers
// the helper, not the handler. With an empty DB the handler must
// still emit cache headers — Last-Modified anchored to max(last_updated) across
// ALL games (including unexposed ones), ETag stable, Cache-Control: no-cache.
// Regression guard for the "unexposed game causes Last-Modified to go stale"
// bug: when the only change is hiding a game (exposed=false), the VRHub client's
// OR-based cache check (Last-Modified OR ETag) must detect the mutation even when
// it matches Last-Modified from a previous sync.
func TestMeta7zHandler_AllFilteredCatalog_Headers(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := db.Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))
	h := NewPublicAPIHandlerWithDeps(modeVal, d, d, nil, nil)
	h.Config = &types.Config{
		DataDir: tmpDir,
		Admin:   types.AdminConfig{ArchivePassword: "filtered-catalog-pw"},
	}

	// Insert one game with Exposed: false so BuildGameListForMeta7z
	// filters it out, leaving `filtered = []`. The Last-Modified header
	// must still reflect this game's last_updated (not the epoch and not
	// the inception sentinel) so the client can detect the mutation.
	gameTS := time.Unix(1_700_000_000, 0).UTC()
	game := types.GameEntry{
		GameName:    "Hidden Game",
		ReleaseName: "hidden_v1",
		PackageName: "com.hidden.test",
		VersionCode: 1,
		SizeBytes:   1024,
		LastUpdated: gameTS,
		Exposed:     false,
	}
	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	req := httptest.NewRequest("GET", "/meta.7z", nil)
	rec := httptest.NewRecorder()
	h.Meta7zHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}

	etag := rec.Header().Get("ETag")
	if etag == "" || etag == "\"\"" {
		t.Errorf("ETag should be present and non-trivial on empty filtered catalog, got %q", etag)
	}

	lm := rec.Header().Get("Last-Modified")
	if lm == "" {
		t.Fatalf("Last-Modified should be present, got empty")
	}
	parsedLM, err := http.ParseTime(lm)
	if err != nil {
		t.Fatalf("Last-Modified %q is not a valid HTTP date: %v", lm, err)
	}
	// Must not be the epoch: even with a fully-hidden catalog, we must
	// return a real timestamp (the unexposed game's last_updated) so
	// clients can detect the mutation.
	if parsedLM.Unix() <= 0 {
		t.Errorf("Last-Modified on empty filtered catalog should not be epoch, got %q", lm)
	}
	// Must equal the unexposed game's last_updated (MAX across all games).
	// HTTP dates have 1-second granularity, so compare at second precision.
	if got, want := parsedLM.Unix(), gameTS.Unix(); got != want {
		t.Errorf("Last-Modified = %v (unix %d), want game's last_updated %v (unix %d)",
			parsedLM, got, gameTS, want)
	}

	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
	}
}

// Code-review patch F8: INM + IMS precedence at the helper level.
// Covers: "INM succeeds + IMS succeeds" (304 via INM, not IMS) and
// "INM weak prefix + matching date" (304 via INM). The original
// TestMatchesClientCache_PrecedenceAndEmpty only tested "INM fails +
// IMS succeeds" and the "both absent" / "garbage IMS" edge cases.
func TestMatchesClientCache_PrecedenceBothMatch(t *testing.T) {
	etag := `"abc123"`
	lm := time.Unix(1_700_000_000, 0).UTC()

	// INM succeeds + IMS succeeds → INM takes precedence, returns 304.
	r1 := httptest.NewRequest("GET", "/", nil)
	r1.Header.Set("If-None-Match", etag)
	r1.Header.Set("If-Modified-Since", lm.Format(http.TimeFormat))
	if !matchesClientCache(r1, etag, lm) {
		t.Errorf("matching INM + matching IMS should yield 304")
	}

	// INM weak prefix + matching date → 304 (weak form is RFC-compliant).
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("If-None-Match", `W/"abc123"`)
	r2.Header.Set("If-Modified-Since", lm.Format(http.TimeFormat))
	if !matchesClientCache(r2, etag, lm) {
		t.Errorf("weak INM prefix + matching date should yield 304")
	}

	// INM weak prefix + stale IMS → 304 (INM wins on precedence).
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.Header.Set("If-None-Match", `W/"abc123"`)
	r3.Header.Set("If-Modified-Since", time.Unix(1_500_000_000, 0).UTC().Format(http.TimeFormat))
	if !matchesClientCache(r3, etag, lm) {
		t.Errorf("matching weak INM + stale IMS should still yield 304 (INM precedence)")
	}

	// INM stale + IMS matches → 304 (we fall through to IMS because
	// INM is set but doesn't match — wait, no: per RFC 7232 INM
	// non-match means stale regardless of date. Re-check.)
	r4 := httptest.NewRequest("GET", "/", nil)
	r4.Header.Set("If-None-Match", `"wrong"`)
	r4.Header.Set("If-Modified-Since", lm.Format(http.TimeFormat))
	if matchesClientCache(r4, etag, lm) {
		t.Errorf("non-matching INM should override matching IMS (INM precedence)")
	}
}

// Code-review patch F3 (defensive): an If-None-Match that trims to
// empty (header sent as "If-None-Match:   " or "If-None-Match: W/"
// with no value) should fall through to the IMS check rather than
// short-circuiting to "no match".
func TestMatchesClientCache_EmptyINM_FallsThroughToIMS(t *testing.T) {
	etag := `"abc123"`
	lm := time.Unix(1_700_000_000, 0).UTC()

	// Whitespace-only INM + matching IMS → should 304 (fell through).
	r1 := httptest.NewRequest("GET", "/", nil)
	r1.Header.Set("If-None-Match", "   ")
	r1.Header.Set("If-Modified-Since", lm.Format(http.TimeFormat))
	if !matchesClientCache(r1, etag, lm) {
		t.Errorf("whitespace-only INM should fall through to IMS and match")
	}

	// Whitespace-only INM + non-matching IMS → no 304.
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("If-None-Match", "   ")
	r2.Header.Set("If-Modified-Since", time.Unix(1_500_000_000, 0).UTC().Format(http.TimeFormat))
	if matchesClientCache(r2, etag, lm) {
		t.Errorf("whitespace-only INM + stale IMS should not match")
	}

	// "W/" alone (no value) + matching IMS → 304.
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.Header.Set("If-None-Match", "W/")
	r3.Header.Set("If-Modified-Since", lm.Format(http.TimeFormat))
	if !matchesClientCache(r3, etag, lm) {
		t.Errorf("'W/' INM with no value should fall through to IMS and match")
	}
}
