// Package engine is the pipeline stage machine (spec §6): it takes a
// session's ready tasks through a frozen template's stages — agent stages
// through the provider layer, command stages through the shell — with
// when-guards, on_fail loop-backs, and dependency-ordered scheduling under a
// concurrency limit. All state lives in the Data Service; every transition
// is an event; every running task is held under a lease (spec §15).
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/inhibit"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/provider"
)

// FailMarker is the convention by which an agent stage fails: the reply's
// last non-empty line. Gate-style agents (code review) are prompted to end
// with "STAGE: pass" or "STAGE: fail — <reason>".
const FailMarker = "STAGE: fail"

// Config wires one run.
type Config struct {
	Data     *dataclient.Client
	Provider provider.Provider
	// SessionID scopes the run: its confirmed ready tasks are the work.
	SessionID string
	// Template is the frozen copy (spec §6: saves never disturb in-flight
	// runs) — parse+validate before calling Run; Run re-checks structure.
	Template *template.Template
	// Cwd is where command stages run and agent one-shots execute. W3.2
	// replaces this with per-task worktrees.
	Cwd string
	// Concurrency caps simultaneously-running tasks (default 2).
	Concurrency int
	// Actor is the run's lease identity (e.g. "pipeline"). Default "pipeline".
	Actor string
	// Model overrides agent model preferences when set.
	Model string
	// StageTimeout bounds each stage (default 10m).
	StageTimeout time.Duration
	// LeaseTTL is the task lease meter (default 10m). A heartbeat renews it
	// at a third of the TTL while the task runs: agent stages can be silent
	// far longer than any TTL, so writes must never be the only renewal.
	LeaseTTL time.Duration
	// RateLimitBackoff is how long the engine pauses before retrying a stage
	// attempt that hit a provider rate limit (spec §11.9: inform and wait).
	// Default 5m — these are rolling windows measured in minutes to hours,
	// so the default is deliberately coarse. Tests override with sub-second
	// values.
	RateLimitBackoff time.Duration
	// RateLimitMaxRetries caps retries after a rate limit before the task is
	// blocked with a note naming the limit, in case the provider never
	// recovers. Default 12 (~1h of pauses at the default backoff).
	RateLimitMaxRetries int

	// DisableSleepInhibit turns off the OS sleep/idle inhibitor normally
	// held for the run's duration (logind + ScreenSaver where available —
	// COSMIC's idle-suspend only honors the latter). Default false
	// (inhibitor enabled). Acquiring it never fails or delays the run: a
	// missing/refused D-Bus service just logs a warning (pkg/inhibit).
	DisableSleepInhibit bool
	// SleepInhibitor is the inhibitor implementation; nil defaults to the
	// real D-Bus-backed inhibit.Locker. Tests inject a fake.
	SleepInhibitor inhibit.Locker

	// Isolated turns on worktree-per-task (spec §6, §17): stages before the
	// merge run in the task's own worktree; the merge stage brings the task
	// branch into Cwd (the primary repo) serialized in dependency order;
	// stages after it (verify) run on the merged tree. Requires WorktreeRoot.
	Isolated bool
	// WorktreeRoot is where task worktrees are created (outside the repo).
	WorktreeRoot string
	// DataURL, when set with Isolated, is stamped into each worktree's git
	// config so the installed post-commit hook can report commits with task
	// context (spec §5).
	DataURL string
}

// Summary is the run's outcome.
type Summary struct {
	Done    []string // task IDs that completed every stage
	Blocked []string // task IDs blocked by stage failure
	Skipped []string // task IDs never eligible (unsatisfiable dependencies)
}

// Run executes the pipeline to quiescence: no runnable tasks remain. It
// returns an error only for launch-check failures and infrastructure
// breakage — task-level failure is a Summary outcome, not an error.
func Run(ctx context.Context, cfg Config) (*Summary, error) {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 2
	}
	if cfg.Actor == "" {
		cfg.Actor = "pipeline"
	}
	if cfg.StageTimeout <= 0 {
		cfg.StageTimeout = 10 * time.Minute
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = 10 * time.Minute
	}
	if cfg.RateLimitBackoff <= 0 {
		cfg.RateLimitBackoff = 5 * time.Minute
	}
	if cfg.RateLimitMaxRetries <= 0 {
		cfg.RateLimitMaxRetries = 12
	}

	lock := inhibit.Handle(inhibit.HandleFunc(func() {}))
	if !cfg.DisableSleepInhibit {
		locker := cfg.SleepInhibitor
		if locker == nil {
			locker = inhibit.NewLocker()
		}
		lock = locker.Acquire(fmt.Sprintf("pipeline run %s", cfg.SessionID))
	}
	defer lock.Release()

	// Launch checks — the same ones the template editor runs (M9).
	if cfg.Template == nil {
		return nil, errors.New("no template")
	}
	agents, err := resolveAgents(ctx, cfg)
	if err != nil {
		return nil, err
	}
	conds, err := parseConds(cfg.Template)
	if err != nil {
		return nil, err
	}
	cycles, err := cfg.Data.Cycles(ctx, cfg.SessionID)
	if err != nil {
		return nil, err
	}
	if len(cycles) > 0 {
		return nil, fmt.Errorf("dependency cycles block launch (spec §3.6): %v", cycles)
	}

	e := &engine{cfg: cfg, agents: agents, conds: conds, loops: map[string]int{}}
	if cfg.Isolated {
		if cfg.WorktreeRoot == "" {
			return nil, errors.New("isolated runs require WorktreeRoot")
		}
		e.wt = &worktrees{
			primary: cfg.Cwd, root: cfg.WorktreeRoot,
			dataURL: cfg.DataURL, sessionID: cfg.SessionID,
		}
	}
	return e.run(ctx)
}

// resolveAgents freezes registry lookups at launch: every agent named by the
// template, plus the executor for agent:auto stages.
func resolveAgents(ctx context.Context, cfg Config) (map[string]contracts.Agent, error) {
	names := map[string]bool{}
	for _, st := range cfg.Template.Stages {
		if st.Agent != "" {
			name := st.Agent
			if name == "auto" {
				name = "executor" // minimal routing; quadrant routing is a later item (§9)
			}
			names[name] = true
		}
	}
	agents := map[string]contracts.Agent{}
	for name := range names {
		a, err := cfg.Data.Agent(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("launch check: agent %q: %w", name, err)
		}
		agents[name] = a
	}
	return agents, nil
}

func parseConds(t *template.Template) ([]*template.Cond, error) {
	conds := make([]*template.Cond, len(t.Stages))
	for i, st := range t.Stages {
		c, err := template.ParseCond(st.When)
		if err != nil {
			return nil, fmt.Errorf("launch check: stage %q: %w", st.Name, err)
		}
		conds[i] = c
	}
	return conds, nil
}

type engine struct {
	cfg    Config
	agents map[string]contracts.Agent
	conds  []*template.Cond
	wt     *worktrees // nil unless Isolated

	mu    sync.Mutex
	loops map[string]int // task|stage → loop-backs taken
}

func (e *engine) run(ctx context.Context) (*Summary, error) {
	e.event(ctx, contracts.PipelineStarted, "", map[string]any{
		"template":    e.cfg.Template.Name,
		"concurrency": e.cfg.Concurrency,
	})

	// The live work queue (spec §6): task.* events for this session poke the
	// scheduler so mid-run inserts are picked up promptly. The stream is an
	// optimization — completion-driven rescans and the poll fallback keep
	// the run correct without it.
	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()
	taskEvents, streamErr := e.cfg.Data.Stream(streamCtx, 0, e.cfg.SessionID, []string{"task.*"})
	if streamErr != nil {
		taskEvents = nil
	}

	summary := &Summary{}
	type result struct {
		id     string
		status contracts.TaskStatus
	}
	results := make(chan result)
	running := map[string]bool{}
	excluded := map[string]bool{} // attempted but unclaimable (leased elsewhere): not ours this run

	// Continuous scheduler: fill free slots from the ready set, then wait
	// for one completion and re-scan — a finishing task can unblock its
	// dependents immediately, and the concurrency cap is never idle-wasted
	// on a batch barrier.
	for {
		// Once cancelled, stop polling for new work — a request against an
		// already-cancelled ctx would just fail and retry in a tight loop,
		// starving the select below that drains in-flight results — and fall
		// straight through to draining.
		var tasks []contracts.Task
		if ctx.Err() == nil {
			var err error
			tasks, err = e.cfg.Data.Tasks(ctx, e.cfg.SessionID, "")
			if err != nil {
				if len(running) > 0 {
					continue // drain in-flight tasks before giving up
				}
				return nil, err
			}
		}
		byID := map[string]contracts.Task{}
		for _, t := range tasks {
			byID[t.ID] = t
		}
		// Mid-run inserts can introduce cycles the launch check never saw:
		// re-check and reject the participants loudly (spec §6, §3.6).
		if cycles, err := e.cfg.Data.Cycles(ctx, e.cfg.SessionID); err == nil {
			for _, cycle := range cycles {
				for _, id := range cycle {
					t, known := byID[id]
					if !known || excluded[id] || running[id] || t.Status != contracts.StatusReady {
						continue
					}
					e.block(ctx, &t, "scheduler", fmt.Sprintf(
						"rejected from the live queue: its dependencies introduce a cycle (%s)",
						strings.Join(cycle, " → ")))
					excluded[id] = true
					summary.Blocked = append(summary.Blocked, id)
				}
			}
		}
		if ctx.Err() == nil { // when cancelled, stop launching but keep draining
			for _, t := range e.runnable(tasks, byID) {
				if running[t.ID] || excluded[t.ID] || len(running) >= e.cfg.Concurrency {
					continue
				}
				running[t.ID] = true
				go func() { results <- result{t.ID, e.runTask(ctx, t)} }()
			}
		}
		if len(running) == 0 {
			// Quiescent: nothing running, nothing can start. Anything
			// still ready has unsatisfiable dependencies this run.
			for _, t := range tasks {
				if t.Status == contracts.StatusReady && !t.Draft && !excluded[t.ID] {
					summary.Skipped = append(summary.Skipped, t.ID)
				}
			}
			break
		}
		// Wake on a completion, a task event (mid-run insert), or the poll
		// fallback — whichever comes first.
		select {
		case r := <-results:
			delete(running, r.id)
			switch r.status {
			case contracts.StatusDone:
				summary.Done = append(summary.Done, r.id)
			case contracts.StatusBlocked:
				summary.Blocked = append(summary.Blocked, r.id)
			default:
				excluded[r.id] = true
				summary.Skipped = append(summary.Skipped, r.id)
			}
		case _, ok := <-taskEvents:
			if !ok {
				taskEvents = nil // stream dropped; rescans carry on
			}
		case <-time.After(2 * time.Second):
		}
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}

	outcome := "completed"
	if len(summary.Blocked) > 0 || len(summary.Skipped) > 0 {
		outcome = "incomplete"
	}
	e.event(ctx, contracts.PipelineCompleted, "", map[string]any{
		"outcome": outcome,
		"done":    len(summary.Done),
		"blocked": len(summary.Blocked),
		"skipped": len(summary.Skipped),
	})
	return summary, nil
}

// runnable selects confirmed ready tasks whose dependencies are all done.
func (e *engine) runnable(tasks []contracts.Task, byID map[string]contracts.Task) []contracts.Task {
	var out []contracts.Task
	for _, t := range tasks {
		if t.Status != contracts.StatusReady || t.Draft {
			continue
		}
		ok := true
		for _, dep := range t.Dependencies {
			if d, known := byID[dep]; !known || d.Status != contracts.StatusDone {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, t)
		}
	}
	return out
}

// runTask drives one task through the stage machine and returns its terminal
// status. The task is leased for the duration; every mutation renews the
// meter.
func (e *engine) runTask(ctx context.Context, task contracts.Task) contracts.TaskStatus {
	ttl := e.cfg.LeaseTTL
	if err := e.cfg.Data.Claim(ctx, task.ID, ttl); err != nil {
		return task.Status // someone else holds it; not ours this run
	}
	// Writes renew the lease meter, but an agent stage can be silent for
	// longer than any TTL (dogfooding lost a lease to the reaper inside a
	// ten-minute one-shot). Heartbeat at a third of the TTL; a renewal
	// refused as a conflict means the lease is gone and the task is no
	// longer ours — cancel its work and stop writing.
	ctx, cancelTask := context.WithCancel(ctx)
	defer cancelTask()
	var lost, terminal atomic.Bool
	taskID := task.ID // the goroutine must not touch task: UpdateTask rewrites it
	go func() {
		tick := time.NewTicker(ttl / 3)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				err := e.cfg.Data.Claim(ctx, taskID, ttl)
				if errors.Is(err, dataclient.ErrConflict) || errors.Is(err, dataclient.ErrNotFound) {
					lost.Store(true)
					cancelTask()
					return
				}
				if err != nil {
					continue // transient; the meter has slack for a retry
				}
				// A machine suspend stalls every heartbeat past the TTL:
				// on wake the reaper has blocked the task, and the claim
				// above silently re-took the freed lease. The engine is
				// alive and still owns the work — put the status back.
				// terminal guards the engine's own block/done writes.
				if cur, err := e.cfg.Data.Task(ctx, taskID); err == nil &&
					cur.Status == contracts.StatusBlocked && !terminal.Load() {
					cur.Status = contracts.StatusInProgress
					_ = e.cfg.Data.UpdateTask(ctx, &cur)
				}
			}
		}
	}()
	defer func() {
		if !lost.Load() {
			_ = e.cfg.Data.Release(context.WithoutCancel(ctx), task.ID)
		}
	}()

	if !e.setStatus(ctx, &task, contracts.StatusInProgress) {
		return task.Status
	}

	// Where stages run: the primary repo, or (isolated) the task's own
	// worktree until the merge stage moves work back to the primary.
	run := taskRun{dir: e.cfg.Cwd}
	if e.wt != nil {
		dir, branch, err := e.wt.create(ctx, task)
		if err != nil {
			terminal.Store(true)
			e.block(ctx, &task, "worktree", err.Error())
			return contracts.StatusBlocked
		}
		run.dir, run.worktree, run.branch = dir, dir, branch
	}
	mergeHeld := false
	defer func() {
		if mergeHeld {
			e.wt.mergeMu.Unlock()
		}
	}()
	// A blocked task keeps its worktree for inspection; say where it is.
	blockReason := func(reason string) string {
		if run.worktree != "" && !run.merged {
			return fmt.Sprintf("%s\n\nThe task's worktree is kept at %s (branch %s).", reason, run.worktree, run.branch)
		}
		return reason
	}

	stages := e.cfg.Template.Stages
	var feedback string // most recent failure, fed to the next agent attempt
	for i := 0; i < len(stages); {
		st := stages[i]
		if ctx.Err() != nil {
			if lost.Load() {
				return task.Status // lease gone; the task is not ours to write
			}
			terminal.Store(true)
			e.block(ctx, &task, st.Name, blockReason("run cancelled"))
			return contracts.StatusBlocked
		}
		if !e.conds[i].Eval(task.Labels) {
			e.event(ctx, contracts.StageSkipped, task.ID, map[string]any{
				"stage": st.Name, "when": st.When,
			})
			i++
			continue
		}
		e.event(ctx, contracts.StageStarted, task.ID, map[string]any{"stage": st.Name})

		var output string
		var err error
		if e.wt != nil && isMergeStage(st) {
			// One merge at a time, and the lock is held through the
			// remaining stages so verify sees exactly this merge.
			if !mergeHeld {
				e.wt.mergeMu.Lock()
				mergeHeld = true
			}
			err = e.wt.commitStray(ctx, run.dir, task)
			if err == nil {
				err = e.wt.merge(ctx, st.Command, task, run.branch)
			}
			if err == nil {
				run.dir = e.cfg.Cwd // post-merge stages run on the merged tree
				run.merged = true
			}
			output = ""
			if err != nil {
				output = err.Error()
			}
		} else {
			var blocked bool
			output, err, blocked = e.runStageRateLimited(ctx, st, &task, feedback, run.dir, &lost, &terminal, blockReason)
			if blocked {
				return contracts.StatusBlocked
			}
		}
		if lost.Load() {
			return task.Status // lease gone mid-stage; whoever holds it now owns the outcome
		}
		if err == nil {
			e.event(ctx, contracts.StageCompleted, task.ID, map[string]any{"stage": st.Name})
			i++
			continue
		}
		feedback = err.Error()
		e.event(ctx, contracts.StageFailed, task.ID, map[string]any{
			"stage": st.Name, "reason": trim(feedback, 2000),
		})

		if of := st.OnFail; of != nil && of.LoopBack != "" {
			key := task.ID + "|" + st.Name
			e.mu.Lock()
			e.loops[key]++
			taken := e.loops[key]
			e.mu.Unlock()
			if taken <= of.MaxLoops {
				e.note(ctx, task.ID, contracts.NoteObservation, fmt.Sprintf(
					"stage %s failed (loop-back %d/%d to %s): %s",
					st.Name, taken, of.MaxLoops, of.LoopBack, trim(output, 2000)))
				i = stageIndex(stages, of.LoopBack)
				continue
			}
		}
		terminal.Store(true)
		e.block(ctx, &task, st.Name, blockReason(trim(output, 2000)))
		return contracts.StatusBlocked
	}

	if lost.Load() {
		return task.Status
	}
	if e.wt != nil && run.merged {
		e.wt.remove(context.WithoutCancel(ctx), run.worktree, run.branch)
	}
	terminal.Store(true)
	if !e.setStatus(ctx, &task, contracts.StatusDone) {
		return contracts.StatusBlocked
	}
	return contracts.StatusDone
}

// runStageRateLimited runs st, pausing and retrying the SAME attempt (same
// feedback, no loop-back consumed) each time the provider reports a rolling
// rate limit — the engine waits it out instead of treating it like a stage
// failure (spec §11.9). blocked reports whether it already parked the task
// at the retry cap; callers must return StatusBlocked without further
// handling in that case.
func (e *engine) runStageRateLimited(ctx context.Context, st template.Stage, task *contracts.Task, feedback, dir string, lost *atomic.Bool, terminal *atomic.Bool, blockReason func(string) string) (output string, err error, blocked bool) {
	var attempt int
	for {
		output, err = e.runStage(ctx, st, *task, feedback, dir)
		if lost.Load() {
			return output, err, false
		}
		var rle *provider.RateLimitError
		if err == nil || !errors.As(err, &rle) {
			return output, err, false
		}
		attempt++
		if attempt > e.cfg.RateLimitMaxRetries {
			terminal.Store(true)
			e.block(ctx, task, st.Name, blockReason(fmt.Sprintf(
				"rate limited by the provider (%s) after %d attempt(s), giving up",
				rle.Message, attempt)))
			return output, err, true
		}
		e.event(ctx, contracts.StageRateLimited, task.ID, map[string]any{
			"stage": st.Name, "attempt": attempt, "message": rle.Message,
			"backoff": e.cfg.RateLimitBackoff.String(),
		})
		select {
		case <-time.After(e.cfg.RateLimitBackoff):
		case <-ctx.Done():
		}
		e.event(ctx, contracts.StageRateLimitCleared, task.ID, map[string]any{
			"stage": st.Name, "attempt": attempt,
		})
	}
}

// taskRun is one task's execution context.
type taskRun struct {
	dir      string // where the current stage runs
	worktree string // the task's worktree (isolated runs)
	branch   string
	merged   bool
}

// isMergeStage recognizes the template's merge directive; the engine
// supplies the branch and serialization (spec §6).
func isMergeStage(st template.Stage) bool {
	return strings.HasPrefix(strings.TrimSpace(st.Command), "git merge")
}

// runStage executes one stage in dir; a nil error is a pass. output carries
// the failure detail for feedback notes.
func (e *engine) runStage(ctx context.Context, st template.Stage, task contracts.Task, feedback, dir string) (output string, err error) {
	sctx, cancel := context.WithTimeout(ctx, e.cfg.StageTimeout)
	defer cancel()
	if st.Command != "" {
		return e.runCommand(sctx, st.Command, dir)
	}
	return e.runAgent(sctx, st, task, feedback, dir)
}

func (e *engine) runCommand(ctx context.Context, command, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	raw, err := cmd.CombinedOutput()
	out := string(raw)
	if err != nil {
		return out, fmt.Errorf("command %q failed: %v\n%s", command, err, trim(out, 2000))
	}
	return out, nil
}

func (e *engine) runAgent(ctx context.Context, st template.Stage, task contracts.Task, feedback, dir string) (string, error) {
	name := st.Agent
	if name == "auto" {
		name = "executor"
	}
	agent := e.agents[name]
	model := e.cfg.Model
	if model == "" {
		model = agent.Preferences.Model
	}
	res, err := e.cfg.Provider.OneShot(ctx, provider.OneShotRequest{
		Prompt: taskPrompt(st.Name, task, feedback),
		System: agent.Prompt,
		Model:  model,
		Cwd:    dir,
	})
	if err != nil {
		return "", fmt.Errorf("agent %s: %w", name, err)
	}
	if line := lastLine(res.Text); strings.HasPrefix(strings.ToUpper(line), strings.ToUpper(FailMarker)) {
		return res.Text, fmt.Errorf("agent %s failed the stage: %s", name, line)
	}
	return res.Text, nil
}

// taskPrompt is the agent stage's working brief: the task plus loop-back
// feedback when a downstream stage sent work back.
func taskPrompt(stage string, task contracts.Task, feedback string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pipeline stage: %s\nTask: %s\n", stage, task.Title)
	if task.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", task.Description)
	}
	if task.AcceptanceCriteria != "" {
		fmt.Fprintf(&b, "\nAcceptance criteria:\n%s\n", task.AcceptanceCriteria)
	}
	if feedback != "" {
		fmt.Fprintf(&b, "\nA previous attempt failed downstream. Address this feedback:\n%s\n", feedback)
	}
	b.WriteString("\nWhen you are done, end your reply with a final line reporting the stage outcome: 'STAGE: pass', or 'STAGE: fail — <reason>' if the work under review does not meet the bar. No marker counts as a pass.\n")
	return b.String()
}

func (e *engine) setStatus(ctx context.Context, task *contracts.Task, status contracts.TaskStatus) bool {
	task.Status = status
	return e.cfg.Data.UpdateTask(ctx, task) == nil
}

// block parks the task loudly: status blocked plus a blocker note saying
// which stage and why (feedback accumulates on the task, MADS-UX drill-in).
func (e *engine) block(ctx context.Context, task *contracts.Task, stage, reason string) {
	ctx = context.WithoutCancel(ctx)
	e.note(ctx, task.ID, contracts.NoteBlocker, fmt.Sprintf("stage %s: %s", stage, reason))
	e.setStatus(ctx, task, contracts.StatusBlocked)
}

func (e *engine) note(ctx context.Context, taskID string, kind contracts.NoteType, content string) {
	_ = e.cfg.Data.AddNote(ctx, taskID, &contracts.Note{
		Type: kind, Content: content, Source: e.cfg.Actor, Status: contracts.NoteStatusConfirmed,
	})
}

func (e *engine) event(ctx context.Context, t contracts.EventType, taskID string, payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{}`)
	}
	_ = e.cfg.Data.Append(context.WithoutCancel(ctx), &contracts.Envelope{
		Type: t, SessionID: e.cfg.SessionID, TaskID: taskID, Payload: raw,
	})
}

func stageIndex(stages []template.Stage, name string) int {
	for i, st := range stages {
		if st.Name == name {
			return i
		}
	}
	return 0 // unreachable: pkg/template validates loop targets exist
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
