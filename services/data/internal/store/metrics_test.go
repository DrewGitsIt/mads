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
