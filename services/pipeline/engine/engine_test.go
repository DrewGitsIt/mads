package engine

import (
	"context"
	"encoding/json"
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
	"github.com/drewkidwell/mads/providers/fastmodel"
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
	ws := contracts.Workspace{Name: "test"}
	if err := h.data.CreateWorkspace(context.Background(), &ws); err != nil {
		t.Fatal(err)
	}
	h.sess = contracts.Session{Name: "engine run", Workspace: "test"}
	if err := h.data.CreateSession(context.Background(), &h.sess); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(h.cwd, "Makefile"),
		[]byte("build:\n\t@true\ntest:\n\t@true\ncheck:\n\t@true\n"), 0o644); err != nil {
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

// waitStatus polls until task id reaches status, failing the test if it
// doesn't within timeout. Replaces fixed sleeps used to synchronize with
// the engine's real scheduling latency, which a fixed sleep can't bound
// once the suite runs under load.
func (h *harness) waitStatus(t *testing.T, id string, status contracts.TaskStatus, timeout time.Duration) contracts.Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		task, err := h.data.Task(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == status {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s: status %s not reached within %s (last seen: %s)", id, status, timeout, task.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
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

// A build -> executor loop-back must advance EXECUTOR's own attempt number,
// not build's: e.loops is keyed on the stage whose on_fail fired (build), so
// reading e.loops for the attempt left the looped-into stage (executor)
// stuck reporting attempt 1 forever on every re-run. Concretely that bug
// meant openOutputLog (opened O_APPEND) silently appended the second
// executor run into executor-attempt-1.log instead of creating
// executor-attempt-2.log, and usage.recorded/stage.attempt_ref both
// mislabeled the second run as attempt 1. This is the same loop-back
// scenario as TestCommandFailureFeedbackLoop, with MadsHome set so the
// output logs are captured and the attempt numbering is directly observable.
func TestLoopBackAdvancesLoopedIntoStageAttempt(t *testing.T) {
	h := newHarness(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "build-loop-attempt", Rules: []fake.Rule{
		// More specific rule first: the fake matches in order, and the
		// feedback-bearing second attempt must not fall through to the
		// generic executor rule below it.
		{Match: "Address this feedback", Reply: "fixed it, second pass",
			Tools: []fake.ToolStep{{Name: "write_file", Path: "fixed.marker", Content: "ok"}}},
		{Match: "Pipeline stage: executor", Reply: "first pass, no marker yet"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	src := `name: loop-attempt-test
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
	madsHome := t.TempDir()
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: f, SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second, MadsHome: madsHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v; task: %+v", sum, h.task(t, seeded[0].ID))
	}

	started := h.events(t, "pipeline.started")
	if len(started) != 1 {
		t.Fatalf("pipeline.started: %+v", started)
	}
	runSeq := started[0].Seq

	attempt1, err := os.ReadFile(outputLogPath(madsHome, h.sess.ID, runSeq, seeded[0].ID, "executor", 1))
	if err != nil {
		t.Fatalf("executor attempt-1 log: %v", err)
	}
	if strings.Contains(string(attempt1), "fixed it, second pass") {
		t.Errorf("executor attempt-1 log was appended into by the second run instead of it opening its own attempt-2 log: %q", attempt1)
	}
	if !strings.HasPrefix(string(attempt1), "=== stage=executor attempt=1 ") {
		t.Errorf("executor attempt-1 header: %q", attempt1)
	}

	attempt2, err := os.ReadFile(outputLogPath(madsHome, h.sess.ID, runSeq, seeded[0].ID, "executor", 2))
	if err != nil {
		t.Fatalf("executor attempt-2 log was never created — the loop-back re-run kept appending into attempt-1 instead: %v", err)
	}
	if !strings.HasPrefix(string(attempt2), "=== stage=executor attempt=2 ") {
		t.Errorf("executor attempt-2 header: %q", attempt2)
	}
	if !strings.Contains(string(attempt2), "fixed it, second pass") {
		t.Errorf("executor attempt-2 log missing the second run's output: %q", attempt2)
	}

	type stageAttempt struct {
		Stage   string `json:"stage"`
		Attempt int    `json:"attempt"`
	}
	var executorAttempts []int
	for _, ev := range h.events(t, "usage.recorded") {
		var p stageAttempt
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Stage == "executor" {
			executorAttempts = append(executorAttempts, p.Attempt)
		}
	}
	if want := []int{1, 2}; len(executorAttempts) != len(want) ||
		executorAttempts[0] != want[0] || executorAttempts[1] != want[1] {
		t.Errorf("executor usage.recorded attempts: %v, want %v (climbing across the build loop-back)", executorAttempts, want)
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

// An "auto" stage routes through pkg/agentselect: a registered agent whose
// quadrant matches the task's Classify heuristics is picked over the
// hardcoded "executor", and the decision is recorded as agent.selected.
// "investigate something" classifies to QuadrantInvestigator (agencyScore
// via "investigate" as an exploration word, clarityScore via "something" as
// a clarity-low word) — a quadrant none of the seeded system agents carry,
// so investigator-bot is the only survivor.
func TestAutoSelectsRegisteredAgent(t *testing.T) {
	h := newHarness(t)
	bot := contracts.Agent{Name: "investigator-bot", Prompt: "You investigate things.", Quadrant: contracts.QuadrantInvestigator}
	if err := h.data.CreateAgent(context.Background(), &bot); err != nil {
		t.Fatal(err)
	}
	f, err := fake.NewWithScenario(fake.Scenario{Name: "auto-select", Rules: []fake.Rule{
		{Match: "Pipeline stage: impl", Reply: "done"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	src := "name: solo\nstages:\n  - name: impl\n    agent: auto\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "investigate something"})
	sum := h.run(t, f, tpl, 1)
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v", sum)
	}
	sels := h.events(t, "agent.selected")
	if len(sels) != 1 || !strings.Contains(string(sels[0].Payload), `"agent":"investigator-bot"`) {
		t.Errorf("agent.selected: %+v", sels)
	}
}

// When no candidate survives auto-selection, generation is automatic: a
// draft is generated from the task via pkg/agentgen, registered, and used
// for the stage — no confirmation step ("that's what auto means").
func TestAutoGeneratesAgentWhenNoCandidate(t *testing.T) {
	h := newHarness(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "auto-generate", Rules: []fake.Rule{
		{Match: "Generate an agent definition for this task", Reply: `{"name":"generated-investigator",` +
			`"description":"Generated for this task","prompt":"You investigate things.",` +
			`"quadrant":"investigator","tags":[],` +
			`"capabilities":{"tools":[],"mcp_servers":[],"skills":[]},"permissions":{}}`},
		{Match: "Pipeline stage: impl", Reply: "done"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fm := fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
	src := "name: solo\nstages:\n  - name: impl\n    agent: auto\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "investigate something"})
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: f, SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second, FastModel: fm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v", sum)
	}
	generated, err := h.data.Agent(context.Background(), "generated-investigator")
	if err != nil {
		t.Fatalf("generated agent was never registered: %v", err)
	}
	if generated.Quadrant != contracts.QuadrantInvestigator {
		t.Errorf("generated agent quadrant: %s", generated.Quadrant)
	}
	if sels := h.events(t, "agent.selected"); len(sels) != 1 {
		t.Errorf("agent.selected: %+v", sels)
	}
}

// Generation failure (e.g. the Fast Model can't produce a usable draft)
// blocks the stage with a clear reason; it never silently falls back to a
// hardcoded agent.
func TestAutoGenerationFailureBlocksStage(t *testing.T) {
	h := newHarness(t)
	f, err := fake.NewWithScenario(fake.Scenario{Name: "auto-generate-fail", Rules: []fake.Rule{
		// Valid JSON, but missing the required name/prompt fields: fails
		// generatedAgent.Validate() on every retry attempt (the retry
		// feedback is appended to, not replacing, this same matched prompt).
		{Match: "Generate an agent definition for this task", Reply: `{"description":"missing name and prompt"}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fm := fastmodel.New(f, fastmodel.Config{Model: "fake-fast"})
	src := "name: solo\nstages:\n  - name: impl\n    agent: auto\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "investigate something"})
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: f, SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second, FastModel: fm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
		t.Fatalf("summary: %+v", sum)
	}
	blocked := h.task(t, seeded[0].ID)
	var found bool
	for _, n := range blocked.Notes {
		if n.Type == contracts.NoteBlocker && strings.Contains(n.Content, "generat") {
			found = true
		}
	}
	if !found {
		t.Errorf("blocked task notes lack a generation-failure reason: %+v", blocked.Notes)
	}
}

// A task's RequiredCapabilities and its workspace's Capabilities both feed
// auto-select's hard capability filter (their union, per
// agentselect.EffectiveCapabilities): an agent missing either is eliminated,
// and the logged agent.selected event names the missing capability so a
// user learns what to add to which agent.
func TestAutoSelectPopulatesEffectiveCapabilities(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for _, name := range []string{"ws-cap", "task-cap"} {
		cap := contracts.Capability{Name: name, Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "true"}}
		if err := h.data.CreateCapability(ctx, &cap); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := h.data.Workspace(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	ws.Capabilities = []string{"ws-cap"}
	if err := h.data.UpdateWorkspace(ctx, &ws); err != nil {
		t.Fatal(err)
	}

	// under-declares: has task-cap, lacks the workspace-level ws-cap.
	if err := h.data.CreateAgent(ctx, &contracts.Agent{
		Name: "under-declared", Prompt: "p", Quadrant: contracts.QuadrantInvestigator,
		Capabilities: contracts.AgentCapabilities{MCPServers: []string{"task-cap"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.data.CreateAgent(ctx, &contracts.Agent{
		Name: "fully-declared", Prompt: "p", Quadrant: contracts.QuadrantInvestigator,
		Capabilities: contracts.AgentCapabilities{MCPServers: []string{"ws-cap", "task-cap"}},
	}); err != nil {
		t.Fatal(err)
	}

	f, err := fake.NewWithScenario(fake.Scenario{Name: "auto-select-caps", Rules: []fake.Rule{
		{Match: "Pipeline stage: impl", Reply: "done"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	src := "name: solo\nstages:\n  - name: impl\n    agent: auto\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "investigate something", RequiredCapabilities: []string{"task-cap"}})
	sum := h.run(t, f, tpl, 1)
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v", sum)
	}

	sels := h.events(t, "agent.selected")
	if len(sels) != 1 {
		t.Fatalf("agent.selected: %+v", sels)
	}
	if !strings.Contains(string(sels[0].Payload), `"agent":"fully-declared"`) {
		t.Errorf("expected fully-declared to win, got %s", sels[0].Payload)
	}
	if !strings.Contains(string(sels[0].Payload), `"eliminated":"capability (missing ws-cap)"`) {
		t.Errorf("expected under-declared eliminated naming ws-cap, got %s", sels[0].Payload)
	}
}

// When every registry agent is eliminated by the capability filter and no
// Fast Model is configured to generate a replacement, the blocked task's
// note names the missing capability instead of a bare failure.
func TestAutoSelectNoCandidateNamesMissingCapability(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	cap := contracts.Capability{Name: "playwright", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "true"}}
	if err := h.data.CreateCapability(ctx, &cap); err != nil {
		t.Fatal(err)
	}
	if err := h.data.CreateAgent(ctx, &contracts.Agent{
		Name: "no-playwright", Prompt: "p", Quadrant: contracts.QuadrantInvestigator,
	}); err != nil {
		t.Fatal(err)
	}

	src := "name: solo\nstages:\n  - name: impl\n    agent: auto\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "investigate something", RequiredCapabilities: []string{"playwright"}})
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: tpl, Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Blocked) != 1 || sum.Blocked[0] != seeded[0].ID {
		t.Fatalf("summary: %+v", sum)
	}
	blocked := h.task(t, seeded[0].ID)
	var found bool
	for _, n := range blocked.Notes {
		if n.Type == contracts.NoteBlocker && strings.Contains(n.Content, "missing playwright") {
			found = true
		}
	}
	if !found {
		t.Errorf("blocked task notes lack a missing-capability reason: %+v", blocked.Notes)
	}
}

// Explicit agent names are untouched by any of the auto-select/generation
// machinery: no agent.selected event, and the stage runs the named agent.
func TestExplicitAgentBypassesAutoSelect(t *testing.T) {
	h := newHarness(t)
	src := "name: solo\nstages:\n  - name: executor\n    agent: executor\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "investigate something"})
	sum := h.run(t, fake.New(), tpl, 1)
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v", sum)
	}
	if sels := h.events(t, "agent.selected"); len(sels) != 0 {
		t.Errorf("agent.selected should not fire for an explicit agent: %+v", sels)
	}
}

// An annotated task (non-empty effective capability set) mounts exactly the
// intersection of what it requires and what the stage's agent declares —
// never more than the agent declared — and logs the mount/skip decision.
func TestScopedCapabilitiesMountsIntersection(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for _, c := range []contracts.Capability{
		{Name: "mcp-in", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "true"}},
		{Name: "mcp-out", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "true"}},
		{Name: "skill-in", Type: contracts.CapabilitySkill, Config: contracts.CapabilityConfig{Path: h.cwd}},
		{Name: "skill-out", Type: contracts.CapabilitySkill, Config: contracts.CapabilityConfig{Path: h.cwd}},
	} {
		if err := h.data.CreateCapability(ctx, &c); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.data.CreateAgent(ctx, &contracts.Agent{
		Name: "scoped-agent", Prompt: "p", Quadrant: contracts.QuadrantInvestigator,
		Capabilities: contracts.AgentCapabilities{
			MCPServers: []string{"mcp-in", "mcp-out"},
			Skills:     []string{"skill-in", "skill-out"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	f, err := fake.NewWithScenario(fake.Scenario{Name: "scoped-mount", Rules: []fake.Rule{
		{Match: "Pipeline stage: executor", Reply: "done"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	src := "name: solo\nstages:\n  - name: executor\n    agent: scoped-agent\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "annotated", RequiredCapabilities: []string{"mcp-in", "skill-in"}})
	sum := h.run(t, f, tpl, 1)
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v", sum)
	}

	mounts := h.events(t, "stage.capabilities_mounted")
	if len(mounts) != 1 {
		t.Fatalf("stage.capabilities_mounted: %+v", mounts)
	}
	payload := string(mounts[0].Payload)
	for _, want := range []string{
		`"scoped":true`,
		`"mounted_mcp_servers":["mcp-in"]`,
		`"skipped_mcp_servers":["mcp-out"]`,
		`"mounted_skills":["skill-in"]`,
		`"skipped_skills":["skill-out"]`,
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("expected payload to contain %s, got %s", want, payload)
		}
	}
}

// An unannotated task (empty effective capability set) mounts the stage's
// agent's full declaration unchanged — today's pre-scoping behavior.
func TestUnscopedCapabilitiesMountsFullDeclaration(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	for _, c := range []contracts.Capability{
		{Name: "mcp-a", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "true"}},
		{Name: "skill-a", Type: contracts.CapabilitySkill, Config: contracts.CapabilityConfig{Path: h.cwd}},
	} {
		if err := h.data.CreateCapability(ctx, &c); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.data.CreateAgent(ctx, &contracts.Agent{
		Name: "full-agent", Prompt: "p", Quadrant: contracts.QuadrantInvestigator,
		Capabilities: contracts.AgentCapabilities{
			MCPServers: []string{"mcp-a"},
			Skills:     []string{"skill-a"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	f, err := fake.NewWithScenario(fake.Scenario{Name: "unscoped-mount", Rules: []fake.Rule{
		{Match: "Pipeline stage: executor", Reply: "done"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	src := "name: solo\nstages:\n  - name: executor\n    agent: full-agent\n"
	tpl, verr := template.Check([]byte(src), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	seeded := h.seed(contracts.Task{Title: "unannotated"})
	sum := h.run(t, f, tpl, 1)
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v", sum)
	}

	mounts := h.events(t, "stage.capabilities_mounted")
	if len(mounts) != 1 {
		t.Fatalf("stage.capabilities_mounted: %+v", mounts)
	}
	payload := string(mounts[0].Payload)
	for _, want := range []string{
		`"scoped":false`,
		`"mounted_mcp_servers":["mcp-a"]`,
		`"mounted_skills":["skill-a"]`,
		`"skipped_mcp_servers":null`,
		`"skipped_skills":null`,
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("expected payload to contain %s, got %s", want, payload)
		}
	}
}

// TestPipelineStartedCarriesTemplateVersion covers W1.5's audit trail: when
// the caller (pipelinerun.LoadTemplate, via its own fetch of the
// contracts.Template registry record) supplies a real version, it rides the
// pipeline.started payload verbatim — this is the one place a historical run
// gets attributed to the exact template revision it executed.
func TestPipelineStartedCarriesTemplateVersion(t *testing.T) {
	h := newHarness(t)
	seeded := h.seed(contracts.Task{Title: "versioned run"})
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: solitaryTemplate(t, ""), Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second, TemplateVersion: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 || sum.Done[0] != seeded[0].ID {
		t.Fatalf("summary: %+v", sum)
	}
	started := h.events(t, "pipeline.started")
	if len(started) != 1 || !strings.Contains(string(started[0].Payload), `"template_version":7`) {
		t.Fatalf("pipeline.started: %+v", started)
	}
}

// TestPipelineStartedOmitsZeroTemplateVersion covers the zero-safety half:
// an unknown version (pre-versioning row, or a caller that bypassed
// pipelinerun.LoadTemplate) must never be reported as the literal version 0
// — it's omitted from the payload entirely, same convention as other
// optional event fields.
func TestPipelineStartedOmitsZeroTemplateVersion(t *testing.T) {
	h := newHarness(t)
	h.seed(contracts.Task{Title: "unversioned run"})
	_, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: solitaryTemplate(t, ""), Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := h.events(t, "pipeline.started")
	if len(started) != 1 || strings.Contains(string(started[0].Payload), "template_version") {
		t.Fatalf("pipeline.started should omit template_version when zero: %+v", started)
	}
}

// TestPipelineEventsCarryRunID: the run's id rides pipeline.started and
// pipeline.completed, so the run history pairs them by id — the Router's
// cancel/failed already carry it — rather than by order, which misfiled a
// cancel when two engines overlapped on one session.
func TestPipelineEventsCarryRunID(t *testing.T) {
	h := newHarness(t)
	h.seed(contracts.Task{Title: "identified run"})
	if _, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID, RunID: "run_abc123",
		Template: solitaryTemplate(t, ""), Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"pipeline.started", "pipeline.completed"} {
		evs := h.events(t, typ)
		if len(evs) != 1 || !strings.Contains(string(evs[0].Payload), `"run_id":"run_abc123"`) {
			t.Errorf("%s: %+v, want run_id run_abc123", typ, evs)
		}
	}
}

// TestExcludedTasksLeftOut: an excluded task is never launched and keeps its
// status; whatever depends on it, directly or transitively, reports skipped
// like any unsatisfiable dependency, and pipeline.started names the excluded
// ids alongside the run id.
func TestExcludedTasksLeftOut(t *testing.T) {
	h := newHarness(t)
	seeded := h.seed(
		contracts.Task{Title: "left out"},
		contracts.Task{Title: "needs left out"},
		contracts.Task{Title: "needs needs left out"},
		contracts.Task{Title: "independent"},
	)
	for i, dep := range map[int]string{1: seeded[0].ID, 2: seeded[1].ID} {
		task := seeded[i]
		task.Dependencies = []string{dep}
		if err := h.data.UpdateTask(context.Background(), &task); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID, RunID: "run_ex",
		Template: solitaryTemplate(t, ""), Cwd: h.cwd, Concurrency: 2,
		StageTimeout: 30 * time.Second, ExcludeTaskIDs: []string{seeded[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Done) != 1 || sum.Done[0] != seeded[3].ID {
		t.Errorf("done: %v, want just %s (independent)", sum.Done, seeded[3].ID)
	}
	skipped := map[string]bool{}
	for _, id := range sum.Skipped {
		skipped[id] = true
	}
	if len(sum.Skipped) != 2 || !skipped[seeded[1].ID] || !skipped[seeded[2].ID] {
		t.Errorf("skipped: %v, want the two dependents %s, %s", sum.Skipped, seeded[1].ID, seeded[2].ID)
	}
	if got := h.task(t, seeded[0].ID).Status; got != contracts.StatusReady {
		t.Errorf("excluded task status = %s, want ready (untouched)", got)
	}
	for _, ev := range h.events(t, "stage.started") {
		if ev.TaskID == seeded[0].ID {
			t.Errorf("excluded task launched: %+v", ev)
		}
	}
	started := h.events(t, "pipeline.started")
	want := `"excluded_task_ids":["` + seeded[0].ID + `"]`
	if len(started) != 1 || !strings.Contains(string(started[0].Payload), want) ||
		!strings.Contains(string(started[0].Payload), `"run_id":"run_ex"`) {
		t.Fatalf("pipeline.started: %+v, want %s and run_id", started, want)
	}
}

// TestPipelineStartedOmitsExcludedWhenNone: a run with nothing left out
// carries no excluded_task_ids key at all.
func TestPipelineStartedOmitsExcludedWhenNone(t *testing.T) {
	h := newHarness(t)
	h.seed(contracts.Task{Title: "plain run"})
	if _, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: solitaryTemplate(t, ""), Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	started := h.events(t, "pipeline.started")
	if len(started) != 1 || strings.Contains(string(started[0].Payload), "excluded_task_ids") {
		t.Fatalf("pipeline.started should omit excluded_task_ids when none: %+v", started)
	}
}
