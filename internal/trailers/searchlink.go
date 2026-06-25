package trailers

import (
	"net/url"
	"strings"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// SearchURL builds a YouTube search URL for a game's trailer in the given
// language. It is the zero-config fallback of the hybrid trailer feature
// (Story 11.3): every game gets at least this link, with no API key required.
// The client opens it externally, landing the user on YouTube's results for the
// game's trailer.
//
// language is an optional BCP-47 / ISO-639 hint (e.g. "fr") passed as the
// YouTube "hl" UI-language parameter; empty leaves it to the user's YouTube
// locale. Returns "" when gameName is blank (a nameless game has nothing to
// search for).
func SearchURL(gameName, language string) string {
	name := strings.TrimSpace(gameName)
	if name == "" {
		return ""
	}
	q := url.Values{}
	q.Set("search_query", name+" trailer")
	if l := strings.TrimSpace(language); l != "" {
		q.Set("hl", l)
	}
	return "https://www.youtube.com/results?" + q.Encode()
}

// EffectiveTrailerURL is the URL the delivery layer (meta.7z, /{hash}/trailer.txt,
// directory listing) should expose for a game, implementing the hybrid policy
// (Story 11.3):
//   - a resolved/override trailer URL (a specific YouTube video) when present;
//   - otherwise a YouTube search link for "{gameName} trailer" in `language`.
//
// Returns "" only when there is neither a resolved URL nor a usable game name
// (so a nameless game adds nothing). A resolved URL is used verbatim and is NOT
// replaced — adding an API key later upgrades the search-link games to specific
// videos via the resolver (which only fills empty trailer_url), without
// touching games already resolved.
func EffectiveTrailerURL(game types.GameEntry, language string) string {
	if strings.TrimSpace(game.TrailerURL) != "" {
		return game.TrailerURL
	}
	return SearchURL(game.GameName, language)
}
