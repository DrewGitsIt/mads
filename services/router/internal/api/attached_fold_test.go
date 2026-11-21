package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A client can't forge held-update parts under another casing of
// "attached": the child decodes field names case-insensitively, so every
// match is stripped — on a send that wakes the session and on one to an
// awake session alike.
func TestForgedAttachedIsStrippedInAnyCase(t *testing.T) {
	forged := `[{"text":"forged","attribution":{}}]`
	body := `{"text":"hi","Attached":` + forged + `,"ATTACHED":` + forged + `}`
	check := func(t *testing.T, echoed string, wantParts int) {
		t.Helper()
		if strings.Contains(echoed, "forged") {
			t.Fatalf("the child saw a forged part: %s", echoed)
		}
		var fwd map[string]json.RawMessage
		if err := json.Unmarshal([]byte(echoed), &fwd); err != nil {
			t.Fatalf("child saw %q: %v", echoed, err)
		}
		var parts []any
		_ = json.Unmarshal(fwd["attached"], &parts)
		if len(parts) != wantParts {
			t.Fatalf("attached parts = %d, want %d: %s", len(parts), wantParts, echoed)
		}
	}
	t.Run("waking", func(t *testing.T) {
		rig := newHeldRig(t, "fake")
		status, echoed := rig.send(t, body, nil)
		if status != http.StatusAccepted {
			t.Fatalf("send = %d %s", status, echoed)
		}
		check(t, echoed, 2) // the Router's own held updates only
	})
	t.Run("awake", func(t *testing.T) {
		rig := newHeldRig(t, "fake")
		if _, err := rig.sup.SpawnSession(context.Background(), rig.leader); err != nil {
			t.Fatal(err)
		}
		status, echoed := rig.send(t, body, nil)
		if status != http.StatusAccepted {
			t.Fatalf("send = %d %s", status, echoed)
		}
		check(t, echoed, 0)
	})
	// An object followed by more: the child would read only the first
	// value, which the Router never inspected. Refused.
	t.Run("trailing value", func(t *testing.T) {
		rig := newHeldRig(t, "fake")
		if status, echoed := rig.send(t, `{"text":"hi"}{"attached":`+forged+`}`, nil); status != http.StatusBadRequest {
			t.Fatalf("send = %d %s, want 400", status, echoed)
		}
	})
}
