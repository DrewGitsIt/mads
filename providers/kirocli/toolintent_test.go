package kirocli

import (
	"testing"

	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/provider"
)

func TestToolIntentResolverConformance(t *testing.T) {
	p := New(Config{})
	// The resolver is an interface — this assertion (type-asserts, then
	// delegates to conformance) is what enforces that.
	conformance.CheckToolIntents(t, p)

	// kiro's tool vocabulary is less stable than goose's; the map may be
	// shorter than claudecode's — but it must be self-consistent, and it
	// must declare the shell intent (the most security-relevant intent).
	m := p.ToolIntentMap()
	if _, ok := m[provider.ToolIntentShell]; !ok {
		t.Fatal("kiro must map ToolIntentShell to a concrete run_command tool")
	}
}
