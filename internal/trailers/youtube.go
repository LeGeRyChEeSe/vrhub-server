package trailers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// youtubeSearchEndpoint is the YouTube Data API v3 search.list endpoint.
const youtubeSearchEndpoint = "https://www.googleapis.com/youtube/v3/search"

// httpYouTubeSearcher is the production YouTubeSearcher: it calls the YouTube
// Data API v3 search.list endpoint and turns the first video result into a
// canonical watch URL. It is only invoked when cfg.Trailer.YouTubeAPIKey is
// set (step 3 of the cascade).
type httpYouTubeSearcher struct {
	// client is the HTTP client used for the search request. nil → a default
	// client with a short timeout is used (the search response is tiny).
	client *http.Client
	// endpoint overrides youtubeSearchEndpoint for tests. Empty → the real
	// endpoint.
	endpoint string
}

// youtubeSearchResponse is the subset of the search.list response we read.
type youtubeSearchResponse struct {
	Items []struct {
		ID struct {
			Kind    string `json:"kind"`
			VideoID string `json:"videoId"`
		} `json:"id"`
	} `json:"items"`
}

// SearchTrailer searches "{query} trailer" and returns a watch URL for the
// first video result, or "" when none is found. It returns a non-nil error
// only for a real, unexpected fault (network failure, non-200 status, or an
// unparseable body) — the caller treats that as best-effort and leaves the
// trailer empty.
func (s *httpYouTubeSearcher) SearchTrailer(ctx context.Context, apiKey, query, language string) (string, error) {
	if apiKey == "" || query == "" {
		return "", nil
	}

	endpoint := s.endpoint
	if endpoint == "" {
		endpoint = youtubeSearchEndpoint
	}

	q := url.Values{}
	q.Set("part", "snippet")
	q.Set("type", "video")
	q.Set("maxResults", "1")
	q.Set("q", query+" trailer")
	if language != "" {
		q.Set("relevanceLanguage", language)
	}
	q.Set("key", apiKey)

	reqURL := endpoint + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("youtube: build request: %w", err)
	}

	client := s.client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("youtube: search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("youtube: search returned status %d", resp.StatusCode)
	}

	var parsed youtubeSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("youtube: decode response: %w", err)
	}

	for _, item := range parsed.Items {
		if item.ID.VideoID != "" {
			return "https://www.youtube.com/watch?v=" + item.ID.VideoID, nil
		}
	}
	return "", nil
}
