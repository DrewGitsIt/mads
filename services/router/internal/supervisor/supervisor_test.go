package supervisor_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/testleakcheck"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

var testChildBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mads-router-supervisor-test")
	if err != nil {
		panic(err)
	}

	testChildBin = filepath.Join(dir, "testchild")
	build := exec.Command("go", "build", "-o", testChildBin,
		"github.com/drewkidwell/mads/services/router/internal/testchild")
	if out, err := build.CombinedOutput(); err != nil {
		panic("build testchild: " + err.Error() + "\n" + string(out))
	}

	code := m.Run()

	if err := testleakcheck.Verify(testChildBin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// newSupervisor builds a Supervisor wired to the fast testchild binary and
// registers a t.Cleanup backstop that terminates every child still tracked
// when the test ends, so a test that returns early (e.g. via t.Fatal)
// doesn't leak a testchild process.
func newSupervisor(t *testing.T, data *dataclient.Client, jobs *job.Runner) *supervisor.Supervisor {
	t.Helper()
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, jobs)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	return sup
}

// eventually polls fn on a tight interval until it reports success or the
// budget elapses. Local process supervision should be fast; this is not
// meant to accommodate TTL expiry (the whole point of the test below is to
// prove supervision beats TTL expiry, not race it).
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

func TestExternalKillReleasesLeasesAndEmitsCrashEvent(t *testing.T) {
	base := datatest.Start(t)
	sup := newSupervisor(t, dataclient.New(base, "router"), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessionID := "ses_test"
	addr, err := sup.SpawnSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}
	if addr == "" {
		t.Fatal("SpawnSession: no address discovered")
	}

	admin := dataclient.New(base, "test-setup")
	task := contracts.Task{
		SessionID: sessionID,
		Title:     "seed task",
		Source:    "test",
		Status:    contracts.StatusInProgress,
	}
	if err := admin.CreateTask(ctx, &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Long enough that if the assertions below only pass because of TTL
	// expiry rather than supervision, they'd still be failing a few hundred
	// ms in — this proves supervision, not the backstop.
	sessionClient := dataclient.New(base, sessionID)
	if err := sessionClient.Claim(ctx, task.ID, 30*time.Second); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	pid, ok := sup.SessionPID(sessionID)
	if !ok {
		t.Fatalf("SessionPID(%s): not tracked", sessionID)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", pid, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill child pid %d: %v", pid, err)
	}

	eventually(t, 5*time.Second, func() bool {
		got, err := admin.Task(ctx, task.ID)
		return err == nil && got.Status == contracts.StatusBlocked
	})

	eventually(t, 5*time.Second, func() bool {
		events, err := admin.Events(ctx, 0, sessionID, []string{"session.crashed"})
		return err == nil && len(events) > 0
	})
}

func TestTerminateDoesNotEmitCrashEvent(t *testing.T) {
	base := datatest.Start(t)
	sup := newSupervisor(t, dataclient.New(base, "router"), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessionID := "ses_terminate"
	if _, err := sup.SpawnSession(ctx, sessionID); err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}

	if err := sup.TerminateSession(ctx, sessionID); err != nil {
		t.Fatalf("TerminateSession: %v", err)
	}

	admin := dataclient.New(base, "test-setup")
	// Terminate's exit handling has already completed by the time
	// TerminateSession returns (child.Terminate waits for onExit to
	// finish) — no polling budget needed to prove absence.
	events, err := admin.Events(ctx, 0, sessionID, []string{"session.crashed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("session.crashed events after Terminate: %v, want none", events)
	}
}

func TestPipelineExternalKillReleasesLeasesAndEmitsFailedEvent(t *testing.T) {
	base := datatest.Start(t)
	sup := newSupervisor(t, dataclient.New(base, "router"), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessionID := "ses_pipeline"
	runID := "run_test"
	addr, err := sup.SpawnPipeline(ctx, runID, sessionID)
	if err != nil {
		t.Fatalf("SpawnPipeline: %v", err)
	}
	if addr == "" {
		t.Fatal("SpawnPipeline: no address discovered")
	}

	admin := dataclient.New(base, "test-setup")
	task := contracts.Task{
		SessionID: sessionID,
		Title:     "seed task",
		Source:    "test",
		Status:    contracts.StatusInProgress,
	}
	if err := admin.CreateTask(ctx, &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	runClient := dataclient.New(base, runID)
	if err := runClient.Claim(ctx, task.ID, 30*time.Second); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	pid, ok := sup.PipelinePID(runID)
	if !ok {
		t.Fatalf("PipelinePID(%s): not tracked", runID)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", pid, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill child pid %d: %v", pid, err)
	}

	eventually(t, 5*time.Second, func() bool {
		got, err := admin.Task(ctx, task.ID)
		return err == nil && got.Status == contracts.StatusBlocked
	})

	eventually(t, 5*time.Second, func() bool {
		events, err := admin.Events(ctx, 0, sessionID, []string{"pipeline.failed"})
		return err == nil && len(events) > 0
	})
}

func TestPipelineCancelDoesNotEmitFailedEvent(t *testing.T) {
	base := datatest.Start(t)
	sup := newSupervisor(t, dataclient.New(base, "router"), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessionID := "ses_cancel"
	runID := "run_cancel"
	if _, err := sup.SpawnPipeline(ctx, runID, sessionID); err != nil {
		t.Fatalf("SpawnPipeline: %v", err)
	}

	if err := sup.TerminatePipeline(ctx, runID); err != nil {
		t.Fatalf("TerminatePipeline: %v", err)
	}

	admin := dataclient.New(base, "test-setup")
	events, err := admin.Events(ctx, 0, sessionID, []string{"pipeline.failed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("pipeline.failed events after Terminate: %v, want none", events)
	}
}

func TestSessionAndPipelineAddr(t *testing.T) {
	base := datatest.Start(t)
	sup := newSupervisor(t, dataclient.New(base, "router"), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, ok := sup.SessionAddr("no-such-session"); ok {
		t.Fatal("SessionAddr for untracked session: ok = true, want false")
	}
	if _, ok := sup.PipelineAddr("no-such-run"); ok {
		t.Fatal("PipelineAddr for untracked run: ok = true, want false")
	}

	sessionID := "ses_addr"
	sessionAddr, err := sup.SpawnSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}
	if got, ok := sup.SessionAddr(sessionID); !ok || got != sessionAddr {
		t.Fatalf("SessionAddr(%s) = %q, %v; want %q, true", sessionID, got, ok, sessionAddr)
	}

	runID := "run_addr"
	pipelineAddr, err := sup.SpawnPipeline(ctx, runID, sessionID)
	if err != nil {
		t.Fatalf("SpawnPipeline: %v", err)
	}
	if got, ok := sup.PipelineAddr(runID); !ok || got != pipelineAddr {
		t.Fatalf("PipelineAddr(%s) = %q, %v; want %q, true", runID, got, ok, pipelineAddr)
	}
}

// TerminateSession is the graceful active → inactive transition (spec §16):
// it must reach the same onSessionExit hook that spawns the session-summary
// job, proving the wiring end to end against a real job.Runner.
func TestTerminateSessionTriggersSessionSummary(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")

	f, err := fake.NewWithScenario(fake.Scenario{Name: "supervisor-summary", Rules: []fake.Rule{{
		Match: "seed task",
		Reply: `{"accomplished":"Did the seed task.","decisions":"","remaining":""}`,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	fm := fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
	jobs := job.New(data, fm)

	sup := newSupervisor(t, data, jobs)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess := contracts.Session{Name: "summary trigger session"}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := data.CreateTask(ctx, &contracts.Task{
		SessionID: sess.ID, Title: "seed task", Source: "test", Status: contracts.StatusDone,
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if _, err := sup.SpawnSession(ctx, sess.ID); err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}
	if err := sup.TerminateSession(ctx, sess.ID); err != nil {
		t.Fatalf("TerminateSession: %v", err)
	}

	want := "## What was accomplished\n\nDid the seed task."
	eventually(t, 5*time.Second, func() bool {
		got, err := data.Session(ctx, sess.ID)
		return err == nil && got.Summary == want
	})
}

// TestSessionResumeAfterGracefulStopRestart verifies the update lifecycle's
// "sessions resume" requirement (spec §14.4, W7.5) using only APIs that
// already exist: activity and task/worktree state are durable in the Data
// Service, so a graceful stop (the same TerminateSession call
// Supervisor.Shutdown makes for every tracked child — the "graceful agent
// stop" step of an update) followed by a fresh SpawnSession (the "restart
// services" step) must leave nothing lost or reset.
func TestSessionResumeAfterGracefulStopRestart(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := newSupervisor(t, data, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess := contracts.Session{Name: "update-resume session"}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	task := contracts.Task{
		SessionID: sess.ID,
		Title:     "survive an update",
		Source:    "test",
		Status:    contracts.StatusInProgress,
	}
	if err := data.CreateTask(ctx, &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if _, err := sup.SpawnSession(ctx, sess.ID); err != nil {
		t.Fatalf("SpawnSession (first): %v", err)
	}

	beforeSession, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Session (before): %v", err)
	}
	beforeTask, err := data.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task (before): %v", err)
	}

	// Step 1: "graceful agent stop" — the exact call Supervisor.Shutdown
	// makes internally for every tracked child.
	if err := sup.TerminateSession(ctx, sess.ID); err != nil {
		t.Fatalf("TerminateSession: %v", err)
	}

	afterStop, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Session (after stop): %v", err)
	}
	// Flipping Activity to Inactive on a graceful exit is the real Session
	// Service's job (runner.Shutdown), not the Supervisor's: testChildBin
	// is a bare fake spawn target with no Activity logic of its own, and
	// onSessionExit only sets Activity itself for an *unexpected* exit
	// (services/router/internal/supervisor/supervisor.go). That behavior is
	// already covered by TestShutdownReleasesLeases in
	// services/session/internal/runner/runner_test.go; this test instead
	// proves what the Supervisor + Data Service alone guarantee across a
	// stop/restart cycle: task and worktree state survive untouched.

	afterStopTask, err := data.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task (after stop): %v", err)
	}
	if afterStopTask.Status != beforeTask.Status || afterStopTask.Title != beforeTask.Title {
		t.Fatalf("task changed across graceful stop: before %+v, after %+v", beforeTask, afterStopTask)
	}
	if afterStop.WorktreeDir != beforeSession.WorktreeDir || afterStop.WorktreeBranch != beforeSession.WorktreeBranch {
		t.Fatalf("session worktree fields changed across graceful stop: before %+v, after %+v", beforeSession, afterStop)
	}

	// Step 2: "restart services" — spawn a fresh child under the same
	// session ID, simulating the update's post-swap restart.
	addr, err := sup.SpawnSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("SpawnSession (restart): %v", err)
	}
	if addr == "" {
		t.Fatal("SpawnSession (restart): no address discovered")
	}
	if _, ok := sup.SessionPID(sess.ID); !ok {
		t.Fatalf("SessionPID(%s): not tracked after restart", sess.ID)
	}
	if gotAddr, ok := sup.SessionAddr(sess.ID); !ok || gotAddr != addr {
		t.Fatalf("SessionAddr(%s) = %q, %v; want %q, true", sess.ID, gotAddr, ok, addr)
	}

	afterRestartTask, err := data.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task (after restart): %v", err)
	}
	if afterRestartTask.Status != beforeTask.Status || afterRestartTask.Title != beforeTask.Title {
		t.Fatalf("task state lost/reset across stop/restart cycle: before %+v, after %+v", beforeTask, afterRestartTask)
	}
}
