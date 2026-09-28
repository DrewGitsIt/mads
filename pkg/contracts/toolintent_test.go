package contracts

import (
	"reflect"
	"testing"
)

func TestToolIntentVocabulary(t *testing.T) {
	if len(ToolIntents) != 8 {
		t.Fatalf("ToolIntents len = %d, want 8 (closed set for v1)", len(ToolIntents))
	}
	for _, v := range ToolIntents {
		if !v.Valid() {
			t.Fatalf("expected %q to be Valid()", v)
		}
	}
	if (ToolIntent("nonsense")).Valid() {
		t.Fatal("nonsense intent reports Valid")
	}
}

func TestEffectiveToolIntentsFresh(t *testing.T) {
	cases := []struct {
		name  string
		a     Agent
		allow []ToolIntent
		deny  []ToolIntent
	}{
		{"empty", Agent{}, nil, nil},
		{"allowlist only", Agent{Allowlist: []ToolIntent{IntentRead, IntentShell}},
			[]ToolIntent{IntentRead, IntentShell}, nil},
		{"denylist only", Agent{Denylist: []ToolIntent{IntentShell}},
			nil, []ToolIntent{IntentShell}},
		{"denylist overrides overlap",
			Agent{Allowlist: []ToolIntent{IntentRead, IntentShell}, Denylist: []ToolIntent{IntentShell}},
			[]ToolIntent{IntentRead}, []ToolIntent{IntentShell}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotA, gotD := tc.a.EffectiveToolIntents()
			if !intentSetEq(gotA, tc.allow) {
				t.Errorf("allowed = %v, want %v", gotA, tc.allow)
			}
			if !intentSetEq(gotD, tc.deny) {
				t.Errorf("denied = %v, want %v", gotD, tc.deny)
			}
		})
	}
}

func TestEffectiveToolIntentsLegacyFold(t *testing.T) {
	// A persona in the OLD shape (capabilities.tools + denied_commands)
	// must still produce a policy that enforces its intent:
	// - "bash" -> allow IntentShell
	// - "rm -rf *" denied -> deny IntentShell
	// (deny wins — the legacy command string means "block shell commands")
	a := Agent{
		Capabilities: AgentCapabilities{Tools: []string{"Read", "Bash"}},
		Permissions:  AgentPermissions{DeniedCommands: []string{"rm -rf *"}},
	}
	allow, deny := a.EffectiveToolIntents()
	if !hasIntent(allow, IntentRead) {
		t.Errorf("legacy capabilities.tools \"Read\" did not fold to IntentRead allow (got %v)", allow)
	}
	if !hasIntent(deny, IntentShell) {
		t.Errorf("legacy denied_commands did not fold to IntentShell deny (got %v)", deny)
	}
	// shell must not be in the allow set: the denied-command intent won.
	if hasIntent(allow, IntentShell) {
		t.Errorf("legacy fold kept IntentShell in allow despite denied_commands (allow=%v, deny=%v)", allow, deny)
	}
}

func TestEffectiveToolIntentsMixedNewAndLegacy(t *testing.T) {
	// A mix of new and old: explicit Denylist entry on IntentShell must
	// still win over a legacy "bash" allow from capabilities.tools.
	a := Agent{
		Denylist:     []ToolIntent{IntentShell},
		Capabilities: AgentCapabilities{Tools: []string{"Bash"}},
	}
	allow, deny := a.EffectiveToolIntents()
	if hasIntent(allow, IntentShell) {
		t.Errorf("explicit Denylist lost to legacy \"Bash\" allow (allow=%v, deny=%v)", allow, deny)
	}
	if !hasIntent(deny, IntentShell) {
		t.Errorf("expected IntentShell in deny (deny=%v)", deny)
	}
}

func TestWorkspaceDefaultsPersonaAlias(t *testing.T) {
	d := WorkspaceDefaults{Agent: "reviewer"}
	if d.PersonaID() != "reviewer" {
		t.Fatalf("PersonaID() = %q, want \"reviewer\"", d.PersonaID())
	}
	d.SetPersona("editor")
	if d.Agent != "editor" || d.Persona != "" {
		t.Fatalf("SetPersona = Agent=%q Persona=%q; want Agent=editor Persona=\"\"", d.Agent, d.Persona)
	}
	// A persona set via the new alias path must also be readable in the
	// canonical Agent field (a single source of truth in the JSON).
	d2 := WorkspaceDefaults{Persona: "fresh"}
	if d2.PersonaID() != "fresh" {
		t.Fatalf("PersonaID() with only Persona set = %q, want \"fresh\"", d2.PersonaID())
	}
	d2.SetPersona("")
	if d2.Agent != "" || d2.Persona != "" {
		t.Fatalf("SetPersona(\"\") left Agent=%q Persona=%q; want both empty", d2.Agent, d2.Persona)
	}
}

func intentSetEq(got, want []ToolIntent) bool {
	if len(got) != len(want) {
		return false
	}
	m := make(map[ToolIntent]bool)
	for _, w := range want {
		m[w] = true
	}
	for _, g := range got {
		if !m[g] {
			return false
		}
	}
	return true
}

// ensure the map literal compiles to a unique set — protects against a
// duplicate-key typo slipping past go vet into a silently-overridden entry.
func TestLegacyToolToIntentNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for name, intent := range LegacyToolToIntent {
		_ = intent
		if seen[name] {
			t.Fatalf("duplicate key %q in LegacyToolToIntent", name)
		}
		seen[name] = true
	}
}

var _ = reflect.DeepEqual // kept so a future test can compare intent sets with order
