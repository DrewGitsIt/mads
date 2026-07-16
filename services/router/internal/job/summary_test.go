package job_test

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

func mustCreateSession(t *testing.T, c *dataclient.Client, s contracts.Session) contracts.Session {
	t.Helper()
	if err := c.CreateSession(context.Background(), &s); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return s
}

func mustCreateSummaryTask(t *testing.T, c *dataclient.Client, task contracts.Task) contracts.Task {
	t.Helper()
	if err := c.CreateTask(context.Background(), &task); err != nil {
		t.Fatalf("create task %s: %v", task.Title, err)
	}
	return task
}

// The golden job path: spawning GenerateSessionSummary writes the rendered
// narrative to session.summary and emits exactly the standard job lifecycle
// (no extra event payload — session.updated, emitted by UpdateSession
// itself, is the only externally visible effect).
func TestGenerateSessionSummaryWritesSessionSummary(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := scriptedFastModel(t, fake.Scenario{Name: "summary-job", Rules: []fake.Rule{{
		Match: "Fix the login bug",
		Reply: `{"accomplished":"Fixed a login bug.","decisions":"","remaining":""}`,
	}}})

	sess := mustCreateSession(t, data, contracts.Session{Name: "summary session"})
	mustCreateSummaryTask(t, data, contracts.Task{
		SessionID: sess.ID, Title: "Fix the login bug", Source: "user", Status: contracts.StatusDone,
	})

	runner := job.New(data, fm, registryclient.New(""))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.SessionSummaryKind, sess.ID, job.GenerateSessionSummary(sess.ID)); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	want := "## What was accomplished\n\nFixed a login bug."
	eventually(t, 5*time.Second, func() bool {
		got, err := verify.Session(ctx, sess.ID)
		return err == nil && got.Summary == want
	})

	evs, err := verify.Events(ctx, 0, sess.ID, nil)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var seq []contracts.EventType
	for _, ev := range evs {
		switch ev.Type {
		case contracts.JobQueued, contracts.JobStarted, contracts.JobCompleted:
			seq = append(seq, ev.Type)
		}
	}
	wantSeq := []contracts.EventType{contracts.JobQueued, contracts.JobStarted, contracts.JobCompleted}
	if len(seq) != len(wantSeq) {
		t.Fatalf("job event sequence = %v, want %v", seq, wantSeq)
	}
	for i, want := range wantSeq {
		if seq[i] != want {
			t.Fatalf("job event %d = %q, want %q (full sequence %v)", i, seq[i], want, seq)
		}
	}
}

// The overwrite proof (explicit acceptance criterion): a second run's
// summary replaces the first rather than appending to it.
func TestGenerateSessionSummaryOverwritesOnSecondRun(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := scriptedFastModel(t, fake.Scenario{Name: "summary-job-overwrite", Rules: []fake.Rule{
		{
			Match: "Second task",
			Reply: `{"accomplished":"Second summary.","decisions":"","remaining":""}`,
		},
		{
			Match: "Fix the login bug",
			Reply: `{"accomplished":"First summary.","decisions":"","remaining":""}`,
		},
	}})

	sess := mustCreateSession(t, data, contracts.Session{Name: "overwrite session"})
	mustCreateSummaryTask(t, data, contracts.Task{
		SessionID: sess.ID, Title: "Fix the login bug", Source: "user", Status: contracts.StatusDone,
	})

	runner := job.New(data, fm, registryclient.New(""))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	verify := dataclient.New(base, "test-verify")

	if _, err := runner.Spawn(ctx, job.SessionSummaryKind, sess.ID, job.GenerateSessionSummary(sess.ID)); err != nil {
		t.Fatalf("Spawn (first run): %v", err)
	}
	first := "## What was accomplished\n\nFirst summary."
	eventually(t, 5*time.Second, func() bool {
		got, err := verify.Session(ctx, sess.ID)
		return err == nil && got.Summary == first
	})

	mustCreateSummaryTask(t, data, contracts.Task{
		SessionID: sess.ID, Title: "Second task", Source: "user", Status: contracts.StatusDone,
	})

	if _, err := runner.Spawn(ctx, job.SessionSummaryKind, sess.ID, job.GenerateSessionSummary(sess.ID)); err != nil {
		t.Fatalf("Spawn (second run): %v", err)
	}
	second := "## What was accomplished\n\nSecond summary."
	eventually(t, 5*time.Second, func() bool {
		got, err := verify.Session(ctx, sess.ID)
		return err == nil && got.Summary == second
	})

	got, err := verify.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if got.Summary != second {
		t.Fatalf("Summary = %q, want %q (not %q, not concatenated)", got.Summary, second, first)
	}
}

// A session deleted before the job runs must be a no-op, not a failure — the
// terminate-then-delete race the trigger's guard exists for.
func TestGenerateSessionSummaryNoopForDeletedSession(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := scriptedFastModel(t, fake.Scenario{Name: "summary-job-deleted"})

	runner := job.New(data, fm, registryclient.New(""))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.SessionSummaryKind, "ses_never_existed", job.GenerateSessionSummary("ses_never_existed")); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, "ses_never_existed", []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	evs, err := verify.Events(ctx, 0, "ses_never_existed", []string{"job.failed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("job.failed events for a deleted session: %v, want none", evs)
	}
}
