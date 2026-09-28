package claudecode

import (
	"testing"

	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/provider"
)

func TestToolIntentResolverConformance(t *testing.T) {
	p := testProvider(t)
	conformance.CheckToolIntents(t, p)

	m := p.ToolIntentMap()
	// The persona-level closed set for tool intents is 8 entries;
	// skill/mcp are not concrete claude-code tools (they are capability-
	// registry concerns wired via opts.MCPServers), so this adapter maps
	// exactly the 6 concrete-tool intents.
	if got, want := len(m), 6; got != want {
		t.Fatalf("ToolIntentMap has %d entries, want %d (v1 closed set minus skill/mcp)", got, want)
	}
	// Native "shell" enforcement is declared in IntentMatrix (Native) AND
	// must be named in the resolver map — the two surfaces have to agree
	// about what "shell" means on claude-code.
	if _, ok := m[provider.ToolIntentShell]; !ok {
		t.Fatal("ToolIntentMap must declare ToolIntentShell (IntentMatrix declares IntentDenyCommands: Native)")
	}
}
