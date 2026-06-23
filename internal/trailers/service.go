package trailers

import (
	"context"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// Store is the subset of the DB the batch resolver needs. internal/db.DB
// satisfies it (ListAllGamesOrderedByName + UpdateTrailerURL). Kept as an
// interface so the batch logic is unit-testable without a real SQLite DB.
type Store interface {
	ListAllGamesOrderedByName() ([]types.GameEntry, error)
	UpdateTrailerURL(packageName, url string) error
}

// ResolveMissing runs the resolution cascade for every game whose trailer_url
// is still empty and persists any URL it finds. Games that already have a
// trailer (e.g. from an operator-override sidecar picked up at import) are
// skipped — the cascade only fills gaps, matching the story's "only
// re-resolve when empty" caching rule.
//
// It is best-effort and non-blocking by contract: it never returns an error
// for a per-game "not found" (those are logged at Debug inside Resolve); the
// only returned error is a failure to list games up-front. Callers run it in a
// background goroutine at startup / on the scheduled metadata refresh.
//
// Returns the number of games whose trailer_url was newly set.
func (r *Resolver) ResolveMissing(ctx context.Context, store Store, cfg *types.Config) (int, error) {
	games, err := store.ListAllGamesOrderedByName()
	if err != nil {
		return 0, err
	}

	var resolved int
	for _, game := range games {
		select {
		case <-ctx.Done():
			vlog.Get().Debug().Int("resolved", resolved).Msg("trailer: batch resolution cancelled")
			return resolved, ctx.Err()
		default:
		}

		// Caching rule: skip games that already have a trailer.
		if game.TrailerURL != "" {
			continue
		}

		url, rErr := r.Resolve(ctx, game, cfg)
		if rErr != nil {
			// best-effort: a YouTube fault logs and we move on.
			vlog.Get().Debug().Err(rErr).Str("package", game.PackageName).Msg("trailer: resolution error, skipping game")
			continue
		}
		if url == "" {
			continue
		}
		if uErr := store.UpdateTrailerURL(game.PackageName, url); uErr != nil {
			vlog.Get().Warn().Err(uErr).Str("package", game.PackageName).Msg("trailer: failed to persist resolved URL")
			continue
		}
		resolved++
		vlog.Get().Info().Str("package", game.PackageName).Str("url", url).Msg("trailer: resolved and persisted")
	}

	vlog.Get().Debug().Int("resolved", resolved).Int("scanned", len(games)).Msg("trailer: batch resolution complete")
	return resolved, nil
}
