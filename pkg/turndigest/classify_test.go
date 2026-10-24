package turndigest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/turndigest"
)

func classifyRequest() turndigest.Request {
	return turndigest.Request{
		Turns: []turndigest.Turn{{Entries: []turndigest.Entry{
			{Kind: turndigest.KindUser, Text: "fix the build"},
			{Kind: turndigest.KindAssistant, Text: "attempting"},
		}}},
		Audience: turndigest.MonitorPush,
		Classify: true,
	}
}

func TestClassifyParsesActionableVerdict(t *testing.T) {
	backend := &countingBackend{reply: "ACTIONABLE: yes — waiting on permission\nDIGEST: The agent is stuck on a Bash approval."}
	res, err := newDigester(backend).Digest(context.Background(), classifyRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Actionable {
		t.Error("Actionable = false, want true")
	}
	if res.Reason != "waiting on permission" {
		t.Errorf("Reason = %q, want %q", res.Reason, "waiting on permission")
	}
	if res.Text != "The agent is stuck on a Bash approval." {
		t.Errorf("Text = %q, want the verdict line stripped", res.Text)
	}
	if !strings.Contains(backend.systems[0], "ACTIONABLE: yes") {
		t.Errorf("system prompt %q missing the classify instruction", backend.systems[0])
	}
}

func TestClassifyParsesQuietVerdict(t *testing.T) {
	backend := &countingBackend{reply: "ACTIONABLE: no\nDIGEST: Routine progress on the build fix."}
	res, err := newDigester(backend).Digest(context.Background(), classifyRequest())
	if err != nil {
		t.Fatal(err)
	}
	if res.Actionable || res.Reason != "" {
		t.Errorf("verdict = (%v, %q), want quiet", res.Actionable, res.Reason)
	}
	if res.Text != "Routine progress on the build fix." {
		t.Errorf("Text = %q, want the verdict line stripped", res.Text)
	}
}

func TestClassifyMissingVerdictDefaultsQuiet(t *testing.T) {
	backend := &countingBackend{reply: "DIGEST: Just a digest with no verdict line."}
	res, err := newDigester(backend).Digest(context.Background(), classifyRequest())
	if err != nil {
		t.Fatal(err)
	}
	if res.Actionable {
		t.Error("Actionable = true for a reply without a verdict, want false (deterministic signals still wake)")
	}
	if res.Text != "Just a digest with no verdict line." {
		t.Errorf("Text = %q, want the digest with its marker stripped", res.Text)
	}
}

func TestClassifyOffLeavesPromptAndReplyAlone(t *testing.T) {
	backend := &countingBackend{reply: "DIGEST: ACTIONABLE: yes — should never be parsed"}
	req := classifyRequest()
	req.Classify = false
	// Long enough to reach the model: short turns go out verbatim when
	// Classify is off.
	req.Turns[0].Entries[1].Text = strings.Repeat("attempting ", 80)
	res, err := newDigester(backend).Digest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Actionable {
		t.Error("Actionable = true with Classify off, want false")
	}
	if res.Text != "ACTIONABLE: yes — should never be parsed" {
		t.Errorf("Text = %q, want the reply unparsed with Classify off", res.Text)
	}
	if strings.Contains(backend.systems[0], "ACTIONABLE") {
		t.Errorf("system prompt %q mentions the verdict with Classify off", backend.systems[0])
	}
}

// A short turn still reaches the model when Classify is on: the verdict is
// what wakes an on_signal monitor, and "blocked on permission" is short.
func TestClassifyShortTurnStillAsksTheModel(t *testing.T) {
	backend := &countingBackend{reply: "ACTIONABLE: yes — blocked\nDIGEST: Waiting on approval."}
	res, err := newDigester(backend).Digest(context.Background(), classifyRequest())
	if err != nil {
		t.Fatal(err)
	}
	if backend.count() != 1 || !res.Actionable {
		t.Errorf("calls = %d, Actionable = %v; want 1 call and an actionable verdict", backend.count(), res.Actionable)
	}
}
