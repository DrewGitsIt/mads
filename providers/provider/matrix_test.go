package provider

import (
	"strings"
	"testing"
)

// fullMatrix returns a valid all-native matrix; overrides adjust rows.
func fullMatrix(overrides map[Behavior]Support) Matrix {
	m := Matrix{}
	for _, b := range Behaviors() {
		m[b] = Native
	}
	for b, s := range overrides {
		m[b] = s
	}
	return m
}

func TestMatrixValidate(t *testing.T) {
	cases := []struct {
		name    string
		m       Matrix
		wantErr string // substring; "" = valid
	}{
		{"all native", fullMatrix(nil), ""},
		{"optional unsupported", fullMatrix(map[Behavior]Support{
			BehaviorBranching: Unsupported, BehaviorCwdReroot: Unsupported,
		}), ""},
		{"pty only", fullMatrix(map[Behavior]Support{BehaviorACP: Unsupported}), ""},
		{"emulated counts as working", fullMatrix(map[Behavior]Support{
			BehaviorACP: Unsupported, BehaviorPTY: Emulated,
		}), ""},
		{"required unsupported", fullMatrix(map[Behavior]Support{
			BehaviorInterrupt: Unsupported,
		}), `required behavior "interrupt"`},
		{"no transport", fullMatrix(map[Behavior]Support{
			BehaviorACP: Unsupported, BehaviorPTY: Unsupported,
		}), "at least one of acp/pty"},
		{"unknown behavior", fullMatrix(map[Behavior]Support{"telepathy": Native}), `unknown behavior "telepathy"`},
		{"invalid support", fullMatrix(map[Behavior]Support{BehaviorResume: "partial"}), `invalid support "partial"`},
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

	// A missing row is reported by name, and all problems come at once.
	m := fullMatrix(map[Behavior]Support{BehaviorInterrupt: Unsupported})
	delete(m, BehaviorTranscript)
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), `"transcript" not declared`) ||
		!strings.Contains(err.Error(), `required behavior "interrupt"`) {
		t.Fatalf("want both problems reported together, got %v", err)
	}
}

func TestRequirementLevels(t *testing.T) {
	for b, want := range map[Behavior]Level{
		BehaviorInteractive: LevelRequired,
		BehaviorOneShot:     LevelRequired,
		BehaviorResume:      LevelExpected,
		BehaviorBranching:   LevelOptional,
	} {
		if got := Requirement(b); got != want {
			t.Errorf("Requirement(%s) = %s, want %s", b, got, want)
		}
	}
	if Requirement("telepathy") != "" {
		t.Error("unknown behavior should have no level")
	}
	if n := len(Behaviors()); n != 14 {
		t.Errorf("Behaviors() = %d entries, want 14", n)
	}
}
