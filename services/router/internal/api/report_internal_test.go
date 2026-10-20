package api

import (
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/goosecli"
	"github.com/drewkidwell/mads/providers/hermescli"
	"github.com/drewkidwell/mads/providers/kirocli"
	"github.com/drewkidwell/mads/providers/provider"
)

// "Compact now" follows the provider matrix's compaction row: offered for a
// harness that compacts natively (claude-code, goose), never for one that
// declares it unsupported (kiro-cli) or one this Router doesn't know.
func TestCanCompactFollowsMatrix(t *testing.T) {
	reg := provider.NewRegistry()
	for _, p := range []provider.Provider{
		claudecode.New(claudecode.Config{}),
		goosecli.New(goosecli.Config{}),
		hermescli.New(hermescli.Config{}),
		kirocli.New(kirocli.Config{}),
	} {
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	s := &server{providers: reg}
	for name, want := range map[string]bool{
		"claude-code": true,
		"goose":       true,
		"hermes":      false,
		"kiro-cli":    false,
		"nonesuch":    false,
		"":            false,
	} {
		if got := s.canCompact(contracts.Session{Provider: name}); got != want {
			t.Errorf("canCompact(%q) = %v, want %v", name, got, want)
		}
	}
	if (&server{}).canCompact(contracts.Session{Provider: "claude-code"}) {
		t.Error("canCompact with no provider registry = true, want false")
	}
}
