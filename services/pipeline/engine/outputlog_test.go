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
	"github.com/drewkidwell/mads/providers/provider"
)

// A nil *outputLog (MadsHome unset) is a safe no-op throughout.
func TestOutputLogNilIsNoOp(t *testing.T) {
	var l *outputLog
	l.write("anything")
	l.tool(&provider.ToolUse{Name: "Read", Path: "f.go"})
	l.close("pass", "fallback")
	if openOutputLog("", "sess", 1, "task", "executor", 1, "model") != nil {
		t.Fatal("empty madsHome should return nil")
	}
}

// Streamed chunks are appended in order, separated by a boundary newline
// where the writer didn't already end with one; close only falls back to
// the final text when nothing streamed. Both cases carry a header and
// footer line.
func TestOutputLogWriteAndFallback(t *testing.T) {
	dir := t.TempDir()
	l := openOutputLog(dir, "sess", 7, "task-1", "executor", 1, "fake-sonnet")
	if l == nil {
		t.Fatal("expected a real log")
	}
	l.write("hello ")
	l.write("world")
	l.close("pass", "unused fallback")

	got, err := os.ReadFile(outputLogPath(dir, "sess", 7, "task-1", "executor", 1))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("log lines = %v, want header/chunk/chunk/footer", lines)
	}
	if !strings.HasPrefix(lines[0], "=== stage=executor attempt=1 task=task-1 model=fake-sonnet started=") {
		t.Errorf("header = %q", lines[0])
	}
	if lines[1] != "hello " || lines[2] != "world" {
		t.Errorf("body lines = %v, want two boundary-separated chunks", lines[1:3])
	}
	if !strings.HasPrefix(lines[3], "=== stage=executor attempt=1 outcome=pass duration=") {
		t.Errorf("footer = %q", lines[3])
	}

	l2 := openOutputLog(dir, "sess", 7, "task-1", "code_review", 1, "fake-sonnet")
	l2.close("pass", "fell back")
	got2, err := os.ReadFile(outputLogPath(dir, "sess", 7, "task-1", "code_review", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got2), "\nfell back\n") {
		t.Errorf("fallback content = %q", got2)
	}
}

// A tool-use event writes a "⏺ " marker line, boundary-separated from any
// surrounding text.
func TestOutputLogToolMarker(t *testing.T) {
	dir := t.TempDir()
	l := openOutputLog(dir, "sess", 1, "task-1", "executor", 1, "fake-sonnet")
	l.write("before")
	l.tool(&provider.ToolUse{Name: "Read", Path: "engine.go"})
	l.write("after")
	l.close("pass", "unused")

	got, err := os.ReadFile(outputLogPath(dir, "sess", 1, "task-1", "executor", 1))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("log lines = %v", lines)
	}
	if lines[1] != "before" || lines[2] != "⏺ Read engine.go" || lines[3] != "after" {
		t.Errorf("body lines = %v", lines[1:4])
	}
}

// The engine captures a real run's streamed agent output to
// $MadsHome/runs/<session>/<run_seq>/<task>/<stage>-attempt-<n>.log — a
// provider that streams chunks and tool use (executor, scripted) leaves a
// header, the tool marker, the concatenated chunks, and a footer; a
// provider call with no OnEvent firing (code_review, unscripted, default
// "ok" reply) still leaves the header, the final text as a fallback, and a
// footer. Config.MadsHome == "" (every other engine test) writes nothing
// and never errors.
func TestEngineCapturesStageOutputLogs(t *testing.T) {
	h := newHarness(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "capture", Rules: []fake.Rule{
		{
			Match:  "Pipeline stage: executor",
			Tools:  []fake.ToolStep{{Name: "Read", Path: "engine.go"}},
			Chunks: []string{"Hello ", "World"},
		},
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
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("executor log lines = %v", lines)
	}
	if !strings.HasPrefix(lines[0], "=== stage=executor attempt=1 ") || !strings.Contains(lines[0], "started=") {
		t.Errorf("executor header = %q", lines[0])
	}
	if lines[1] != "⏺ Read engine.go" {
		t.Errorf("executor tool marker = %q", lines[1])
	}
	if lines[2] != "Hello " || lines[3] != "World" {
		t.Errorf("executor body = %v", lines[2:4])
	}
	if !strings.HasPrefix(lines[4], "=== stage=executor attempt=1 outcome=pass duration=") {
		t.Errorf("executor footer = %q", lines[4])
	}

	reviewLog := outputLogPath(madsHome, h.sess.ID, runSeq, seeded[0].ID, "code_review", 1)
	got, err = os.ReadFile(reviewLog)
	if err != nil {
		t.Fatalf("code_review log: %v", err)
	}
	reviewLines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(reviewLines) != 3 {
		t.Fatalf("code_review log lines = %v", reviewLines)
	}
	if !strings.HasPrefix(reviewLines[0], "=== stage=code_review attempt=1 ") {
		t.Errorf("code_review header = %q", reviewLines[0])
	}
	if !strings.Contains(reviewLines[1], "ok") {
		t.Errorf("code_review body = %q, want the fallback final text", reviewLines[1])
	}
	if !strings.HasPrefix(reviewLines[2], "=== stage=code_review attempt=1 outcome=pass duration=") {
		t.Errorf("code_review footer = %q", reviewLines[2])
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
