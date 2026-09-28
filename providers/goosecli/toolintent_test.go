package goosecli

import (
	"testing"

	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/provider"
)

func TestToolIntentResolverConformance(t *testing.T) {
	p := New(Config{})
	conformance.CheckToolIntents(t, p)

	m := p.ToolIntentMap()
	// goose is the reference vocabulary for the v1 closed set; all six
	// concrete-tool intents map to goose built-in tools.
	if got, want := len(m), 6; got != want {
		t.Fatalf("ToolIntentMap has %d entries, want %d (6 of the 8 v1 intents — skill/mcp are capability-registry concerns)", got, want)
	}
	if _, ok := m[provider.ToolIntentShell]; !ok {
		t.Fatal("goose must map ToolIntentShell to its built-in 'shell' tool")
	}
}
