package claudecode

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

// TestIntentMatrixMatchesArgConstruction asserts every Native row of
// IntentMatrix against the real flag construction it claims to ride —
// declaration and behavior must never drift apart.
func TestIntentMatrixMatchesArgConstruction(t *testing.T) {
	p := New(Config{})
	m := p.IntentMatrix()

	// deny_commands: native via --disallowedTools Bash, present for both
	// trust levels that restrict at all.
	if m[provider.IntentDenyCommands] != provider.Native {
		t.Fatalf("IntentDenyCommands = %s, want native", m[provider.IntentDenyCommands])
	}
	for _, trust := range []provider.Trust{provider.TrustLimited, provider.TrustReadOnly} {
		args := trustArgs(trust)
		if !strings.Contains(strings.Join(args, " "), "Bash") {
			t.Errorf("trust %s: trustArgs = %v, want Bash disallowed (deny_commands native)", trust, args)
		}
	}

	// read_only: native via --disallowedTools also covering Write/Edit/
	// NotebookEdit — TrustReadOnly leaves no write tool reachable.
	if m[provider.IntentReadOnly] != provider.Native {
		t.Fatalf("IntentReadOnly = %s, want native", m[provider.IntentReadOnly])
	}
	roArgs := strings.Join(trustArgs(provider.TrustReadOnly), " ")
	for _, tool := range []string{"Write", "Edit", "NotebookEdit"} {
		if !strings.Contains(roArgs, tool) {
			t.Errorf("trustArgs(read_only) = %q, want %s disallowed (read_only native)", roArgs, tool)
		}
	}

	// require_approval: native via the permission.go PreToolUse hook, wired
	// exactly when needsPermissionHook is true and a callback URL is set.
	if m[provider.IntentRequireApproval] != provider.Native {
		t.Fatalf("IntentRequireApproval = %s, want native", m[provider.IntentRequireApproval])
	}
	for _, trust := range []provider.Trust{provider.TrustLimited, provider.TrustReadOnly} {
		if !needsPermissionHook(trust) {
			t.Errorf("trust %s: needsPermissionHook = false, want true (require_approval native)", trust)
		}
		args, path, err := permissionArgs(trust, "http://127.0.0.1:0/permission-hook")
		if err != nil {
			t.Fatalf("trust %s: permissionArgs: %v", trust, err)
		}
		if len(args) == 0 {
			t.Errorf("trust %s: permissionArgs returned no args, want --settings hook wiring", trust)
		}
		if path == "" {
			t.Errorf("trust %s: permissionArgs returned no settings path", trust)
		}
	}
	if needsPermissionHook(provider.TrustFull) {
		t.Error("trust full: needsPermissionHook = true, want false (bypassPermissions never asks)")
	}

	// path_scope: no claude flag confines writes to an allowed-path set —
	// declared unenforced rather than implied.
	if m[provider.IntentPathScope] != provider.Unsupported {
		t.Fatalf("IntentPathScope = %s, want unsupported", m[provider.IntentPathScope])
	}
}
