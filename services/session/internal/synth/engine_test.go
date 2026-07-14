package synth

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/golden"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func eventually(t *testing.T, budget time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !fn() {
		t.Fatalf("condition not met within %s", budget)
	}
}

// draftJSON is the fake fast model's scripted synthesis reply: one
// high-confidence and one low-confidence task, and likewise for notes — the
// fixture every mode test filters differently.
const draftJSON = `{
  "tasks": [
    {"title": "Add retry to the flaky uploader", "confidence": 0.9},
    {"title": "Consider renaming the helper", "confidence": 0.4}
  ],
  "notes": [
    {"type": "decision", "content": "Chose exponential backoff over a fixed delay.", "confidence": 0.85},
    {"type": "observation", "content": "The uploader test is still a little slow.", "confidence": 0.3}
  ]
}`

// newTestEngine wires an Engine against a real (test) Data Service and a
// fake Fast Model backend scripted to always reply with reply. It returns a
// second client, under a distinct actor, for the test to verify writes with.
func newTestEngine(t *testing.T, mode contracts.SynthesisMode, sessionID, reply string) (*Engine, *dataclient.Client) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "synthesizer")
	f, err := fake.NewWithScenario(fake.Scenario{Name: "synth", Rules: []fake.Rule{
		{Match: "", Reply: reply},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fm := fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
	return New(mode, sessionID, data, fm), dataclient.New(base, "test-verify")
}

// taskSummary/noteSummary pin only the fields synthesis provenance cares
// about — no IDs or timestamps — so the golden fixture stays stable.
type taskSummary struct {
	Title  string        `json:"title"`
	Draft  bool          `json:"draft"`
	Source string        `json:"source"`
	Notes  []noteSummary `json:"notes,omitempty"`
}

type noteSummary struct {
	Type    contracts.NoteType   `json:"type"`
	Content string               `json:"content"`
	Status  contracts.NoteStatus `json:"status"`
	Source  string               `json:"source"`
}

func summarize(tasks []contracts.Task) []taskSummary {
	out := make([]taskSummary, 0, len(tasks))
	for _, t := range tasks {
		ts := taskSummary{Title: t.Title, Draft: t.Draft, Source: t.Source}
		for _, n := range t.Notes {
			ts.Notes = append(ts.Notes, noteSummary{Type: n.Type, Content: n.Content, Status: n.Status, Source: n.Source})
		}
		out = append(out, ts)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// TestFullModeSurfacesAllDrafts: Full mode keeps every task and note the
// fast model proposes, regardless of confidence (spec §10).
func TestFullModeSurfacesAllDrafts(t *testing.T) {
	e, verify := newTestEngine(t, contracts.SynthesisFull, "ses_full", draftJSON)
	ctx := context.Background()

	e.OnEvent(provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{Name: "edit", Path: "uploader.go"}})

	eventually(t, 5*time.Second, func() bool {
		tasks, err := verify.Tasks(ctx, "ses_full", "")
		if err != nil {
			t.Fatalf("Tasks: %v", err)
		}
		return len(tasks) == 2
	})

	tasks, err := verify.Tasks(ctx, "ses_full", "")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := json.MarshalIndent(summarize(tasks), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "full_mode.golden.json", append(pinned, '\n'))
}

// TestLightModeSurfacesOnlyHighConfidence: Light mode drops every task and
// note below the confidence threshold (spec §10) — the fixture's
// low-confidence task and note never reach the Data Service.
func TestLightModeSurfacesOnlyHighConfidence(t *testing.T) {
	e, verify := newTestEngine(t, contracts.SynthesisLight, "ses_light", draftJSON)
	ctx := context.Background()

	e.OnEvent(provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{Name: "edit", Path: "uploader.go"}})

	eventually(t, 5*time.Second, func() bool {
		tasks, err := verify.Tasks(ctx, "ses_light", "")
		if err != nil {
			t.Fatalf("Tasks: %v", err)
		}
		return len(tasks) == 1
	})

	tasks, err := verify.Tasks(ctx, "ses_light", "")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := json.MarshalIndent(summarize(tasks), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "light_mode.golden.json", append(pinned, '\n'))
}

// TestOffModeNeverFires: New returns nil for SynthesisOff, and every Engine
// method is a no-op on nil — Off does nothing, by construction, not by a
// runtime mode check.
func TestOffModeNeverFires(t *testing.T) {
	e, verify := newTestEngine(t, contracts.SynthesisOff, "ses_off", draftJSON)
	if e != nil {
		t.Fatalf("New(SynthesisOff, ...) = %v, want nil", e)
	}

	e.OnUserSend("looks good, ship it")
	e.OnEvent(provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{Name: "edit", Path: "uploader.go"}})
	e.OnEvent(provider.Event{Kind: provider.EventTurnEnd})

	time.Sleep(100 * time.Millisecond) // no goroutine to wait for; just confirm silence
	ctx := context.Background()
	tasks, err := verify.Tasks(ctx, "ses_off", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("Off mode wrote tasks: %+v", tasks)
	}
}

// TestMediumValueTriggerDebouncedInFullMode: three-plus tool uses in one
// turn is a medium-value trigger, but only fires once the mode's debounce
// (turn count, here) has elapsed — otherwise a busy but unremarkable
// session would spam drafts every turn.
func TestMediumValueTriggerDebouncedInFullMode(t *testing.T) {
	e, verify := newTestEngine(t, contracts.SynthesisFull, "ses_debounce", draftJSON)
	ctx := context.Background()

	threeReads := func() {
		for i := 0; i < 3; i++ {
			e.OnEvent(provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{Name: "read", Path: "x.go"}})
		}
		e.OnEvent(provider.Event{Kind: provider.EventTurnEnd})
	}

	threeReads() // turnsSinceFire: 0 -> 1, below debounceByMode[Full].turns (3)
	threeReads() // -> 2
	time.Sleep(100 * time.Millisecond)
	tasks, err := verify.Tasks(ctx, "ses_debounce", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("medium-value trigger fired before debounce elapsed: %+v", tasks)
	}

	threeReads() // -> 3, meets debounceByMode[Full].turns: fires
	eventually(t, 5*time.Second, func() bool {
		tasks, err := verify.Tasks(ctx, "ses_debounce", "")
		if err != nil {
			t.Fatalf("Tasks: %v", err)
		}
		return len(tasks) == 2
	})
}
