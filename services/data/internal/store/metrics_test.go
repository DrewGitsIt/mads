package store

import (
	"context"
	"testing"

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
