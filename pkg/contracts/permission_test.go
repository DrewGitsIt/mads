package contracts_test

import (
	"reflect"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestSessionTrustIntents(t *testing.T) {
	cases := []struct {
		trust contracts.SessionTrust
		want  contracts.IntentSet
	}{
		{contracts.TrustFull, contracts.IntentSet{}},
		{"", contracts.IntentSet{}},
		// Limited asks before commands rather than denying them.
		{contracts.TrustLimited, contracts.IntentSet{
			contracts.IntentRequireApproval: true,
		}},
		{contracts.TrustReadOnly, contracts.IntentSet{
			contracts.IntentReadOnly:        true,
			contracts.IntentRequireApproval: true,
		}},
	}
	for _, c := range cases {
		got := c.trust.Intents()
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SessionTrust(%q).Intents() = %v, want %v", c.trust, got, c.want)
		}
	}
}

func TestAgentPermissionsIntents(t *testing.T) {
	cases := []struct {
		name  string
		perms contracts.AgentPermissions
		want  contracts.IntentSet
	}{
		{"empty", contracts.AgentPermissions{}, contracts.IntentSet{}},
		{"denied commands", contracts.AgentPermissions{DeniedCommands: []string{"rm"}}, contracts.IntentSet{
			contracts.IntentDenyCommands: true,
		}},
		{"allowed paths", contracts.AgentPermissions{AllowedPaths: []string{"/repo"}}, contracts.IntentSet{
			contracts.IntentPathScope: true,
		}},
		{"both", contracts.AgentPermissions{DeniedCommands: []string{"rm"}, AllowedPaths: []string{"/repo"}}, contracts.IntentSet{
			contracts.IntentDenyCommands: true,
			contracts.IntentPathScope:    true,
		}},
	}
	for _, c := range cases {
		got := c.perms.Intents()
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: AgentPermissions.Intents() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEffectiveIntents(t *testing.T) {
	// A read_only trust session combined with a command blocklist and a path
	// scope: the effective policy carries every restriction either source
	// imposes — the union, i.e. the most-restrictive intersection of what
	// each side would otherwise permit.
	got := contracts.EffectiveIntents(contracts.TrustReadOnly, contracts.AgentPermissions{
		DeniedCommands: []string{"rm"},
		AllowedPaths:   []string{"/repo"},
	})
	want := contracts.IntentSet{
		contracts.IntentReadOnly:        true,
		contracts.IntentRequireApproval: true,
		contracts.IntentDenyCommands:    true,
		contracts.IntentPathScope:       true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EffectiveIntents(read_only, denied+paths) = %v, want %v", got, want)
	}

	// Full trust with no AgentPermissions declared: no restrictions at all.
	none := contracts.EffectiveIntents(contracts.TrustFull, contracts.AgentPermissions{})
	if len(none) != 0 {
		t.Fatalf("EffectiveIntents(full, none) = %v, want empty", none)
	}

	// Full trust doesn't relax restrictions AgentPermissions itself declares.
	withPerms := contracts.EffectiveIntents(contracts.TrustFull, contracts.AgentPermissions{DeniedCommands: []string{"rm"}})
	if !withPerms.Has(contracts.IntentDenyCommands) {
		t.Fatalf("EffectiveIntents(full, denied commands) = %v, want deny_commands present", withPerms)
	}
}
