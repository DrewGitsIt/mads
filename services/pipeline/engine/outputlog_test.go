package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/fake"
)

// A nil *outputLog (MadsHome unset) is a safe no-op throughout.
func TestOutputLogNilIsNoOp(t *testing.T) {
	var l *outputLog
	l.write("anything")
	l.close("fallback")
	if openOutputLog("", "sess", 1, "task", "executor", 1) != nil {
		t.Fatal("empty madsHome should return nil")
	}
}

// Streamed chunks are appended in order; close only falls back to the final
// text when nothing streamed.
func TestOutputLogWriteAndFallback(t *testing.T) {
	dir := t.TempDir()
	l := openOutputLog(dir, "sess", 7, "task-1", "executor", 1)
	if l == nil {
		t.Fatal("expected a real log")
	}
	l.write("hello ")
	l.write("world")
	l.close("unused fallback")

	got, err := os.ReadFile(outputLogPath(dir, "sess", 7, "task-1", "executor", 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Errorf("log content = %q", got)
	}

	l2 := openOutputLog(dir, "sess", 7, "task-1", "code_review", 1)
	l2.close("fell back")
	got2, err := os.ReadFile(outputLogPath(dir, "sess", 7, "task-1", "code_review", 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != "fell back" {
		t.Errorf("fallback content = %q", got2)
	}
}

// The engine captures a real run's streamed agent output to
// $MadsHome/runs/<session>/<run_seq>/<task>/<stage>-attempt-<n>.log — a
// provider that streams chunks (executor, scripted) leaves the concatenated
// chunks; a provider call with no OnChunk firing (code_review, unscripted,
// default "ok" reply) still leaves the final text as a single fallback
// chunk. Config.MadsHome == "" (every other engine test) writes nothing and
// never errors.
func TestEngineCapturesStageOutputLogs(t *testing.T) {
	h := newHarness(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "capture", Rules: []fake.Rule{
		{Match: "Pipeline stage: executor", Chunks: []string{"Hello ", "World"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	seeded := h.seed(contracts.Task{Title: "typo fix", Labels: []string{"trivial"}})
	madsHome := t.TempDir()
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: f, SessionID: h.sess.ID,
		Template: h.defaultTemplate(), Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second, MadsHome: madsHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v", sum)
	}

	started := h.events(t, "pipeline.started")
	if len(started) != 1 {
		t.Fatalf("pipeline.started: %+v", started)
	}
	runSeq := started[0].Seq

	executorLog := outputLogPath(madsHome, h.sess.ID, runSeq, seeded[0].ID, "executor", 1)
	got, err := os.ReadFile(executorLog)
	if err != nil {
		t.Fatalf("executor log: %v", err)
	}
	if string(got) != "Hello World" {
		t.Errorf("executor log content = %q", got)
	}

	reviewLog := outputLogPath(madsHome, h.sess.ID, runSeq, seeded[0].ID, "code_review", 1)
	got, err = os.ReadFile(reviewLog)
	if err != nil {
		t.Fatalf("code_review log: %v", err)
	}
	if !strings.Contains(string(got), "ok") {
		t.Errorf("code_review log content = %q, want the fallback final text", got)
	}
}

// Confirms the log tree layout itself, independent of the fallback content
// assertion above.
func TestOutputLogPathShape(t *testing.T) {
	got := outputLogPath("/home", "sess-1", 42, "task-1", "executor", 2)
	want := filepath.Join("/home", "runs", "sess-1", "42", "task-1", "executor-attempt-2.log")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}
