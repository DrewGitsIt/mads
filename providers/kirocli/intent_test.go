package kirocli

import (
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

func TestIntentMatrixValid(t *testing.T) {
	p := New(Config{})
	if err := p.IntentMatrix().Validate(); err != nil {
		t.Fatalf("IntentMatrix invalid: %v", err)
	}
}

// TestIntentMatrixDeclarations asserts kiro-cli's stance on the portable
// permission intents: require_approval native — kiro's own per-request
// asks reach the user (StartOptions.Permissions on ACP, kiro's prompt on
// PTY, where --trust-all-tools is withheld below full trust) — and the
// honest unenforced stance on the rest: no command blocklist flag, no
// path-scoped write restriction, no flag that blocks writes/commands
// outright.
func TestIntentMatrixDeclarations(t *testing.T) {
	p := New(Config{})
	m := p.IntentMatrix()
	for _, i := range provider.Intents {
		want := provider.Unsupported
		if i == provider.IntentRequireApproval {
			want = provider.Native
		}
		if m[i] != want {
			t.Errorf("IntentMatrix[%s] = %s, want %s", i, m[i], want)
		}
	}
	for _, trust := range []provider.Trust{provider.TrustLimited, provider.TrustReadOnly, provider.TrustAutoReview} {
		args := strings.Join(p.ptyArgs(provider.StartOptions{Trust: trust}), " ")
		if strings.Contains(args, "--trust-all-tools") {
			t.Errorf("trust %s: PTY argv %q trusts every tool, so kiro would never ask (require_approval native)", trust, args)
		}
	}
}
