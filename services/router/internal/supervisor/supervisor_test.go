package supervisor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
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

// newCredStore opens a credstore in a fresh temp dir seeded with name→value
// for each entry in creds.
func newCredStore(t *testing.T, creds map[string]string) *credstore.Store {
	t.Helper()
	store, err := credstore.Open(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatalf("credstore.Open: %v", err)
	}
	for name, value := range creds {
		if err := store.Set(name, value); err != nil {
			t.Fatalf("credstore.Set(%s): %v", name, err)
		}
	}
	return store
}

// envPresent asks a testchild process, over its "-addr"-discovered HTTP
// address, whether name is set in its own OS environment — the only way to
// observe a separately-spawned process's env from a test.
func envPresent(t *testing.T, addr, name string) bool {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://%s/env?name=%s", addr, name))
	if err != nil {
		t.Fatalf("GET /env?name=%s: %v", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Present bool `json:"present"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /env response: %v", err)
	}
	return body.Present
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
	addr, err := sup.SpawnPipeline(ctx, runID, sessionID, "tpl_test")
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
	if _, err := sup.SpawnPipeline(ctx, runID, sessionID, "tpl_test"); err != nil {
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

func enableLinkedSessions(t *testing.T, data *dataclient.Client) {
	t.Helper()
	cfg, err := data.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.FeatureFlags.LinkedSessions = true
	if err := data.UpdateConfig(context.Background(), &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
}

// Edge A (docs/LINKED-SESSIONS-DESIGN.md decision 6): a pipeline run starting
// for a session whose interactive child is already live grants the implicit
// hand-raiser immediately.
func TestSpawnPipelineGrantsImplicitHandRaiserWhenSessionLive(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)
	sup := newSupervisor(t, data, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess := contracts.Session{Name: "edge a session"}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sessionID := sess.ID
	if _, err := sup.SpawnSession(ctx, sessionID); err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}
	if _, err := sup.SpawnPipeline(ctx, "run_edge_a", sessionID, "tpl_test"); err != nil {
		t.Fatalf("SpawnPipeline: %v", err)
	}

	eventually(t, 5*time.Second, func() bool {
		links, err := data.LinksForSession(ctx, sessionID)
		return err == nil && len(links) == 1 && links[0].Implicit
	})
}

// Edge B: the session's interactive child starting while a pipeline run is
// already in flight for it also grants the implicit hand-raiser.
func TestSpawnSessionGrantsImplicitHandRaiserWhenPipelineLive(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)
	sup := newSupervisor(t, data, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess := contracts.Session{Name: "edge b session"}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sessionID := sess.ID
	if _, err := sup.SpawnPipeline(ctx, "run_edge_b", sessionID, "tpl_test"); err != nil {
		t.Fatalf("SpawnPipeline: %v", err)
	}
	if _, err := sup.SpawnSession(ctx, sessionID); err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}

	eventually(t, 5*time.Second, func() bool {
		links, err := data.LinksForSession(ctx, sessionID)
		return err == nil && len(links) == 1 && links[0].Implicit
	})
}

// A pipeline run starting for a session with no live interactive child must
// not grant anything — there's no one to raise a hand to.
func TestSpawnPipelineNoImplicitHandRaiserWithoutLiveChild(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)
	sup := newSupervisor(t, data, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessionID := "ses_no_child"
	if _, err := sup.SpawnPipeline(ctx, "run_no_child", sessionID, "tpl_test"); err != nil {
		t.Fatalf("SpawnPipeline: %v", err)
	}

	// Give any (incorrect) implicit grant a beat to land before asserting its
	// absence.
	time.Sleep(200 * time.Millisecond)
	links, err := data.LinksForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("LinksForSession: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("links for session with no live child: %v, want none", links)
	}
}

// When the pipeline run that granted the implicit hand-raiser completes and
// the session's interactive child is still live, the implicit link is
// removed — it should not outlive the run that created it.
func TestPipelineExitRevokesImplicitHandRaiser(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)
	sup := newSupervisor(t, data, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess := contracts.Session{Name: "teardown session"}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sessionID := sess.ID
	runID := "run_teardown"
	if _, err := sup.SpawnSession(ctx, sessionID); err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}
	if _, err := sup.SpawnPipeline(ctx, runID, sessionID, "tpl_test"); err != nil {
		t.Fatalf("SpawnPipeline: %v", err)
	}

	eventually(t, 5*time.Second, func() bool {
		links, err := data.LinksForSession(ctx, sessionID)
		return err == nil && len(links) == 1
	})

	if err := sup.TerminatePipeline(ctx, runID); err != nil {
		t.Fatalf("TerminatePipeline: %v", err)
	}

	eventually(t, 5*time.Second, func() bool {
		links, err := data.LinksForSession(ctx, sessionID)
		return err == nil && len(links) == 0
	})
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
	pipelineAddr, err := sup.SpawnPipeline(ctx, runID, sessionID, "tpl_test")
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
	jobs := job.New(data, fm, registryclient.New(""))

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

// TerminateSession must reconcile a session that says active but has no
// tracked child — the post-Router-restart orphan case (Supervisor state is
// in-memory only). Truth-enforcement, not a no-op: flips Activity and
// emits the same session.activity_changed event a real exit would.
func TestTerminateReconcilesOrphanedActiveSession(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := newSupervisor(t, data, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess := contracts.Session{Name: "orphaned session"}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Never spawned — simulates a session left active by a prior Router
	// process that no longer has this session's child tracked.

	if err := sup.TerminateSession(ctx, sess.ID); err != nil {
		t.Fatalf("TerminateSession: %v", err)
	}

	got, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if got.Activity != contracts.SessionInactive {
		t.Fatalf("Activity after Terminate on orphan = %q, want inactive", got.Activity)
	}

	events, err := data.Events(ctx, 0, sess.ID, []string{"session.activity_changed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no session.activity_changed event after reconciling an orphaned-active session")
	}
}

// The orphan-reconciliation path must trigger the same session-summary job
// a real exit would (spec §16: every active → inactive transition triggers
// one) — proving reconcileInactiveSession isn't a partial imitation of
// onSessionExit.
func TestTerminateOrphanedActiveSessionTriggersSessionSummary(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")

	f, err := fake.NewWithScenario(fake.Scenario{Name: "orphan-summary", Rules: []fake.Rule{{
		Match: "seed task",
		Reply: `{"accomplished":"Did the seed task.","decisions":"","remaining":""}`,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	fm := fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
	jobs := job.New(data, fm, registryclient.New(""))

	sup := newSupervisor(t, data, jobs)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess := contracts.Session{Name: "orphan summary session"}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := data.CreateTask(ctx, &contracts.Task{
		SessionID: sess.ID, Title: "seed task", Source: "test", Status: contracts.StatusDone,
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
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

// The boot sweep must reconcile orphaned-active sessions to inactive while
// never fighting a live pipeline engine (a session with an unexpired task
// write-lease — the signal a CLI `mads run` is genuinely driving it — or a
// session with a real tracked child).
func TestBootSweepReconcilesOrphanedActiveSession(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := newSupervisor(t, data, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Session A: orphan, no lease — the sweep must flip it to inactive.
	orphan := contracts.Session{Name: "boot sweep orphan"}
	if err := data.CreateSession(ctx, &orphan); err != nil {
		t.Fatalf("CreateSession (orphan): %v", err)
	}

	// Session B: active with a live task lease — simulates a CLI `mads run`
	// with no Router-tracked child. The sweep must leave it alone.
	engineActive := contracts.Session{Name: "boot sweep engine-active"}
	if err := data.CreateSession(ctx, &engineActive); err != nil {
		t.Fatalf("CreateSession (engine-active): %v", err)
	}
	engineTask := contracts.Task{
		SessionID: engineActive.ID, Title: "engine task", Source: "test", Status: contracts.StatusInProgress,
	}
	if err := data.CreateTask(ctx, &engineTask); err != nil {
		t.Fatalf("CreateTask (engine-active): %v", err)
	}
	engineClient := dataclient.New(base, "external-mads-run")
	if err := engineClient.Claim(ctx, engineTask.ID, 30*time.Second); err != nil {
		t.Fatalf("Claim (engine-active): %v", err)
	}

	// Session C: active with a real tracked child, no lease. The `tracked`
	// check must short-circuit before ever consulting leases.
	tracked := contracts.Session{Name: "boot sweep tracked"}
	if err := data.CreateSession(ctx, &tracked); err != nil {
		t.Fatalf("CreateSession (tracked): %v", err)
	}
	if _, err := sup.SpawnSession(ctx, tracked.ID); err != nil {
		t.Fatalf("SpawnSession (tracked): %v", err)
	}

	sup.ReconcileOrphanedSessions(ctx)

	got, err := data.Session(ctx, orphan.ID)
	if err != nil {
		t.Fatalf("Session (orphan): %v", err)
	}
	if got.Activity != contracts.SessionInactive {
		t.Fatalf("orphan session Activity after sweep = %q, want inactive", got.Activity)
	}

	got, err = data.Session(ctx, engineActive.ID)
	if err != nil {
		t.Fatalf("Session (engine-active): %v", err)
	}
	if got.Activity != contracts.SessionActive {
		t.Fatalf("engine-active session Activity after sweep = %q, want active (must not fight a live engine)", got.Activity)
	}

	got, err = data.Session(ctx, tracked.ID)
	if err != nil {
		t.Fatalf("Session (tracked): %v", err)
	}
	if got.Activity != contracts.SessionActive {
		t.Fatalf("tracked session Activity after sweep = %q, want active", got.Activity)
	}
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

// With no CredentialBindings set, a spawned session's environment contains
// every stored credential (backward compatible with pre-profiles-v2
// installs).
func TestSpawnSessionInjectsAllCredentialsWhenNoBindings(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := newSupervisor(t, data, nil)
	sup.SetCreds(newCredStore(t, map[string]string{"CRED_A": "secret-a", "CRED_B": "secret-b"}))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	addr, err := sup.SpawnSession(ctx, "ses_all_creds")
	if err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}
	if !envPresent(t, addr, "CRED_A") {
		t.Error("CRED_A not present in spawned env, want present (no bindings = all credentials)")
	}
	if !envPresent(t, addr, "CRED_B") {
		t.Error("CRED_B not present in spawned env, want present (no bindings = all credentials)")
	}
}

// With a binding set of {CRED_A}, a spawned session's environment contains
// CRED_A but not CRED_B (profiles-v2 §13 enforcement).
func TestSpawnSessionInjectsOnlyBoundCredentials(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := newSupervisor(t, data, nil)
	sup.SetCreds(newCredStore(t, map[string]string{"CRED_A": "secret-a", "CRED_B": "secret-b"}))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cfg, err := data.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.CredentialBindings = []string{"CRED_A"}
	if err := data.UpdateConfig(ctx, &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	addr, err := sup.SpawnSession(ctx, "ses_bound_creds")
	if err != nil {
		t.Fatalf("SpawnSession: %v", err)
	}
	if !envPresent(t, addr, "CRED_A") {
		t.Error("CRED_A not present in spawned env, want present (bound)")
	}
	if envPresent(t, addr, "CRED_B") {
		t.Error("CRED_B present in spawned env, want absent (not bound)")
	}
}

// Changing the binding set (profile switch) affects only the NEXT spawned
// session; a session already spawned under the old binding set keeps
// whatever it was given at spawn time — a process's env is fixed for its
// lifetime; nothing re-injects credentials into a running child.
func TestCredentialBindingChangeAffectsOnlyNextSpawn(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := newSupervisor(t, data, nil)
	sup.SetCreds(newCredStore(t, map[string]string{"CRED_A": "secret-a", "CRED_B": "secret-b"}))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Spawned first, under no bindings: gets both credentials.
	firstAddr, err := sup.SpawnSession(ctx, "ses_before_switch")
	if err != nil {
		t.Fatalf("SpawnSession (first): %v", err)
	}
	if !envPresent(t, firstAddr, "CRED_A") || !envPresent(t, firstAddr, "CRED_B") {
		t.Fatal("first session missing a credential before any binding was set")
	}

	// Profile switch: restrict to {CRED_A}.
	cfg, err := data.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.CredentialBindings = []string{"CRED_A"}
	if err := data.UpdateConfig(ctx, &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	// The already-running first session is untouched: still has both.
	if !envPresent(t, firstAddr, "CRED_A") || !envPresent(t, firstAddr, "CRED_B") {
		t.Error("already-running session's env changed after a binding-set switch, want untouched")
	}

	// The next spawned session only gets the newly bound credential.
	secondAddr, err := sup.SpawnSession(ctx, "ses_after_switch")
	if err != nil {
		t.Fatalf("SpawnSession (second): %v", err)
	}
	if !envPresent(t, secondAddr, "CRED_A") {
		t.Error("second session missing CRED_A, want present (bound)")
	}
	if envPresent(t, secondAddr, "CRED_B") {
		t.Error("second session has CRED_B, want absent (not bound after switch)")
	}
}
