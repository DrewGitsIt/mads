package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// refAnnouncingFake wraps fake.Fake's OneShot to also announce a
// provider-native session ref via EventSessionRef, the way claude-code's
// wire format does — fake itself deliberately never emits one (its Ref is
// meaningless outside a real transcript), so this stub stands in for a
// provider that does.
type refAnnouncingFake struct {
	*fake.Fake
	ref string
}

func (r *refAnnouncingFake) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	if req.OnEvent != nil {
		req.OnEvent(provider.Event{Kind: provider.EventSessionRef, Ref: r.ref})
	}
	return r.Fake.OneShot(ctx, req)
}

// The engine records a stage.attempt_ref event, naming the stage and attempt
// exactly like usage.recorded, as soon as the provider announces its session
// ref — before the one-shot completes (spec: exact transcript attribution,
// no path guessing).
func TestStageAttemptRefRecordedNamesStageAndAttempt(t *testing.T) {
	h := newHarness(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "review-fail-attribution", Rules: []fake.Rule{
		{Match: "code_review\nTask: flaky api", Reply: "This won't do.\nSTAGE: fail — missing error handling"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	p := &refAnnouncingFake{Fake: f, ref: "sess-ref-123"}
	h.seed(contracts.Task{Title: "flaky api", Labels: []string{"trivial"}})

	h.run(t, p, h.defaultTemplate(), 1)

	type stageAttemptRef struct {
		Stage   string `json:"stage"`
		Attempt int    `json:"attempt"`
		Ref     string `json:"ref"`
	}
	var executorAttempts, codeReviewAttempts []int
	for _, ev := range h.events(t, "stage.attempt_ref") {
		var pl stageAttemptRef
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		if pl.Ref != "sess-ref-123" {
			t.Errorf("stage.attempt_ref ref: %q, want the announced ref", pl.Ref)
		}
		switch pl.Stage {
		case "executor":
			executorAttempts = append(executorAttempts, pl.Attempt)
		case "code_review":
			codeReviewAttempts = append(codeReviewAttempts, pl.Attempt)
		case "":
			t.Errorf("stage.attempt_ref missing stage: %s", ev.Payload)
		default:
			t.Errorf("stage.attempt_ref for unexpected stage: %s", ev.Payload)
		}
	}
	if len(executorAttempts) != 3 {
		t.Errorf("executor stage.attempt_ref events: %v, want 3 (one per loop-back re-run)", executorAttempts)
	}
	if want := []int{1, 2, 3}; len(codeReviewAttempts) != len(want) ||
		codeReviewAttempts[0] != want[0] || codeReviewAttempts[1] != want[1] || codeReviewAttempts[2] != want[2] {
		t.Errorf("code_review attempts: %v, want %v (climbing across its own loop-backs)", codeReviewAttempts, want)
	}
}

// A provider that never announces a ref (fake's own OneShot, or claude-code
// invocations that somehow never see session_id — never in practice) simply
// never posts stage.attempt_ref; other stage events are untouched.
func TestNoStageAttemptRefWithoutAnnouncement(t *testing.T) {
	h := newHarness(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "no-ref", Rules: []fake.Rule{
		{Match: "Pipeline stage: executor", Reply: "STAGE: pass"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h.seed(contracts.Task{Title: "quiet task", Labels: []string{"trivial"}})

	h.run(t, f, h.defaultTemplate(), 1)

	if evs := h.events(t, "stage.attempt_ref"); len(evs) != 0 {
		t.Errorf("stage.attempt_ref: %v, want none from a provider that never announces", evs)
	}
}
