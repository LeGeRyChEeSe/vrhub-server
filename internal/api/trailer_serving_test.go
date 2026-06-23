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

// TestServeTrailerFile_NoTrailer404 verifies AC3: a game without a trailer
// returns 404 for /{hash}/trailer.txt.
func TestServeTrailerFile_NoTrailer404(t *testing.T) {
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        trailerTestHash,
			// TrailerURL empty.
		},
	}
	handler := setupFileServerHandler(t, db, &mockFileReader{}, &types.Config{DataDir: t.TempDir()})

	req := httptest.NewRequest("GET", "/"+trailerTestHash+"/trailer.txt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestPackageListing_ShowsTrailerLink verifies AC3: the package listing
// advertises trailer.txt when a trailer URL is set, and omits it otherwise.
func TestPackageListing_ShowsTrailerLink(t *testing.T) {
	tests := []struct {
		name       string
		trailerURL string
		wantLink   bool
	}{
		{"with trailer", "https://www.youtube.com/watch?v=ABCDEFGHIJK", true},
		{"without trailer", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := &mockFileServerDB{
				game: &types.GameEntry{
					GameName:    "Test Game",
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
