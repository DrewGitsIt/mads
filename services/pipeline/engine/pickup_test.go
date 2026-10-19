package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// pickupTemplate: an agent stage before the one a test interrupts, so a
// resume can show it isn't rerun.
const pickupTemplate = `name: pick
stages:
  - name: plan
    agent: executor
  - name: executor
    agent: executor
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: make test
`

// pickupRules: the executor writes before.txt (the work an interruption must
// not lose); a resumed one-shot finishes the stage.
func pickupRules() []fake.Rule {
	return []fake.Rule{
		{Resumed: true, Reply: "picked it back up\nSTAGE: pass"},
		{Match: "Pipeline stage: executor", Reply: "working on it",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "before.txt", Content: "before\n"}}},
		{Match: "", Reply: "ok"},
	}
}

func pickupFake(t *testing.T) *fake.Fake {
	t.Helper()
	f, err := fake.NewWithScenario(fake.Scenario{Name: "pickup", AnnounceOneShotRef: true, Rules: pickupRules()})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// oneShotCall is one OneShot a pickupProvider saw.
type oneShotCall struct {
	prompt, resume string
	hadBefore      bool // before.txt existed in the call's cwd
}

// pickupProvider runs the fake, recording every call, and can park a call
// whose prompt contains park after the fake has done its work (wrote its
// files, announced its ref) until the run is stopped: an agent interrupted
// mid-stage.
type pickupProvider struct {
	*fake.Fake
	park     string
	parking  atomic.Bool
	parked   chan struct{}
	once     sync.Once
	noResume bool // declare one_shot_resume unsupported

	mu    sync.Mutex
	calls []oneShotCall
}

func newPickupProvider(f *fake.Fake, park string) *pickupProvider {
	p := &pickupProvider{Fake: f, park: park, parked: make(chan struct{})}
	p.parking.Store(park != "")
	return p
}

func (p *pickupProvider) Matrix() provider.Matrix {
	m := p.Fake.Matrix()
	if p.noResume {
		out := provider.Matrix{}
		for k, v := range m {
			out[k] = v
		}
		out[provider.BehaviorOneShotResume] = provider.Unsupported
		return out
	}
	return m
}

func (p *pickupProvider) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	_, statErr := os.Stat(filepath.Join(req.Cwd, "before.txt"))
	p.mu.Lock()
	p.calls = append(p.calls, oneShotCall{prompt: req.Prompt, resume: req.Resume, hadBefore: statErr == nil})
	p.mu.Unlock()
	if req.Resume != "" && p.noResume {
		return nil, provider.ErrResumeUnsupported
	}
	res, err := p.Fake.OneShot(ctx, req)
	if err != nil || req.Resume != "" || !p.parking.Load() || !strings.Contains(req.Prompt, p.park) {
		return res, err
	}
	p.once.Do(func() { close(p.parked) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *pickupProvider) callsMatching(sub string) []oneShotCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []oneShotCall
	for _, c := range p.calls {
		if strings.Contains(c.prompt, sub) {
			out = append(out, c)
		}
	}
	return out
}

// pickupWorld is one isolated session whose runs share a worktree root and
// MADS_HOME, as successive runs of one workspace do.
type pickupWorld struct {
	h          *harness
	root, home string
}

func newPickupWorld(t *testing.T) *pickupWorld {
	return &pickupWorld{h: newHarness(t), root: t.TempDir(), home: t.TempDir()}
}

type runOut struct {
	sum *Summary
	err error
}

func (w *pickupWorld) config(t *testing.T, p provider.Provider, src string, version int, runID string) Config {
	t.Helper()
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	return Config{
		Data: w.h.data, Provider: p, SessionID: w.h.sess.ID, RunID: runID,
		Template: tpl, TemplateVersion: version, Cwd: w.h.cwd, Concurrency: 2,
		StageTimeout: 30 * time.Second, LeaseTTL: time.Minute,
		Isolated: true, WorktreeRoot: w.root, DataURL: w.h.base, MadsHome: w.home,
		DisableSleepInhibit: true,
	}
}

func (w *pickupWorld) start(t *testing.T, cfg Config) (context.CancelFunc, <-chan runOut) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan runOut, 1)
	go func() {
		sum, err := Run(ctx, cfg)
		out <- runOut{sum, err}
	}()
	return cancel, out
}

func (w *pickupWorld) run(t *testing.T, cfg Config) *Summary {
	t.Helper()
	sum, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// stopMidStage runs cfg until p parks, then stops the run (the Router's
// Stop: ctx cancelled) and checks what Stop must leave behind: the task
// in_progress with no lease, no block, its stray work committed and its
// branch backed up.
func (w *pickupWorld) stopMidStage(t *testing.T, cfg Config, p *pickupProvider, task contracts.Task) {
	t.Helper()
	cancel, out := w.start(t, cfg)
	select {
	case <-p.parked:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("the interrupted stage never started")
	}
	cancel()
	var res runOut
	select {
	case res = <-out:
	case <-time.After(15 * time.Second):
		t.Fatal("run did not stop")
	}
	p.parking.Store(false)
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("stopped run err = %v, want context.Canceled", res.err)
	}
	if len(res.sum.Interrupted) != 1 || res.sum.Interrupted[0] != task.ID || len(res.sum.Blocked) != 0 {
		t.Errorf("stopped run summary = %+v, want just %s interrupted", res.sum, task.ID)
	}
	h := w.h
	if got := h.task(t, task.ID); got.Status != contracts.StatusInProgress {
		t.Fatalf("status after Stop = %s, want in_progress (Interrupted)", got.Status)
	}
	if leases, err := h.data.LeasesOfKind(context.Background(), "pipeline"); err != nil || len(leases) != 0 {
		t.Fatalf("leases after Stop = %+v (%v), want none", leases, err)
	}
	for _, n := range h.task(t, task.ID).Notes {
		if strings.Contains(n.Content, "run cancelled") {
			t.Errorf("Stop wrote a block note: %q", n.Content)
		}
	}
	if !branchExists(h.cwd, "backup/"+task.ID) {
		t.Errorf("Stop didn't back up mads/%s", task.ID)
	}
	if msg := gitOut(t, h.cwd, "log", "-1", "--format=%s", "mads/"+task.ID); !strings.Contains(msg, "auto-commit: interrupted work") {
		t.Errorf("mads/%s HEAD = %q, want the stray work committed on Stop", task.ID, msg)
	}
}

// runSeqOf is the pipeline.started seq of the run with this id.
func runSeqOf(t *testing.T, h *harness, runID string) int64 {
	t.Helper()
	for _, ev := range h.events(t, string(contracts.PipelineStarted)) {
		var p struct {
			RunID string `json:"run_id"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		if p.RunID == runID {
			return ev.Seq
		}
	}
	t.Fatalf("no pipeline.started for %s", runID)
	return 0
}

// stageEvents decodes a task's events of one type (all runs).
func stageEvents(t *testing.T, h *harness, typ contracts.EventType, taskID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range h.events(t, string(typ)) {
		if ev.TaskID != taskID {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func startedFor(t *testing.T, h *harness, taskID, stage string, runSeq int64) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, p := range stageEvents(t, h, contracts.StageStarted, taskID) {
		if p["stage"] == stage && (runSeq == 0 || int64(p["run_seq"].(float64)) == runSeq) {
			out = append(out, p)
		}
	}
	return out
}

func onePickup(t *testing.T, h *harness, taskID string) map[string]any {
	t.Helper()
	picks := stageEvents(t, h, contracts.StagePickedUp, taskID)
	if len(picks) != 1 {
		t.Fatalf("stage.picked_up events = %+v, want one", picks)
	}
	return picks[0]
}

// Stop mid agent stage, then a new run of the same template and version:
// the task's worktree is kept, the completed stage isn't rerun, and the
// interrupted agent session is resumed as the same attempt (its log in the
// new run's directory, noting what it resumes) — then the task finishes.
func TestPickupResumesInterruptedAgentStage(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	task := h.seed(contracts.Task{Title: "resumable"})[0]
	f := pickupFake(t)
	p := newPickupProvider(f, "Pipeline stage: executor")

	w.stopMidStage(t, w.config(t, p, pickupTemplate, 1, "run_1"), p, task)
	run1 := runSeqOf(t, h, "run_1")
	refs := stageEvents(t, h, contracts.StageAttemptRef, task.ID)
	var ref string
	for _, r := range refs {
		if r["stage"] == "executor" {
			ref, _ = r["ref"].(string)
		}
	}
	if ref == "" {
		t.Fatalf("run 1 recorded no executor ref: %+v", refs)
	}

	sum := w.run(t, w.config(t, p, pickupTemplate, 1, "run_2"))
	run2 := runSeqOf(t, h, "run_2")
	if len(sum.Done) != 1 || sum.Done[0] != task.ID {
		t.Fatalf("run 2 summary = %+v, want the task done", sum)
	}
	if got := h.task(t, task.ID); got.Status != contracts.StatusDone {
		t.Fatalf("status = %s, want done", got.Status)
	}

	if got := f.ResumeCalls(); len(got) != 1 || got[0] != ref {
		t.Errorf("resume calls = %v, want [%s]", got, ref)
	}
	resumed := p.callsMatching(resumePrompt)
	if len(resumed) != 1 || resumed[0].resume != ref || !resumed[0].hadBefore {
		t.Errorf("resumed calls = %+v, want one resuming %s in the kept worktree (before.txt there)", resumed, ref)
	}
	if n := len(p.callsMatching("Pipeline stage: plan")); n != 1 {
		t.Errorf("plan ran %d times, want once (completed in run 1, skipped on resume)", n)
	}
	if got := startedFor(t, h, task.ID, "plan", run2); len(got) != 0 {
		t.Errorf("run 2 started plan: %+v", got)
	}
	st := startedFor(t, h, task.ID, "executor", run2)
	if len(st) != 1 || st[0]["resumed"] != true || st[0]["attempt"] != float64(1) {
		t.Errorf("run 2 executor stage.started = %+v, want one {resumed: true, attempt: 1}", st)
	}
	pick := onePickup(t, h, task.ID)
	if pick["mode"] != "resume" || pick["from_stage"] != "executor" || pick["resumed_from_run_id"] != "run_1" ||
		pick["resumed_from_run_seq"] != float64(run1) || pick["run_seq"] != float64(run2) {
		t.Errorf("stage.picked_up = %+v", pick)
	}
	if done, _ := pick["completed_stages"].([]any); len(done) != 1 || done[0] != "plan" {
		t.Errorf("completed_stages = %v, want [plan]", pick["completed_stages"])
	}
	// The router finds the resumed attempt's ref by run and attempt.
	found := false
	for _, r := range stageEvents(t, h, contracts.StageAttemptRef, task.ID) {
		if r["run_seq"] == float64(run2) && r["attempt"] == float64(1) && r["ref"] == ref {
			found = true
		}
	}
	if !found {
		t.Error("run 2 recorded no stage.attempt_ref for the resumed attempt")
	}
	logPath := outputLogPath(w.home, h.sess.ID, run2, task.ID, "executor", 1)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("resumed attempt log: %v", err)
	}
	if first, _, _ := strings.Cut(string(raw), "\n"); !strings.Contains(first, "resumes run") || !strings.Contains(first, "attempt 1") {
		t.Errorf("resumed log's first line = %q, want a note of the run and attempt it resumes", first)
	}
	if _, err := os.Stat(filepath.Join(h.cwd, "before.txt")); err != nil {
		t.Errorf("the interrupted attempt's work didn't reach trunk: %v", err)
	}
	if branchExists(h.cwd, "backup/"+task.ID) {
		t.Errorf("backup/%s left after the task landed", task.ID)
	}
}

// A provider that can't resume a one-shot: the interrupted stage gets a new
// attempt in the same worktree, told it is resuming and given the
// interrupted attempt's output.
func TestPickupWithoutResumeStartsNewAttempt(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	task := h.seed(contracts.Task{Title: "no-resume"})[0]
	f := pickupFake(t)
	p := newPickupProvider(f, "Pipeline stage: executor")
	w.stopMidStage(t, w.config(t, p, pickupTemplate, 1, "run_1"), p, task)

	p.noResume = true
	sum := w.run(t, w.config(t, p, pickupTemplate, 1, "run_2"))
	if len(sum.Done) != 1 {
		t.Fatalf("summary = %+v, want the task done", sum)
	}
	if got := f.ResumeCalls(); len(got) != 0 {
		t.Errorf("resume calls = %v, want none (provider can't resume)", got)
	}
	assertResumingAttempt(t, w, p, task)
}

// A resume that errors (here: a provider that doesn't know the session)
// falls back to the same new attempt with context.
func TestPickupResumeErrorFallsBack(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	task := h.seed(contracts.Task{Title: "lost-session"})[0]
	p := newPickupProvider(pickupFake(t), "Pipeline stage: executor")
	w.stopMidStage(t, w.config(t, p, pickupTemplate, 1, "run_1"), p, task)

	fresh := pickupFake(t) // a new process: run 1's session is unknown to it
	p2 := newPickupProvider(fresh, "")
	sum := w.run(t, w.config(t, p2, pickupTemplate, 1, "run_2"))
	if len(sum.Done) != 1 {
		t.Fatalf("summary = %+v, want the task done", sum)
	}
	if got := fresh.ResumeCalls(); len(got) != 1 {
		t.Errorf("resume calls = %v, want one attempt", got)
	}
	st := startedFor(t, h, task.ID, "executor", runSeqOf(t, h, "run_2"))
	if len(st) != 2 || st[0]["resumed"] != true || st[1]["rerun"] != true || st[1]["resume_error"] == nil {
		t.Errorf("run 2 executor stage.started = %+v, want the resume then a rerun naming its error", st)
	}
	assertResumingAttempt(t, w, p2, task)
}

// assertResumingAttempt: run 2's executor ran as attempt 2 in the kept
// worktree, told it is resuming, with the interrupted attempt's output.
func assertResumingAttempt(t *testing.T, w *pickupWorld, p *pickupProvider, task contracts.Task) {
	t.Helper()
	h := w.h
	var resuming []oneShotCall
	for _, c := range p.callsMatching("You are resuming interrupted work") {
		if c.resume == "" {
			resuming = append(resuming, c)
		}
	}
	if len(resuming) != 1 || !resuming[0].hadBefore || !strings.Contains(resuming[0].prompt, "working on it") {
		t.Fatalf("resuming calls = %+v, want one in the kept worktree carrying the interrupted output", resuming)
	}
	st := startedFor(t, h, task.ID, "executor", runSeqOf(t, h, "run_2"))
	last := st[len(st)-1]
	if last["rerun"] != true || last["attempt"] != float64(2) {
		t.Errorf("run 2 executor stage.started = %+v, want a rerun as attempt 2", st)
	}
	if _, err := os.Stat(outputLogPath(w.home, h.sess.ID, runSeqOf(t, h, "run_2"), task.ID, "executor", 2)); err != nil {
		t.Errorf("attempt 2 log: %v", err)
	}
	if got := h.task(t, task.ID); got.Status != contracts.StatusDone {
		t.Errorf("status = %s, want done", got.Status)
	}
}

// Stop mid command stage: the pickup reruns it as a new attempt, and the
// agent stage before it is not rerun.
func TestPickupRerunsInterruptedCommandStage(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	gate := filepath.Join(t.TempDir(), "go")
	src := `name: cmd
stages:
  - name: executor
    agent: executor
  - name: build
    command: test -f ` + gate + ` || sleep 60
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: make test
`
	task := h.seed(contracts.Task{Title: "cmd-stop"})[0]
	p := newPickupProvider(pickupFake(t), "")
	cancel, out := w.start(t, w.config(t, p, src, 4, "run_1"))
	deadline := time.Now().Add(15 * time.Second)
	for len(startedFor(t, h, task.ID, "build", 0)) == 0 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("build never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if res := <-out; len(res.sum.Interrupted) != 1 {
		t.Fatalf("stopped run summary = %+v", res.sum)
	}
	if got := h.task(t, task.ID); got.Status != contracts.StatusInProgress {
		t.Fatalf("status after Stop = %s", got.Status)
	}
	if f := stageEvents(t, h, contracts.StageFailed, task.ID); len(f) != 0 {
		t.Errorf("a stopped command stage reported failure: %+v", f)
	}

	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := w.run(t, w.config(t, p, src, 4, "run_2"))
	if len(sum.Done) != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	run2 := runSeqOf(t, h, "run_2")
	st := startedFor(t, h, task.ID, "build", run2)
	if len(st) != 1 || st[0]["rerun"] != true || st[0]["attempt"] != float64(2) {
		t.Errorf("run 2 build stage.started = %+v, want a rerun as attempt 2", st)
	}
	if n := len(p.callsMatching("Pipeline stage: executor")); n != 1 {
		t.Errorf("executor ran %d times, want once", n)
	}
	if pick := onePickup(t, h, task.ID); pick["mode"] != "resume" || pick["from_stage"] != "build" {
		t.Errorf("stage.picked_up = %+v", pick)
	}
}

// seedMergeInFlight fakes a run that died inside the merge stage: the task
// in_progress with no lease, its worktree and branch holding one commit,
// and that run's events up to the merge stage's start.
func seedMergeInFlight(t *testing.T, w *pickupWorld, task contracts.Task) {
	t.Helper()
	h := w.h
	ctx := context.Background()
	task.Status = contracts.StatusInProgress
	if err := h.data.UpdateTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(w.root, task.ID)
	gitRun(t, h.cwd, "worktree", "add", "-b", "mads/"+task.ID, dir)
	if err := os.WriteFile(filepath.Join(dir, "landed.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "work")

	appendEv := func(typ contracts.EventType, taskID string, payload map[string]any) int64 {
		raw, _ := json.Marshal(payload)
		env := &contracts.Envelope{Type: typ, SessionID: h.sess.ID, TaskID: taskID, Payload: raw}
		if err := h.data.Append(ctx, env); err != nil {
			t.Fatal(err)
		}
		return env.Seq
	}
	run := appendEv(contracts.PipelineStarted, "", map[string]any{"run_id": "run_dead", "template": "isolated", "template_version": 1})
	appendEv(contracts.StageStarted, task.ID, map[string]any{"stage": "executor", "attempt": 1, "run_seq": run})
	appendEv(contracts.StageCompleted, task.ID, map[string]any{"stage": "executor", "run_seq": run})
	appendEv(contracts.StageStarted, task.ID, map[string]any{"stage": "merge", "attempt": 1, "run_seq": run})
}

func mergeCommits(t *testing.T, h *harness, taskID string) int {
	t.Helper()
	n := 0
	for _, s := range strings.Split(gitOut(t, h.cwd, "log", "--merges", "--format=%s"), "\n") {
		if strings.Contains(s, "mads/"+taskID) {
			n++
		}
	}
	return n
}

// The merge stage was in flight but the merge had already landed: the
// pickup completes the stage without merging again.
func TestPickupMergeAlreadyLanded(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	task := h.seed(contracts.Task{Title: "landed"})[0]
	seedMergeInFlight(t, w, task)
	gitRun(t, h.cwd, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "--no-ff", "-q", "mads/"+task.ID, "-m", "Merge mads/"+task.ID)

	sum := w.run(t, w.config(t, newPickupProvider(pickupFake(t), ""), isolatedTemplate, 1, "run_2"))
	if len(sum.Done) != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if n := mergeCommits(t, h, task.ID); n != 1 {
		t.Errorf("merge commits for the task = %d, want 1 (no double merge)", n)
	}
	if evs := h.events(t, string(contracts.MergeAttempted)); len(evs) != 0 {
		t.Errorf("merge.attempted = %d, want none (already landed)", len(evs))
	}
	if got := startedFor(t, h, task.ID, "executor", runSeqOf(t, h, "run_2")); len(got) != 0 {
		t.Errorf("executor rerun on pickup: %+v", got)
	}
}

// The merge stage was in flight with the merge half done in the primary:
// the pickup aborts it and merges again, once.
func TestPickupAbortsHalfMergeAndRedoes(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	task := h.seed(contracts.Task{Title: "half"})[0]
	seedMergeInFlight(t, w, task)
	gitRun(t, h.cwd, "merge", "--no-ff", "--no-commit", "mads/"+task.ID)
	if _, err := os.Stat(filepath.Join(h.cwd, ".git", "MERGE_HEAD")); err != nil {
		t.Fatalf("setup: no half merge: %v", err)
	}

	sum := w.run(t, w.config(t, newPickupProvider(pickupFake(t), ""), isolatedTemplate, 1, "run_2"))
	if len(sum.Done) != 1 {
		t.Fatalf("summary = %+v; task %+v", sum, h.task(t, task.ID))
	}
	if n := mergeCommits(t, h, task.ID); n != 1 {
		t.Errorf("merge commits for the task = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(h.cwd, ".git", "MERGE_HEAD")); err == nil {
		t.Error("MERGE_HEAD still present")
	}
	if _, err := os.Stat(filepath.Join(h.cwd, "landed.txt")); err != nil {
		t.Errorf("the task's work isn't in trunk: %v", err)
	}
}

// A different template restarts from its first stage, keeping the
// worktree, and its first agent stage is told about the prior work.
func TestPickupDifferentTemplateRestartsKeepingWorktree(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	task := h.seed(contracts.Task{Title: "switched"})[0]
	p := newPickupProvider(pickupFake(t), "Pipeline stage: executor")
	w.stopMidStage(t, w.config(t, p, pickupTemplate, 1, "run_1"), p, task)

	other := `name: other
stages:
  - name: draft
    agent: executor
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: make test
`
	sum := w.run(t, w.config(t, p, other, 1, "run_2"))
	if len(sum.Done) != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if got := p.ResumeCalls(); len(got) != 0 {
		t.Errorf("resume calls = %v, want none on a restart", got)
	}
	drafts := p.callsMatching("Pipeline stage: draft")
	if len(drafts) != 1 || !drafts[0].hadBefore || !strings.Contains(drafts[0].prompt, "An earlier pipeline run worked on this task") {
		t.Errorf("draft calls = %+v, want one in the kept worktree, told about the prior work", drafts)
	}
	pick := onePickup(t, h, task.ID)
	if pick["mode"] != "restart" || pick["from_stage"] != "draft" || pick["resumed_from_run_id"] != "run_1" {
		t.Errorf("stage.picked_up = %+v", pick)
	}
	if _, err := os.Stat(filepath.Join(h.cwd, "before.txt")); err != nil {
		t.Errorf("run 1's work didn't survive the restart into trunk: %v", err)
	}
}

// A ready task still gets a fresh worktree: whatever a stale earlier
// attempt left on its branch is discarded.
func TestReadyTaskGetsFreshWorktree(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	task := h.seed(contracts.Task{Title: "fresh"})[0]
	dir := filepath.Join(w.root, task.ID)
	gitRun(t, h.cwd, "worktree", "add", "-b", "mads/"+task.ID, dir)
	if err := os.WriteFile(filepath.Join(dir, "stale.txt"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "stale")

	sum := w.run(t, w.config(t, newPickupProvider(pickupFake(t), ""), isolatedTemplate, 1, "run_1"))
	if len(sum.Done) != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if _, err := os.Stat(filepath.Join(h.cwd, "stale.txt")); err == nil {
		t.Error("a ready task kept a stale worktree's work")
	}
	if picks := stageEvents(t, h, contracts.StagePickedUp, task.ID); len(picks) != 0 {
		t.Errorf("a ready task was 'picked up': %+v", picks)
	}
}

// Blocked and needs_review tasks need a person: never Interrupted, never
// picked up, even with no lease.
func TestBlockedAndNeedsReviewNeverPickedUp(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	seeded := h.seed(contracts.Task{Title: "stuck"}, contracts.Task{Title: "review me"})
	for i, st := range []contracts.TaskStatus{contracts.StatusBlocked, contracts.StatusNeedsReview} {
		task := seeded[i]
		task.Status = st
		if err := h.data.UpdateTask(context.Background(), &task); err != nil {
			t.Fatal(err)
		}
	}
	p := newPickupProvider(pickupFake(t), "")
	sum := w.run(t, w.config(t, p, isolatedTemplate, 1, "run_1"))
	if len(sum.Done)+len(sum.Blocked)+len(sum.Skipped)+len(sum.LeftOut) != 0 {
		t.Errorf("summary = %+v, want nothing touched", sum)
	}
	if n := len(p.callsMatching("")); n != 0 {
		t.Errorf("%d agent calls, want none", n)
	}
	if got := h.task(t, seeded[0].ID).Status; got != contracts.StatusBlocked {
		t.Errorf("blocked task status = %s", got)
	}
	if got := h.task(t, seeded[1].ID).Status; got != contracts.StatusNeedsReview {
		t.Errorf("needs_review task status = %s", got)
	}
}

// claimCounter fronts the data service and counts claim requests per task.
type claimCounter struct {
	mu     sync.Mutex
	claims map[string]int
	url    string
}

func newClaimCounter(t *testing.T, base string) *claimCounter {
	t.Helper()
	target, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	c := &claimCounter{claims: map[string]int{}}
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if id, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/tasks/"), "/claim"); ok && r.Method == http.MethodPost {
			c.mu.Lock()
			c.claims[id]++
			c.mu.Unlock()
		}
		proxy.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	c.url = srv.URL
	return c
}

func (c *claimCounter) count(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.claims[id]
}

// An in_progress task another live run holds is that run's: this run
// neither counts it nor claims it on every scan. A pipeline run's lease is
// seen up front (no claim at all); a lease of another kind costs one claim,
// then a backoff.
func TestInterruptedHeldElsewhereIsSkippedQuietly(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	ctx := context.Background()
	seeded := h.seed(contracts.Task{Title: "other run's"}, contracts.Task{Title: "someone's"}, contracts.Task{Title: "slow"})
	other := dataclient.New(h.base, "pipeline")
	if err := other.Claim(ctx, seeded[0].ID, "pipeline:run_other", time.Minute); err != nil {
		t.Fatal(err)
	}
	someone := dataclient.New(h.base, "thief")
	if err := someone.Claim(ctx, seeded[1].ID, "", time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, task := range seeded[:2] {
		task.Status = contracts.StatusInProgress
		c := other
		if task.ID == seeded[1].ID {
			c = someone
		}
		if err := c.UpdateTask(ctx, &task); err != nil {
			t.Fatal(err)
		}
	}

	counter := newClaimCounter(t, h.base)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "slow", Rules: []fake.Rule{
		{Match: "Task: slow", Reply: "ok", DelayMs: 2500}, // outlasts a 2s rescan
		{Match: "", Reply: "ok"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := w.config(t, f, soloTemplate, 1, "run_1")
	cfg.Data = dataclient.New(counter.url, "pipeline")
	cfg.Isolated = false
	sum := w.run(t, cfg)

	if len(sum.Done) != 1 || sum.Done[0] != seeded[2].ID || len(sum.Skipped)+len(sum.Blocked)+len(sum.LeftOut) != 0 {
		t.Errorf("summary = %+v, want only the slow task, done; the held ones not counted", sum)
	}
	if n := counter.count(seeded[0].ID); n != 0 {
		t.Errorf("claims on the task another pipeline run holds = %d, want 0", n)
	}
	if n := counter.count(seeded[1].ID); n != 1 {
		t.Errorf("claims on the task held by another kind = %d, want 1 (then backoff)", n)
	}
	for _, id := range []string{seeded[0].ID, seeded[1].ID} {
		if got := h.task(t, id).Status; got != contracts.StatusInProgress {
			t.Errorf("held task %s status = %s, want in_progress (untouched)", id, got)
		}
	}
	if err := other.Renew(ctx, seeded[0].ID); err != nil {
		t.Errorf("the other run's lease was disturbed: %v", err)
	}
}

// The exclude box and left-out rules apply to Interrupted tasks as to
// ready ones: an excluded Interrupted task is left out, and so is a task
// (ready or Interrupted) stranded only by it.
func TestInterruptedTasksHonourExclusions(t *testing.T) {
	w := newPickupWorld(t)
	h := w.h
	ctx := context.Background()
	seeded := h.seed(contracts.Task{Title: "excluded"}, contracts.Task{Title: "after it"}, contracts.Task{Title: "free"})
	for _, i := range []int{0, 1} {
		task := seeded[i]
		task.Status = contracts.StatusInProgress
		if i == 1 {
			task.Dependencies = []string{seeded[0].ID}
		}
		if err := h.data.UpdateTask(ctx, &task); err != nil {
			t.Fatal(err)
		}
	}
	p := newPickupProvider(pickupFake(t), "")
	cfg := w.config(t, p, isolatedTemplate, 1, "run_1")
	cfg.ExcludeTaskIDs = []string{seeded[0].ID}
	sum := w.run(t, cfg)

	if len(sum.Done) != 1 || sum.Done[0] != seeded[2].ID {
		t.Errorf("done = %v, want just the free task", sum.Done)
	}
	if len(sum.LeftOut) != 2 || len(sum.Skipped) != 0 {
		t.Errorf("left out = %v, skipped = %v; want the excluded task and its dependent left out", sum.LeftOut, sum.Skipped)
	}
	for _, i := range []int{0, 1} {
		if got := h.task(t, seeded[i].ID).Status; got != contracts.StatusInProgress {
			t.Errorf("%s status = %s, want in_progress (untouched)", seeded[i].Title, got)
		}
	}
}
