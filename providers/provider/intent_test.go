package provider

import (
	"maps"
	"strings"
	"testing"
)

func fullIntentMatrix(overrides map[Intent]Support) IntentMatrix {
	m := IntentMatrix{}
	for _, i := range Intents {
		m[i] = Native
	}
	maps.Copy(m, overrides)
	return m
}

func TestIntentMatrixValidate(t *testing.T) {
	cases := []struct {
		name    string
		m       IntentMatrix
		wantErr string // substring; "" = valid
	}{
		{"all native", fullIntentMatrix(nil), ""},
		{"unenforced counts as valid", fullIntentMatrix(map[Intent]Support{IntentPathScope: Unsupported}), ""},
		{"unknown intent", fullIntentMatrix(map[Intent]Support{"telepathy": Native}), `unknown intent "telepathy"`},
		{"invalid support", fullIntentMatrix(map[Intent]Support{IntentReadOnly: "partial"}), `invalid support "partial"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.m.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}

	m := fullIntentMatrix(nil)
	delete(m, IntentDenyCommands)
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), `"deny_commands" not declared`) {
		t.Fatalf("want missing-row error, got %v", err)
	}
}
