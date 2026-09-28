package conformance

import (
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// CheckToolIntents is the agent-concept redesign §2 structural assertion:
// a provider's ToolIntentMap must be self-consistent (no unknown intents,
// no empty mapping rows). It does NOT cross-check against which intents a
// provider claims to enforce in its Matrix — that is the adapters' own
// judgment call (some harnesses map an intent but don't enforce the
// restriction natively — e.g. a prompt-level "don't shell" is a mapping
// that also functions as a native restriction, but some harnesses map
// without enforcing and leave enforcement to the trust layer).
//
// Callers: each adapter's test package runs this for its own provider
// (see providers/{claudecode,goosecli,kirocli,fake}). A provider that
// does NOT implement provider.ToolIntentResolver is a no-op here: it has
// taken no stance, which is the same as today's behavior and is fine.
func CheckToolIntents(t *testing.T, p provider.Provider) {
	t.Helper()
	r, ok := p.(provider.ToolIntentResolver)
	if !ok {
		t.Skipf("provider %q does not implement provider.ToolIntentResolver — no stance, no check", p.Name())
	}
	m := r.ToolIntentMap()
	if err := m.Validate(); err != nil {
		t.Errorf("ToolIntentMap: %v", err)
	}
	// Every declared intent must be in the closed set — the closed set
	// is the contract surface the persona config exposes (UI enumeration
	// comes from contracts.ToolIntents, which mirrors provider.ToolIntents).
	for intent := range m {
		if !knownProviderToolIntent(intent) {
			t.Errorf("ToolIntentMap declares unknown intent %q (not in the v1 closed set)", intent)
		}
	}
}

func knownProviderToolIntent(i provider.ToolIntent) bool {
	for _, v := range provider.ToolIntentVocabulary {
		if v == i {
			return true
		}
	}
	return false
}
