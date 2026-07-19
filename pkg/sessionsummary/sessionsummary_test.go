package sessionsummary_test

import (
	"context"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/golden"
	"github.com/drewkidwell/mads/pkg/sessionsummary"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// setup starts a real Data Service and a Generator riding a fake-scripted
// Fast Model — never a mock of either, never a live LLM (project
// convention).
func setup(t *testing.T, sc fake.Scenario) (*sessionsummary.Generator, *dataclient.Client) {
	t.Helper()
	base := datatest.Start(t)
	c := dataclient.New(base, "sessionsummary")
	f, err := fake.NewWithScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	fm := fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
	return sessionsummary.New(c, fm), c
}

func mustCreateTask(t *testing.T, c *dataclient.Client, task contracts.Task) contracts.Task {
	t.Helper()
	if err := c.CreateTask(context.Background(), &task); err != nil {
		t.Fatalf("create task %s: %v", task.Title, err)
	}
	return task
}

// The golden path: a scripted Fast Model reply with all three fields
// populated comes back rendered as three headed markdown sections.
func TestGenerateGolden(t *testing.T) {
	g, c := setup(t, fake.Scenario{Name: "full-summary", Rules: []fake.Rule{{
		Match: "Write the platform spec",
		Reply: `{"accomplished":"Wrote a platform spec across multiple passes of increasing granularity.",` +
			`"decisions":"Chose a compressed narrative over an enumerated task list for session summaries.",` +
			`"remaining":"Run controlled experiments on agent prompt effectiveness using SWE-bench."}`,
	}}})

	sessionID := "ses_full"
	mustCreateTask(t, c, contracts.Task{
		SessionID: sessionID, Title: "Write the platform spec", Source: "user",
		Status: contracts.StatusDone, Description: "Draft the rebuild spec end to end.",
		Notes: []contracts.Note{{
			Type: contracts.NoteDecision, Status: contracts.NoteStatusConfirmed, Source: "user",
			Content: "Chose a compressed narrative over an enumerated task list for session summaries.",
		}},
	})

	got, err := g.Generate(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	golden.Assert(t, "full_summary.golden.md", []byte(got))
}

// A reply with empty decisions/remaining renders only the accomplished
// section — no heading, no blank body for the omitted sections (the
// acceptance-critical proof).
func TestGenerateEmptySectionsOmitted(t *testing.T) {
	g, c := setup(t, fake.Scenario{Name: "empty-sections", Rules: []fake.Rule{{
		Match: "Fix the login bug",
		Reply: `{"accomplished":"Fixed a login bug.","decisions":"","remaining":""}`,
	}}})

	sessionID := "ses_empty"
	mustCreateTask(t, c, contracts.Task{
		SessionID: sessionID, Title: "Fix the login bug", Source: "user", Status: contracts.StatusDone,
	})

	got, err := g.Generate(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "## What was accomplished\n\nFixed a login bug."
	if got != want {
		t.Fatalf("Generate = %q, want %q", got, want)
	}
	if got != want || len(got) == 0 {
		t.Fatalf("rendered output must be exactly the accomplished section")
	}
}

// A session with zero tasks has nothing to summarize and must not spend a
// Fast Model call — an empty-Rules scenario makes any unexpected call fail
// loudly (extractJSON rejects the fallback "ok" reply).
func TestGenerateNoTasksSkipsModelCall(t *testing.T) {
	g, _ := setup(t, fake.Scenario{Name: "no-tasks"})

	got, err := g.Generate(context.Background(), "ses_no_tasks")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "" {
		t.Fatalf("Generate = %q, want empty string for a session with no tasks", got)
	}
}

// An unconfigured Fast Model backend — production's default, providers/fake
// with no scripted rules, always falls back to its builtin "ok" reply, which
// is never valid JSON — must degrade to "", nil, not an error. This is the
// exact shape a session close hits when -fastmodel-base-url is unset (spec
// §16: never block or fail a close on summarization).
func TestGenerateSkipsSilentlyOnUnconfiguredBackend(t *testing.T) {
	g, c := setup(t, fake.Scenario{Name: "unconfigured-backend"})

	sessionID := "ses_unconfigured"
	mustCreateTask(t, c, contracts.Task{
		SessionID: sessionID, Title: "Fix the login bug", Source: "user", Status: contracts.StatusDone,
	})

	got, err := g.Generate(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Generate: %v, want nil (unconfigured backend must skip silently)", err)
	}
	if got != "" {
		t.Fatalf("Generate = %q, want empty string when the backend can't produce structured output", got)
	}
}
