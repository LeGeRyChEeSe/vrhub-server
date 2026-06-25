package trailers

import (
	"strings"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

func TestSearchURL(t *testing.T) {
	tests := []struct {
		name      string
		gameName  string
		language  string
		wantEmpty bool
		contains  []string
		omits     []string
	}{
		{
			name:     "name and language",
			gameName: "Beat Saber",
			language: "fr",
			contains: []string{"https://www.youtube.com/results?", "search_query=Beat+Saber+trailer", "hl=fr"},
		},
		{
			name:     "name without language",
			gameName: "SUPERHOT VR",
			language: "",
			contains: []string{"search_query=SUPERHOT+VR+trailer"},
			omits:    []string{"hl="},
		},
		{
			name:     "language is trimmed",
			gameName: "Game",
			language: "  pt-BR  ",
			contains: []string{"hl=pt-BR"},
		},
		{
			name:      "blank name yields empty",
			gameName:  "   ",
			language:  "en",
			wantEmpty: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SearchURL(tc.gameName, tc.language)
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("SearchURL = %q, want empty", got)
				}
				return
			}
			for _, c := range tc.contains {
				if !strings.Contains(got, c) {
					t.Errorf("SearchURL = %q, want it to contain %q", got, c)
				}
			}
			for _, o := range tc.omits {
				if strings.Contains(got, o) {
					t.Errorf("SearchURL = %q, want it NOT to contain %q", got, o)
				}
			}
		})
	}
}

func TestEffectiveTrailerURL(t *testing.T) {
	const resolved = "https://www.youtube.com/watch?v=ABCDEFGHIJK"

	// Resolved/override URL wins verbatim and is not replaced.
	if got := EffectiveTrailerURL(types.GameEntry{GameName: "X", TrailerURL: resolved}, "fr"); got != resolved {
		t.Errorf("with resolved URL = %q, want %q", got, resolved)
	}

	// No resolved URL but a name → search link.
	got := EffectiveTrailerURL(types.GameEntry{GameName: "Trombone Champ"}, "en")
	if !strings.Contains(got, "youtube.com/results") || !strings.Contains(got, "Trombone+Champ+trailer") {
		t.Errorf("fallback = %q, want a YouTube search link", got)
	}

	// No resolved URL and no name → empty.
	if got := EffectiveTrailerURL(types.GameEntry{GameName: ""}, "en"); got != "" {
		t.Errorf("nameless empty-trailer = %q, want empty", got)
	}
}
