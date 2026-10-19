package store

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// TestPipelineRunsUnterminatedStaysRunning pins the trickiest reconstruction
// case: a pipeline.started event with no closing terminal event yet must
// come back as an open run (Outcome "running", zero EndedAt), not be dropped.
func TestPipelineRunsUnterminatedStaysRunning(t *testing.T) {
	l := openMigratedDB(t)
	metrics := NewMetrics(l, NewTasks(l), NewCapabilities(l))
	ctx := context.Background()

	if err := l.Append(ctx, &contracts.Envelope{
		Type: contracts.PipelineStarted, SessionID: "ses_open", Actor: "pipeline",
	}); err != nil {
		t.Fatal(err)
	}

	runs, err := metrics.PipelineRuns(ctx, "ses_open")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d: %+v", len(runs), runs)
	}
	run := runs[0]
	if run.Outcome != "running" {
		t.Errorf("outcome = %q, want %q", run.Outcome, "running")
	}
	if !run.EndedAt.IsZero() {
		t.Errorf("ended_at = %v, want zero", run.EndedAt)
	}
}

// TestPipelineRunsEventSilentReclassifiedInterrupted pins that an open run
// whose last event is well past runStaleAfter is reported as "interrupted",
// not left "running" forever — the engine that started it is gone and never
// emitted a terminal event (e.g. it was killed).
func TestPipelineRunsEventSilentReclassifiedInterrupted(t *testing.T) {
	l := openMigratedDB(t)
	metrics := NewMetrics(l, NewTasks(l), NewCapabilities(l))
	ctx := context.Background()

	started := time.Date(2026, 7, 19, 23, 7, 0, 0, time.UTC)
	if err := l.Append(ctx, &contracts.Envelope{
		Type: contracts.PipelineStarted, SessionID: "ses_silent", Actor: "pipeline",
		Timestamp: started,
	}); err != nil {
		t.Fatal(err)
	}
	metrics.now = func() time.Time { return started.Add(48 * time.Hour) }

	runs, err := metrics.PipelineRuns(ctx, "ses_silent")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d: %+v", len(runs), runs)
	}
	run := runs[0]
	if run.Outcome != "interrupted" {
		t.Errorf("outcome = %q, want %q", run.Outcome, "interrupted")
	}
	if !run.EndedAt.Equal(started) {
		t.Errorf("ended_at = %v, want %v", run.EndedAt, started)
	}
}

// TestPipelineRunsRecentActivityStaysRunning pins the other side: a run
// whose pipeline.started is old but which has a recent event (e.g. a
// stage.started from an engine still working through a long run) must not
// be reclassified just because the start itself is old.
func TestPipelineRunsRecentActivityStaysRunning(t *testing.T) {
	l := openMigratedDB(t)
	metrics := NewMetrics(l, NewTasks(l), NewCapabilities(l))
	ctx := context.Background()

	started := time.Date(2026, 7, 19, 23, 7, 0, 0, time.UTC)
	if err := l.Append(ctx, &contracts.Envelope{
		Type: contracts.PipelineStarted, SessionID: "ses_active", Actor: "pipeline",
		Timestamp: started,
	}); err != nil {
		t.Fatal(err)
	}
	recent := started.Add(48*time.Hour - 10*time.Minute)
	if err := l.Append(ctx, &contracts.Envelope{
		Type: contracts.StageStarted, SessionID: "ses_active", Actor: "pipeline",
		TaskID: "tsk_x", Timestamp: recent,
		Payload: []byte(`{"stage":"executor"}`),
	}); err != nil {
		t.Fatal(err)
	}
	metrics.now = func() time.Time { return started.Add(48 * time.Hour) }

	runs, err := metrics.PipelineRuns(ctx, "ses_active")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d: %+v", len(runs), runs)
	}
	run := runs[0]
	if run.Outcome != "running" {
		t.Errorf("outcome = %q, want %q", run.Outcome, "running")
	}
	if !run.EndedAt.IsZero() {
		t.Errorf("ended_at = %v, want zero", run.EndedAt)
	}
}

// appendRunEvents appends pipeline lifecycle events to one session, each a
// minute apart, and returns nothing: the log's own seqs order them.
func appendRunEvents(t *testing.T, l *Log, sessionID string, evs ...contracts.Envelope) {
	t.Helper()
	at := time.Now().UTC().Add(-time.Duration(len(evs)) * time.Minute)
	for _, ev := range evs {
		ev.SessionID, ev.Actor, ev.Timestamp = sessionID, "pipeline", at
		if err := l.Append(context.Background(), &ev); err != nil {
			t.Fatal(err)
		}
		at = at.Add(time.Minute)
	}
}

func runEvent(typ contracts.EventType, payload string) contracts.Envelope {
	return contracts.Envelope{Type: typ, Payload: []byte(payload)}
}

func outcomes(runs []contracts.PipelineRun) []string {
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = r.RunID + ":" + r.Outcome
	}
	return out
}

// TestPipelineRunsPairByRunID: a UI-launched run and a CLI run overlap on
// one session. The UI run's cancel closes the UI run — not whichever run
// started last, which is what order-pairing did (the leader session's
// run_seq 12226 stayed "running" forever while 12244 took its cancel).
func TestPipelineRunsPairByRunID(t *testing.T) {
	l := openMigratedDB(t)
	metrics := NewMetrics(l, NewTasks(l), NewCapabilities(l))
	appendRunEvents(t, l, "ses_overlap",
		runEvent(contracts.PipelineStarted, `{"run_id":"run_ui","concurrency":3}`),
		runEvent(contracts.PipelineStarted, `{"run_id":"run_cli","concurrency":1}`),
		runEvent(contracts.PipelineCancelled, `{"run_id":"run_ui"}`),
		runEvent(contracts.PipelineCompleted, `{"run_id":"run_cli","outcome":"completed","done":2}`),
	)
	runs, err := metrics.PipelineRuns(context.Background(), "ses_overlap")
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(runs)
	want := []string{"run_ui:cancelled", "run_cli:completed"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	if runs[1].TasksDone != 2 || runs[0].EndedAt.IsZero() {
		t.Errorf("runs = %+v", runs)
	}
}

// TestPipelineRunsLegacyNeverRunningForever: the leader session's actual
// log, from before pipeline.started carried run_id. The pairing can't be
// recovered, but no run is left "running" behind a newer start.
func TestPipelineRunsLegacyNeverRunningForever(t *testing.T) {
	l := openMigratedDB(t)
	metrics := NewMetrics(l, NewTasks(l), NewCapabilities(l))
	appendRunEvents(t, l, "ses_legacy",
		runEvent(contracts.PipelineStarted, `{"concurrency":3,"template":"default-implementation"}`),
		runEvent(contracts.PipelineStarted, `{"concurrency":1,"template":"default-implementation"}`),
		runEvent(contracts.PipelineCancelled, `{"run_id":"run_789d8b08f61b635b"}`),
		runEvent(contracts.PipelineStarted, `{"concurrency":3,"template":"default-implementation"}`),
	)
	runs, err := metrics.PipelineRuns(context.Background(), "ses_legacy")
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(runs)
	want := []string{":interrupted", ":cancelled", ":running"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("runs = %v, want %v", got, want)
		}
	}
	running := 0
	for _, r := range runs {
		if r.Outcome == "running" {
			running++
		}
	}
	if running != 1 {
		t.Errorf("%d runs report running, want only the newest", running)
	}
}

// TestPipelineRunsLostReadsInterrupted: the Router's lost-run sweep closes a
// run whose engine died unseen with pipeline.failed {reason: "lost"}; run
// history reports that as interrupted, while a crash stays failed.
func TestPipelineRunsLostReadsInterrupted(t *testing.T) {
	l := openMigratedDB(t)
	metrics := NewMetrics(l, NewTasks(l), NewCapabilities(l))
	appendRunEvents(t, l, "ses_lost",
		runEvent(contracts.PipelineStarted, `{"run_id":"run_crash"}`),
		runEvent(contracts.PipelineFailed, `{"run_id":"run_crash","reason":"exit status 1"}`),
		runEvent(contracts.PipelineStarted, `{"run_id":"run_lost"}`),
		runEvent(contracts.PipelineFailed, `{"run_id":"run_lost","reason":"lost","run_seq":3}`),
	)
	runs, err := metrics.PipelineRuns(context.Background(), "ses_lost")
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(runs)
	want := []string{"run_crash:failed", "run_lost:interrupted"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("runs = %v, want %v", got, want)
	}
}
