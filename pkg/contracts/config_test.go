package contracts

import "testing"

func TestDefaultConfigValid(t *testing.T) {
	c := DefaultConfig()
	if verr := ValidateConfig(&c); !verr.Empty() {
		t.Fatalf("DefaultConfig() failed its own validation: %+v", verr.Errors)
	}
}

func TestValidateConfigReportsEveryProblem(t *testing.T) {
	c := Config{
		SynthesisMode:       "bogus",
		AgentSelectionMode:  "bogus",
		DefaultModelTier:    "bogus",
		CostAlertCents:      -1,
		DefaultTrust:        "bogus",
		Theme:               "bogus",
		Palette:             "bogus",
		ConcurrencyLimit:    0,
		SynthesisDebounceMS: -1,
		LogRetentionDays:    0,
	}
	verr := ValidateConfig(&c)
	if verr.Empty() {
		t.Fatal("expected validation failures")
	}
	want := 10
	if len(verr.Errors) != want {
		t.Fatalf("got %d errors, want %d (report every problem at once): %+v", len(verr.Errors), want, verr.Errors)
	}
}
