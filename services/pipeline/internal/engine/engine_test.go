package engine

import (
	"context"
	"os"
	"os/exec"
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
	"github.com/drewkidwell/mads/services/data/datatest"
)

// harness is one engine test's world: a real Data Service, a session, and a
// workspace directory where the default template's command stages pass
// (Makefile targets + a git repo where bare `git merge --no-ff` merges the
// configured upstream).
type harness struct {
	t    *testing.T
	base string
	data *dataclient.Client
	sess contracts.Session
	cwd  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, cwd: t.TempDir(), base: datatest.Start(t)}
	h.data = dataclient.New(h.base, "pipeline")
	h.sess = contracts.Session{Name: "engine run", Workspace: "test"}
	if err := h.data.CreateSession(context.Background(), &h.sess); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(h.cwd, "Makefile"),
		[]byte("build:\n\t@true\ntest:\n\t@true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "add", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"},
		{"config", "merge.defaultToUpstream", "true"},
		{"config", "branch.main.remote", "."},
		{"config", "branch.main.merge", "refs/heads/main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = h.cwd
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return h
}

// seed imports tasks and returns them in input order.
func (h *harness) seed(tasks ...contracts.Task) []contracts.Task {
	h.t.Helper()
	for i := range tasks {
		tasks[i].SessionID = h.sess.ID
		tasks[i].Source = "user"
	}
	out, err := h.data.ImportTasks(context.Background(), tasks)
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

// defaultTemplate fetches the seeded default-implementation from the store —
// the frozen-copy path a real launch takes.
func (h *harness) defaultTemplate() *template.Template {
	h.t.Helper()
	rec, err := h.data.Template(context.Background(), "default-implementation")
	if err != nil {
		h.t.Fatal(err)
	}
	tpl, verr := template.Check([]byte(rec.Source), func(string) bool { return true })
	if verr != nil {
		h.t.Fatal(verr)
	}
	return tpl
}

func (h *harness) run(t *testing.T, p provider.Provider, tpl *template.Template, concurrency int) *Summary {
	t.Helper()
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: p, SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: concurrency,
		StageTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func (h *harness) task(t *testing.T, id string) contracts.Task {
	t.Helper()
	task, err := h.data.Task(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func (h *harness) events(t *testing.T, types ...string) []contracts.Envelope {
	t.Helper()
	evs, err := h.data.Events(context.Background(), 0, h.sess.ID, types)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// The W3.1 done criterion: the seeded default template runs a seeded 3-task
// graph on provider-fake; a scripted review failure loops back exactly
// max_loops times, then blocks — and dependents never start.
func TestDefaultTemplateReviewLoopBack(t *testing.T) {
	h := newHarness(t)
	// The reviewer fails t2 every time: the rule keys on the stage AND the
	// task title, both present in the stage prompt.
	f, err := fake.NewWithScenario(fake.Scenario{Name: "review-fail", Rules: []fake.Rule{
		{Match: "code_review\nTask: flaky api", Reply: "This won't do.\nSTAGE: fail — missing error handling"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	seeded := h.seed(
		contracts.Task{Title: "core lib"},
		contracts.Task{Title: "flaky api"},
		contracts.Task{Title: "cli"},
	)
	// Chain: core lib ← flaky api ← cli.
	dep := seeded[1]
	dep.Dependencies = []string{seeded[0].ID}
	if err := h.data.UpdateTask(context.Background(), &dep); err != nil {
		t.Fatal(err)
	}
	dep = seeded[2]
	dep.Dependencies = []string{seeded[1].ID}
	if err := h.data.UpdateTask(context.Background(), &dep); err != nil {
		t.Fatal(err)
	}

	sum := h.run(t, f, h.defaultTemplate(), 2)

	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Errorf("done: %v, want just %s (core lib)", sum.Done, seeded[0].ID)
	}
	if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[1].ID {
		t.Errorf("blocked: %v, want just %s (flaky api)", sum.Blocked, seeded[1].ID)
	}
	if len(sum.Skipped) != 1 || sum.Skipped[0] != seeded[2].ID {
		t.Errorf("skipped: %v, want just %s (cli, dep never done)", sum.Skipped, seeded[2].ID)
	}

	// Review failed 3 times: initial + exactly max_loops(2) retries.
	var reviewFails int
	for _, ev := range h.events(t, "stage.failed") {
		if ev.TaskID == seeded[1].ID && strings.Contains(string(ev.Payload), "code_review") {
			reviewFails++
		}
	}
	if reviewFails != 3 {
		t.Errorf("code_review failures: %d, want 3 (1 + max_loops)", reviewFails)
	}

	// The blocked task carries the story: 2 loop-back observations + the
	// final blocker naming the stage.
	blocked := h.task(t, seeded[1].ID)
	if blocked.Status != contracts.StatusBlocked {
		t.Fatalf("flaky api status: %s", blocked.Status)
	}
	var loopNotes, blockNotes int
	for _, n := range blocked.Notes {
		switch {
		case n.Type == contracts.NoteObservation && strings.Contains(n.Content, "loop-back"):
			loopNotes++
		case n.Type == contracts.NoteBlocker && strings.Contains(n.Content, "code_review"):
			blockNotes++
		}
	}
	if loopNotes != 2 || blockNotes != 1 {
		t.Errorf("notes: %d loop-backs (want 2), %d blockers (want 1): %+v", loopNotes, blockNotes, blocked.Notes)
	}

	// The architect stage was skipped for nobody (no trivial labels), the
	// pipeline announced start and completion.
	if n := len(h.events(t, "pipeline.started")); n != 1 {
		t.Errorf("pipeline.started: %d", n)
	}
	done := h.events(t, "pipeline.completed")
	if len(done) != 1 || !strings.Contains(string(done[0].Payload), `"outcome":"incomplete"`) {
		t.Errorf("pipeline.completed: %+v", done)
	}
}

// when-guards skip stages: a trivial-labeled task never sees the architect.
func TestWhenGuardSkips(t *testing.T) {
	h := newHarness(t)
	// If the architect ran at all, it would fail the stage and block.
	f, err := fake.NewWithScenario(fake.Scenario{Name: "no-architect", Rules: []fake.Rule{
		{Match: "Pipeline stage: architect", Reply: "STAGE: fail — should have been skipped"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	seeded := h.seed(contracts.Task{Title: "typo fix", Labels: []string{"trivial"}})
	sum := h.run(t, f, h.defaultTemplate(), 1)
	if len(sum.Done) != 1 {
		t.Fatalf("summary: %+v", sum)
	}
	skips := h.events(t, "stage.skipped")
	if len(skips) != 1 || skips[0].TaskID != seeded[0].ID ||
		!strings.Contains(string(skips[0].Payload), "architect") {
		t.Errorf("stage.skipped: %+v", skips)
	}
}

// A failing command stage loops back with feedback, and the agent's
// feedback-informed retry really fixes it: the executor only writes the file
// the build checks for when its prompt carries the loop-back feedback.
func TestCommandFailureFeedbackLoop(t *testing.T) {
	h := newHarness(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "fix-on-feedback", Rules: []fake.Rule{
		{Match: "Address this feedback", Reply: "fixed it",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "fixed.marker", Content: "ok"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	src := `name: loop-test
stages:
  - name: executor
    agent: executor
  - name: build
    command: test -f fixed.marker
    on_fail: { loop_back: executor, max_loops: 2 }
`
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "make it build"})
	sum := h.run(t, f, tpl, 1)
	if len(sum.Done) != 1 {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}
	if _, err := os.Stat(filepath.Join(h.cwd, "fixed.marker")); err != nil {
		t.Error("the feedback-informed retry never wrote the fix")
	}
	fails := h.events(t, "stage.failed")
	if len(fails) != 1 || !strings.Contains(string(fails[0].Payload), "build") {
		t.Errorf("stage.failed: %+v", fails)
	}
}

// countingProvider tracks concurrent OneShot calls.
type countingProvider struct {
	provider.Provider
	mu      sync.Mutex
	current int32
	max     int32
}

func (c *countingProvider) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	cur := atomic.AddInt32(&c.current, 1)
	c.mu.Lock()
	if cur > c.max {
		c.max = cur
	}
	c.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	defer atomic.AddInt32(&c.current, -1)
	return c.Provider.OneShot(ctx, req)
}

// The concurrency cap holds: with 6 independent tasks and a limit of 2, at
// most 2 agent stages ever run at once — and at least 2 do (it's not
// accidentally serial).
func TestConcurrencyLimit(t *testing.T) {
	h := newHarness(t)
	cp := &countingProvider{Provider: fake.New()}
	src := "name: solo\nstages:\n  - name: executor\n    agent: executor\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	var batch []contracts.Task
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		batch = append(batch, contracts.Task{Title: name})
	}
	h.seed(batch...)
	sum := h.run(t, cp, tpl, 2)
	if len(sum.Done) != 6 {
		t.Fatalf("summary: %+v", sum)
	}
	if cp.max > 2 {
		t.Errorf("concurrency cap violated: %d simultaneous agent stages", cp.max)
	}
	if cp.max < 2 {
		t.Errorf("never actually parallel: max %d", cp.max)
	}
}

// Dependency cycles are legal at rest but block launch (spec §3.6).
func TestCyclesBlockLaunch(t *testing.T) {
	h := newHarness(t)
	seeded := h.seed(contracts.Task{Title: "a"}, contracts.Task{Title: "b"})
	ta, tb := seeded[0], seeded[1]
	ta.Dependencies = []string{tb.ID}
	tb.Dependencies = []string{ta.ID}
	for _, task := range []*contracts.Task{&ta, &tb} {
		if err := h.data.UpdateTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: h.defaultTemplate(), Cwd: h.cwd,
	})
	if err == nil || !strings.Contains(err.Error(), "cycles") {
		t.Fatalf("want cycle launch failure, got %v", err)
	}
	if n := len(h.events(t, "pipeline.started")); n != 0 {
		t.Errorf("pipeline.started emitted despite failed launch: %d", n)
	}
}

// A task leased elsewhere is not ours: the engine leaves it untouched and
// finishes the rest.
func TestForeignLeaseSkipsTask(t *testing.T) {
	h := newHarness(t)
	seeded := h.seed(contracts.Task{Title: "mine"}, contracts.Task{Title: "theirs"})
	other := dataclient.New(h.base, "session-ses_1")
	if err := other.Claim(context.Background(), seeded[1].ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	src := "name: solo\nstages:\n  - name: executor\n    agent: executor\n"
	tpl, _ := template.Check([]byte(src), func(string) bool { return true })
	sum := h.run(t, fake.New(), tpl, 2)
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Errorf("done: %v", sum.Done)
	}
	if len(sum.Skipped) != 1 || sum.Skipped[0] != seeded[1].ID {
		t.Errorf("skipped: %v", sum.Skipped)
	}
	if got := h.task(t, seeded[1].ID); got.Status != contracts.StatusReady {
		t.Errorf("foreign-leased task was touched: %s", got.Status)
	}
}
