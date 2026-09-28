package claudecode

import (
	"os"
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
		args, path, err := permissionArgs(trust, "http://127.0.0.1:0/permission-hook", nil)
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

	// path_scope: Native via the path-scope PreToolUse hook in
	// permission.go (Option A, agent-concept redesign §2) — a local
	// POSIX sh containment check on Write|Edit|MultiEdit|NotebookEdit
	// wired from PersonaAllowedPaths, independent of trust. Prove the
	// wiring actually fires when a persona allowlist is present (and
	// stays absent when there is none).
	if m[provider.IntentPathScope] != provider.Native {
		t.Fatalf("IntentPathScope = %s, want native", m[provider.IntentPathScope])
	}
	{
		args, path, err := permissionArgs(provider.TrustFull, "", []string{"src/"})
		if err != nil {
			t.Fatalf("permissionArgs(TrustFull, paths): %v", err)
		}
		if len(args) == 0 || path == "" {
			t.Errorf("permissionArgs(TrustFull, allowlist) returned no --settings wiring, want the path-scope hook file")
		}
		if path != "" {
			defer func() { _ = os.Remove(path) }()
		}
	}
	{
		args, path, err := permissionArgs(provider.TrustFull, "", nil)
		if err != nil {
			t.Fatalf("permissionArgs(TrustFull, no allowlist): %v", err)
		}
		if len(args) != 0 || path != "" {
			t.Errorf("permissionArgs(TrustFull, no allowlist) = args %v path %q, want none (no hook required)", args, path)
		}
	}
}
