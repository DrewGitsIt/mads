package retro_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/golden"
	"github.com/drewkidwell/mads/pkg/retro"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// setup starts a real Data Service and a Generator riding a fake-scripted
// Fast Model — never a mock of either, never a live LLM (project
// convention).
func setup(t *testing.T, sc fake.Scenario) (*retro.Generator, *dataclient.Client) {
	t.Helper()
	base := datatest.Start(t)
	c := dataclient.New(base, "retro")
	f, err := fake.NewWithScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	fm := fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
	return retro.New(c, fm), c
}

func mustCreateSession(t *testing.T, c *dataclient.Client, sess contracts.Session) contracts.Session {
	t.Helper()
	if err := c.CreateSession(context.Background(), &sess); err != nil {
		t.Fatalf("create session %s: %v", sess.Name, err)
	}
	return sess
}

func mustCreateTask(t *testing.T, c *dataclient.Client, task contracts.Task) contracts.Task {
	t.Helper()
	if err := c.CreateTask(context.Background(), &task); err != nil {
		t.Fatalf("create task %s: %v", task.Title, err)
	}
	return task
}

// The golden path: a scripted Fast Model reply with all five DPA fields
// populated comes back as DPAs with deterministic IDs and default-accepted
// status (the acceptance criterion: "a scripted retro over providers/fake
// sessions yields golden DPAs with all five fields populated").
func TestGenerateGolden(t *testing.T) {
	g, c := setup(t, fake.Scenario{Name: "golden-dpas", Rules: []fake.Rule{{
		Match: "Flaky test suite",
		Reply: `{"dpas": [
			{"problem": "The CI test suite fails intermittently on the router package.",
			 "evidence": "Task 'Flaky test suite' has retry_count 3 and a blocker note about a timing-dependent test.",
			 "root_cause": "A test asserts on goroutine completion without synchronization.",
			 "remediation_layer": "tool_config",
			 "suggested_action": "Add a wait group or channel handshake instead of a sleep in the test."},
			{"problem": "The commit fixing the flake was orphaned (no linked task).",
			 "evidence": "Commit abc123 'fix flaky test' has orphan=true.",
			 "root_cause": "The session had no active task set when the fix was committed.",
			 "remediation_layer": "human_process",
			 "suggested_action": "Set the session's active task before committing fixes."}
		]}`,
	}}})

	sess := mustCreateSession(t, c, contracts.Session{Name: "Fix CI flake"})
	mustCreateTask(t, c, contracts.Task{
		SessionID: sess.ID, Title: "Flaky test suite", Source: "user",
		Status: contracts.StatusDone, RetryCount: 3,
		Notes: []contracts.Note{{
			Type: contracts.NoteBlocker, Status: contracts.NoteStatusConfirmed, Source: "agent",
			Content: "Test asserts on goroutine completion without synchronization.",
		}},
	})
	if err := c.RecordFileChange(context.Background(), &contracts.FileChange{
		SessionID: sess.ID, Path: "router_test.go", Change: "modified", Origin: contracts.OriginToolUse,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RecordCommit(context.Background(), &dataclient.CommitReport{
		Commit: contracts.Commit{Hash: "abc123", SessionID: sess.ID, Message: "fix flaky test"},
	}); err != nil {
		t.Fatal(err)
	}

	var steps []string
	dpas, err := g.Generate(context.Background(), []string{sess.ID}, "", func(step string) {
		steps = append(steps, step)
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if want := []string{"gathering", "generating"}; len(steps) != len(want) || steps[0] != want[0] || steps[1] != want[1] {
		t.Errorf("progress steps = %v, want %v", steps, want)
	}

	if len(dpas) != 2 {
		t.Fatalf("got %d DPAs, want 2", len(dpas))
	}
	for i, dpa := range dpas {
		if dpa.ID == "" || dpa.Problem == "" || dpa.Evidence == "" || dpa.RootCause == "" || dpa.SuggestedAction == "" {
			t.Errorf("dpa %d: missing field(s): %+v", i, dpa)
		}
		if !dpa.RemediationLayer.Valid() {
			t.Errorf("dpa %d: invalid remediation_layer %q", i, dpa.RemediationLayer)
		}
		if dpa.Status != contracts.DPAAccepted {
			t.Errorf("dpa %d: status = %q, want default-accepted", i, dpa.Status)
		}
	}
	if dpas[0].ID != "dpa_1" || dpas[1].ID != "dpa_2" {
		t.Errorf("DPA IDs not minted in emission order: %s, %s", dpas[0].ID, dpas[1].ID)
	}

	rendered, err := json.MarshalIndent(dpas, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "golden_dpas.golden.json", append(rendered, '\n'))
}

// A session tagged "retro" whose RetroID points at a prior retro folds that
// retro's DPAs into the prompt as prior findings — the entire retro-of-retros
// requirement (spec §7: no separate mode, just tag-aware enrichment).
func TestGenerateFoldsInPriorRetroFindings(t *testing.T) {
	const priorProblem = "The deploy script silently swallowed failures."

	g, c := setup(t, fake.Scenario{Rules: []fake.Rule{
		{Match: priorProblem, Reply: `{"dpas": [
			{"problem": "The deploy script's silent-failure bug recurred.",
			 "evidence": "The follow-up session's task shows the same symptom.",
			 "root_cause": "The suggested action from the prior retro was never applied.",
			 "remediation_layer": "human_process",
			 "suggested_action": "Block follow-up session completion until the DPA's suggested action is confirmed done."}
		]}`},
	}})

	prior := contracts.Retro{
		ID: "rto_prior",
		DPAs: []contracts.DPA{{
			ID: "dpa_1", Problem: priorProblem, Evidence: "Deploy logs show a swallowed exit code.",
			RootCause: "Missing `set -e` in the deploy script.", RemediationLayer: contracts.RemediationToolConfig,
			SuggestedAction: "Add `set -e` and check exit codes explicitly.", Status: contracts.DPAAccepted,
		}},
	}
	if err := c.CreateRetro(context.Background(), &prior); err != nil {
		t.Fatal(err)
	}

	sess := mustCreateSession(t, c, contracts.Session{
		Name: "Retro follow-up", Tags: []string{"retro"}, RetroID: prior.ID,
	})
	mustCreateTask(t, c, contracts.Task{
		SessionID: sess.ID, Title: "Address deploy script bug", Source: "retro", Status: contracts.StatusDone,
	})

	dpas, err := g.Generate(context.Background(), []string{sess.ID}, "", func(string) {})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(dpas) != 1 {
		t.Fatalf("got %d DPAs, want 1 (proves the fake matched on the folded-in prior problem text)", len(dpas))
	}
}
