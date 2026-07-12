package contracts_test

import (
	"encoding/json"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/golden"
)

// The validate-all-at-once rule (spec §10.3): every failure is collected and
// reported together, never fail-on-first.
func TestValidationErrorCollectsAll(t *testing.T) {
	var v contracts.ValidationError
	if !v.Empty() {
		t.Fatal("new ValidationError should be empty")
	}
	v.Add("title", "required")
	v.Add("stages[2].agent", `unknown agent "reviewer-9000"`)
	v.Add("stages[4].on_fail.loop_back", "target stage does not exist")
	if v.Empty() {
		t.Fatal("ValidationError with entries reports empty")
	}
	if len(v.Errors) != 3 {
		t.Fatalf("want all 3 errors reported together, got %d", len(v.Errors))
	}

	got, err := json.MarshalIndent(&v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "validation_error.json", append(got, '\n'))
}

func TestValidationErrorMessage(t *testing.T) {
	var v contracts.ValidationError
	v.Add("name", "required")
	v.Add("port", "must be between 1 and 65535")
	want := "validation failed: name: required; port: must be between 1 and 65535"
	if v.Error() != want {
		t.Errorf("Error() = %q, want %q", v.Error(), want)
	}
}
