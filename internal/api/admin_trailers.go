package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"time"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/trailers"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// resolveTrailersAsync runs the trailer resolution cascade (Story 11.3) for
// every game that still lacks a resolved trailer_url, in the background
// (best-effort, 5-minute timeout). With a YouTube API key configured it upgrades
// search-link games to specific videos; without one it is effectively a no-op
// (the override sidecar aside). Games already resolved are skipped, so repeated
// calls only cost quota for the still-unresolved games. Safe when h.DB is nil.
func (h *AdminHandler) resolveTrailersAsync(cfg *types.Config) {
	if h.DB == nil || cfg == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		resolver := trailers.New(filepath.Join(h.DataDir, "metadata"))
		n, err := resolver.ResolveMissing(ctx, h.DB, cfg)
		if err != nil {
			vlog.Get().Warn().Err(err).Msg("trailers: background resolution failed")
			return
		}
		vlog.Get().Info().Int("resolved", n).Msg("trailers: background resolution complete")
	}()
}

// HandleTrailersResolvePOST triggers a background trailer resolution pass for
// all games missing a specific trailer video and returns 202 immediately.
// Story 11.3 — backs the admin "Resolve trailers now" action. Without a
// configured YouTube API key this resolves nothing new (games still fall back to
// search links at delivery time), which the response message makes explicit.
func (h *AdminHandler) HandleTrailersResolvePOST(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	cfg, ok := h.resolveConfig()
	if !ok {
		writeError(w, http.StatusInternalServerError, "config not available", "CONFIG_UNAVAILABLE")
		return
	}

	h.resolveTrailersAsync(cfg)

	hasKey := cfg.Trailer.YouTubeAPIKey != ""
	msg := "Trailer resolution started in the background."
	if !hasKey {
		msg = "Started, but no YouTube API key is set — games without a hand-picked trailer keep their YouTube search link. Set a key to resolve specific videos."
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"status":              "resolving",
		"youtube_api_key_set": hasKey,
		"message":             msg,
	})
}
