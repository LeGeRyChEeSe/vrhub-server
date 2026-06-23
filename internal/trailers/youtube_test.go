package trailers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHTTPYouTubeSearcher_BuildsURLAndParsesResult verifies the real searcher
// builds a correct search.list query (q, relevanceLanguage, type, key) and
// turns the first videoId into a canonical watch URL.
func TestHTTPYouTubeSearcher_BuildsURLAndParsesResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("q"); got != "Cool Game trailer" {
			t.Errorf("q = %q, want \"Cool Game trailer\"", got)
		}
		if got := q.Get("relevanceLanguage"); got != "fr" {
			t.Errorf("relevanceLanguage = %q, want \"fr\"", got)
		}
		if got := q.Get("type"); got != "video" {
			t.Errorf("type = %q, want \"video\"", got)
		}
		if got := q.Get("key"); got != "secret-key" {
			t.Errorf("key = %q, want \"secret-key\"", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[{"id":{"kind":"youtube#video","videoId":"dQw4w9WgXcQ"}}]}`)) //nolint:errcheck
	}))
	defer srv.Close()

	s := &httpYouTubeSearcher{client: srv.Client(), endpoint: srv.URL}
	got, err := s.SearchTrailer(context.Background(), "secret-key", "Cool Game", "fr")
	if err != nil {
		t.Fatalf("SearchTrailer: %v", err)
	}
	const want = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	if got != want {
		t.Errorf("SearchTrailer = %q, want %q", got, want)
	}
}

// TestHTTPYouTubeSearcher_NoResults returns "" with no error when the API
// yields zero items.
func TestHTTPYouTubeSearcher_NoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[]}`)) //nolint:errcheck
	}))
	defer srv.Close()

	s := &httpYouTubeSearcher{client: srv.Client(), endpoint: srv.URL}
	got, err := s.SearchTrailer(context.Background(), "k", "q", "en")
	if err != nil || got != "" {
		t.Errorf("SearchTrailer = (%q, %v), want (\"\", nil)", got, err)
	}
}

// TestHTTPYouTubeSearcher_Non200 returns an error on a non-200 status (e.g.
// quota exceeded / bad key).
func TestHTTPYouTubeSearcher_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	s := &httpYouTubeSearcher{client: srv.Client(), endpoint: srv.URL}
	if _, err := s.SearchTrailer(context.Background(), "k", "q", "en"); err == nil {
		t.Error("expected error on non-200 status")
	}
}
