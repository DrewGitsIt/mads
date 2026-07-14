package job_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/agentgen"
	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// scriptedFastModel builds a Fast Model runner over a fake provider scripted
// with sc — a real fastmodel.Runner, never a mock, riding providers/fake per
// project convention.
func scriptedFastModel(t *testing.T, sc fake.Scenario) *fastmodel.Runner {
	t.Helper()
	f, err := fake.NewWithScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	return fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
}

func TestGenerateAgentFromTaskJobDeliversDraftEvent(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := scriptedFastModel(t, fake.Scenario{Name: "agentgen-job-task", Rules: []fake.Rule{{
		Match: "Fix the flaky login test",
		Reply: `{"name":"login-test-fixer","description":"Fixes flaky login tests",` +
			`"prompt":"You are a QA engineer who stabilizes flaky login tests.",` +
			`"quadrant":"autonomous_executor","tags":["testing","login"],` +
			`"capabilities":{"tools":["shell"],"mcp_servers":[],"skills":[]},"permissions":{}}`,
	}}})

	runner := job.New(data, fm)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	fn := job.GenerateAgentFromTask(agentselect.Request{
		TaskID: "tsk_1", SessionID: "ses_gen", Title: "Fix the flaky login test",
	}, "ses_gen", "tsk_1")

	id, err := runner.Spawn(ctx, job.AgentGenerationKind, "ses_gen", fn)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, "ses_gen", []string{"job.completed"})
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		return len(evs) > 0
	})

	evs, err := verify.Events(ctx, 0, "ses_gen", nil)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	var seq []contracts.EventType
	var draft *agentgen.Result
	for _, ev := range evs {
		seq = append(seq, ev.Type)
		if ev.Type == contracts.AgentDraftGenerated {
			var res agentgen.Result
			if err := json.Unmarshal(ev.Payload, &res); err != nil {
				t.Fatalf("unmarshal draft event: %v", err)
			}
			draft = &res
		}
	}

	wantSeq := []contracts.EventType{
		contracts.JobQueued, contracts.JobStarted, contracts.AgentDraftGenerated, contracts.JobCompleted,
	}
	if len(seq) != len(wantSeq) {
		t.Fatalf("event sequence = %v, want %v", seq, wantSeq)
	}
	for i, want := range wantSeq {
		if seq[i] != want {
			t.Fatalf("event %d = %q, want %q (full sequence %v)", i, seq[i], want, seq)
		}
	}

	if draft == nil {
		t.Fatal("no agent.draft_generated event observed")
	}
	if draft.Draft.Name != "login-test-fixer" {
		t.Fatalf("draft.Name = %q, want login-test-fixer", draft.Draft.Name)
	}

	if id == "" {
		t.Fatal("Spawn: no job id returned")
	}
}

func TestGenerateAgentFromDescriptionJobDeliversDraftEvent(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := scriptedFastModel(t, fake.Scenario{Name: "agentgen-job-description", Rules: []fake.Rule{{
		Match: "database migrations and knows PostgreSQL",
		Reply: `{"name":"db-migrator","description":"Specializes in PostgreSQL schema migrations",` +
			`"prompt":"You are a database migration specialist.",` +
			`"quadrant":"technical_partner","tags":["database","postgres"],` +
			`"capabilities":{"tools":["shell"],"mcp_servers":["postgres"],"skills":[]},"permissions":{}}`,
	}}})

	runner := job.New(data, fm)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	fn := job.GenerateAgentFromDescription("I need an agent that specializes in database migrations and knows PostgreSQL", "ses_gen_nl")
	if _, err := runner.Spawn(ctx, job.AgentGenerationKind, "ses_gen_nl", fn); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, "ses_gen_nl", []string{"agent.draft_generated"})
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		return len(evs) > 0
	})

	evs, err := verify.Events(ctx, 0, "ses_gen_nl", []string{"agent.draft_generated"})
	if err != nil || len(evs) != 1 {
		t.Fatalf("events: %d, %v", len(evs), err)
	}
	var res agentgen.Result
	if err := json.Unmarshal(evs[0].Payload, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Draft.Name != "db-migrator" {
		t.Fatalf("draft.Name = %q, want db-migrator", res.Draft.Name)
	}
}

// A Fast Model reply the validator rejects past MaxAttempts fails the job
// body, which must surface as job.failed rather than a delivered draft.
func TestGenerateAgentFromTaskFailsWhenGenerationFails(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := scriptedFastModel(t, fake.Scenario{Name: "agentgen-job-invalid", Rules: []fake.Rule{{
		Match: "Fix the flaky login test",
		Reply: `not json at all`,
	}}})

	runner := job.New(data, fm)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	fn := job.GenerateAgentFromTask(agentselect.Request{
		TaskID: "tsk_bad", SessionID: "ses_bad", Title: "Fix the flaky login test",
	}, "ses_bad", "tsk_bad")

	if _, err := runner.Spawn(ctx, job.AgentGenerationKind, "ses_bad", fn); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, "ses_bad", []string{"job.failed"})
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		return len(evs) > 0
	})

	evs, err := verify.Events(ctx, 0, "ses_bad", nil)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var gotFailed, gotDraft bool
	for _, ev := range evs {
		switch ev.Type {
		case contracts.JobFailed:
			gotFailed = true
		case contracts.AgentDraftGenerated:
			gotDraft = true
		}
	}
	if !gotFailed {
		t.Fatal("no job.failed event observed")
	}
	if gotDraft {
		t.Fatal("agent.draft_generated emitted for a failing generation")
	}
}
