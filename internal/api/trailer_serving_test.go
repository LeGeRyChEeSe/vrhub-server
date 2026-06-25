package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

const trailerTestHash = "abc123def456789012345678abcdef00"

// TestServeTrailerFile_ReturnsURL verifies AC3: GET /{hash}/trailer.txt for a
// game with a resolved TrailerURL returns 200 text/plain with the URL body.
func TestServeTrailerFile_ReturnsURL(t *testing.T) {
	const url = "https://www.youtube.com/watch?v=ABCDEFGHIJK"
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        trailerTestHash,
			TrailerURL:  url,
		},
	}
	handler := setupFileServerHandler(t, db, &mockFileReader{}, &types.Config{DataDir: t.TempDir()})

	req := httptest.NewRequest("GET", "/"+trailerTestHash+"/trailer.txt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
	}
	if body := rec.Body.String(); body != url {
		t.Errorf("body = %q, want %q", body, url)
	}
}

// TestServeTrailerFile_NoTrailer_FallsBackToSearch verifies the Story 11.3
// hybrid behaviour: a NAMED game without a resolved trailer_url returns 200 with
// a YouTube SEARCH link for "{gameName} trailer" (the zero-config fallback).
func TestServeTrailerFile_NoTrailer_FallsBackToSearch(t *testing.T) {
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        trailerTestHash,
			// TrailerURL empty → search-link fallback.
		},
	}
	handler := setupFileServerHandler(t, db, &mockFileReader{}, &types.Config{DataDir: t.TempDir()})

	req := httptest.NewRequest("GET", "/"+trailerTestHash+"/trailer.txt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (search-link fallback)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "youtube.com/results") || !strings.Contains(body, "Test+Game+trailer") {
		t.Errorf("body = %q, want a YouTube search link for the game trailer", body)
	}
}

// TestServeTrailerFile_NoTrailerNoName_404 verifies the only 404 case under the
// hybrid policy: a game with neither a trailer_url nor a game name has nothing
// to search for.
func TestServeTrailerFile_NoTrailerNoName_404(t *testing.T) {
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "", // nameless
			PackageName: "com.test.game",
			Hash:        trailerTestHash,
		},
	}
	handler := setupFileServerHandler(t, db, &mockFileReader{}, &types.Config{DataDir: t.TempDir()})

	req := httptest.NewRequest("GET", "/"+trailerTestHash+"/trailer.txt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no trailer, no name)", rec.Code)
	}
}

// TestPackageListing_ShowsTrailerLink verifies the Story 11.3 hybrid listing:
// trailer.txt is advertised for any named game (resolved URL or search-link
// fallback), and omitted only for a nameless game with no trailer.
func TestPackageListing_ShowsTrailerLink(t *testing.T) {
	tests := []struct {
		name       string
		gameName   string
		trailerURL string
		wantLink   bool
	}{
		{"with resolved trailer", "Test Game", "https://www.youtube.com/watch?v=ABCDEFGHIJK", true},
		{"no trailer, named -> search-link", "Test Game", "", true},
		{"no trailer, nameless", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := &mockFileServerDB{
				game: &types.GameEntry{
					GameName:    tc.gameName,
					PackageName: "com.test.game",
					Hash:        trailerTestHash,
					TrailerURL:  tc.trailerURL,
				},
				packages: []string{"com.test.game"},
			}
			handler := setupFileServerHandler(t, db, &mockFileReader{}, &types.Config{DataDir: t.TempDir()})

			req := httptest.NewRequest("GET", "/"+trailerTestHash+"/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			hasLink := strings.Contains(rec.Body.String(), `href="trailer.txt"`)
			if hasLink != tc.wantLink {
				t.Errorf("listing has trailer.txt link = %v, want %v; body:\n%s", hasLink, tc.wantLink, rec.Body.String())
			}
		})
	}
}
