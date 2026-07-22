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
		UpdateFeedURL:       "not-a-url",
	}
	verr := ValidateConfig(&c)
	if verr.Empty() {
		t.Fatal("expected validation failures")
	}
	want := 11
	if len(verr.Errors) != want {
		t.Fatalf("got %d errors, want %d (report every problem at once): %+v", len(verr.Errors), want, verr.Errors)
	}
}

func TestValidateConfigUpdateFeedURL(t *testing.T) {
	base := DefaultConfig()

	base.UpdateFeedURL = ""
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("empty update_feed_url should be valid (feature off): %+v", verr.Errors)
	}

	base.UpdateFeedURL = "https://example.com/feed.json"
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("absolute https URL should be valid: %+v", verr.Errors)
	}

	base.UpdateFeedURL = "ftp://example.com/feed.json"
	if verr := ValidateConfig(&base); verr.Empty() {
		t.Fatal("non-http(s) scheme should be invalid")
	}

	base.UpdateFeedURL = "not-a-url"
	if verr := ValidateConfig(&base); verr.Empty() {
		t.Fatal("relative/non-absolute URL should be invalid")
	}
}

func TestValidateConfigFastModelBaseURL(t *testing.T) {
	base := DefaultConfig()

	base.FastModelBaseURL = ""
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("empty fast_model_base_url should be valid (no endpoint configured): %+v", verr.Errors)
	}

	base.FastModelBaseURL = "https://api.example.com/v1"
	if verr := ValidateConfig(&base); !verr.Empty() {
		t.Fatalf("absolute https URL should be valid: %+v", verr.Errors)
	}

	base.FastModelBaseURL = "not-a-url"
	if verr := ValidateConfig(&base); verr.Empty() {
		t.Fatal("relative/non-absolute URL should be invalid")
	}
}
