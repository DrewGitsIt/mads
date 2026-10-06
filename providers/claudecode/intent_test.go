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

	// deny_commands: native via --disallowedTools Bash — read-only trust's
	// set, and a persona that denies the "shell" intent.
	if m[provider.IntentDenyCommands] != provider.Native {
		t.Fatalf("IntentDenyCommands = %s, want native", m[provider.IntentDenyCommands])
	}
	if args := strings.Join(trustArgs(provider.TrustReadOnly, false), " "); !strings.Contains(args, "Bash") {
		t.Errorf("trustArgs(read_only) = %q, want Bash disallowed (deny_commands native)", args)
	}
	persona := provider.StartOptions{ToolIntents: provider.ToolIntents{Denied: []provider.ToolIntent{provider.ToolIntentShell}}}
	if got := personaDisallowedTools(persona); !strings.Contains(got, "Bash") {
		t.Errorf("persona denying shell: --disallowedTools %q, want Bash (deny_commands native)", got)
	}

	// read_only: native via --disallowedTools also covering Write/Edit/
	// NotebookEdit — TrustReadOnly leaves no write tool reachable.
	if m[provider.IntentReadOnly] != provider.Native {
		t.Fatalf("IntentReadOnly = %s, want native", m[provider.IntentReadOnly])
	}
	roArgs := strings.Join(trustArgs(provider.TrustReadOnly, false), " ")
	for _, tool := range []string{"Write", "Edit", "NotebookEdit"} {
		if !strings.Contains(roArgs, tool) {
			t.Errorf("trustArgs(read_only) = %q, want %s disallowed (read_only native)", roArgs, tool)
		}
	}

	// require_approval: native via claude's manual permission mode, whose
	// prompts reach the asker over --permission-prompt-tool stdio on chat.
	if m[provider.IntentRequireApproval] != provider.Native {
		t.Fatalf("IntentRequireApproval = %s, want native", m[provider.IntentRequireApproval])
	}
	for _, trust := range []provider.Trust{provider.TrustLimited, provider.TrustReadOnly} {
		args := strings.Join(trustArgs(trust, true), " ")
		if !strings.Contains(args, "--permission-mode manual") || !strings.Contains(args, "--permission-prompt-tool stdio") {
			t.Errorf("trust %s: trustArgs = %q, want manual mode answered over the stdio host channel", trust, args)
		}
	}
	// auto_review compiles to require_approval too: the classifier's
	// escalations reach the user over the same channel.
	if args := strings.Join(trustArgs(provider.TrustAutoReview, true), " "); args != "--permission-mode auto --permission-prompt-tool stdio" {
		t.Errorf("trust auto_review: trustArgs = %q, want auto mode answered over the stdio host channel", args)
	}
	if args := strings.Join(trustArgs(provider.TrustFull, true), " "); strings.Contains(args, "--permission-prompt-tool") {
		t.Errorf("trust full: trustArgs = %q, want no prompt channel (bypassPermissions never asks)", args)
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
		args, path, err := permissionArgs([]string{"src/"})
		if err != nil {
			t.Fatalf("permissionArgs(paths): %v", err)
		}
		if len(args) == 0 || path == "" {
			t.Errorf("permissionArgs(allowlist) returned no --settings wiring, want the path-scope hook file")
		}
		if path != "" {
			defer func() { _ = os.Remove(path) }()
		}
	}
	{
		args, path, err := permissionArgs(nil)
		if err != nil {
			t.Fatalf("permissionArgs(no allowlist): %v", err)
		}
		if len(args) != 0 || path != "" {
			t.Errorf("permissionArgs(no allowlist) = args %v path %q, want none (no hook required)", args, path)
		}
	}
}
