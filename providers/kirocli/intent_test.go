package kirocli

import (
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

func TestIntentMatrixValid(t *testing.T) {
	p := New(Config{})
	if err := p.IntentMatrix().Validate(); err != nil {
		t.Fatalf("IntentMatrix invalid: %v", err)
	}
}

// TestIntentMatrixDeclaresUnenforced asserts kiro-cli takes the honest
// unenforced stance on every portable permission intent — no command
// blocklist flag, no path-scoped write restriction, no flag that blocks
// writes/commands outright, and (unlike claude-code) no wiring of
// StartOptions.PermissionPromptURL at all, so require_approval has no
// enforcement mechanism to ride.
func TestIntentMatrixDeclaresUnenforced(t *testing.T) {
	p := New(Config{})
	m := p.IntentMatrix()
	for _, i := range provider.Intents {
		if m[i] != provider.Unsupported {
			t.Errorf("IntentMatrix[%s] = %s, want unsupported (unenforced)", i, m[i])
		}
	}
}
