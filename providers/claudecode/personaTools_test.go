package claudecode

import (
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

func TestPersonaDisallowedTools(t *testing.T) {
	cases := []struct {
		name string
		ti   provider.ToolIntents
		want string
	}{
		{"zero value, no persona",
			provider.ToolIntents{}, ""},
		{"deny shell -> Bash",
			provider.ToolIntents{Denied: []provider.ToolIntent{provider.ToolIntentShell}},
			"Bash"},
		{"deny write + edit -> Edit,Write",
			provider.ToolIntents{Denied: []provider.ToolIntent{provider.ToolIntentWrite, provider.ToolIntentEdit}},
			"Edit,Write"},
		// sort order is alphabetical by tool name (Bash, Edit, Glob, Grep,
		// WebFetch, Write) — the test pins the stable output ordering.
		{"allow read+search -> disallow Bash,Edit,Write,WebFetch (Glob/Grep allowed)",
			provider.ToolIntents{Allowed: []provider.ToolIntent{provider.ToolIntentRead, provider.ToolIntentSearch}},
			"Bash,Edit,WebFetch,Write"},
		{"allow all six concrete-tool intents -> no disallows (allow-all)",
			provider.ToolIntents{Allowed: []provider.ToolIntent{
				provider.ToolIntentRead, provider.ToolIntentWrite, provider.ToolIntentEdit,
				provider.ToolIntentShell, provider.ToolIntentSearch, provider.ToolIntentFetch,
			}},
			""},
		{"deny write beats allow-all",
			provider.ToolIntents{
				Allowed: []provider.ToolIntent{
					provider.ToolIntentRead, provider.ToolIntentWrite, provider.ToolIntentEdit,
					provider.ToolIntentShell, provider.ToolIntentSearch, provider.ToolIntentFetch,
				},
				Denied: []provider.ToolIntent{provider.ToolIntentWrite},
			},
			"Write"},
		{"deny fetch -> WebFetch",
			provider.ToolIntents{Denied: []provider.ToolIntent{provider.ToolIntentFetch}},
			"WebFetch"},
		{"search has two tools -> both",
			provider.ToolIntents{Denied: []provider.ToolIntent{provider.ToolIntentSearch}},
			"Glob,Grep"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := personaDisallowedTools(provider.StartOptions{ToolIntents: tc.ti})
			if got != tc.want {
				t.Errorf("personaDisallowedTools = %q, want %q", got, tc.want)
			}
		})
	}
}
