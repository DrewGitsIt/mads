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
	"log"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/inhibit"
	"github.com/drewkidwell/mads/pkg/procid"
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
	// MadsHome, when set, captures each agent stage attempt's streamed
	// output to $MadsHome/runs/<session_id>/<run_seq>/<task_id>/
	// <stage>-attempt-<n>.log — the Router serves these to the Pipeline
	// tab's drill-in. Empty (the default) is a no-op: no log capture.
	MadsHome string

	// MergirafPath is the path to the mergiraf binary (mergiraf.org),
	// registered as the land queue's git merge driver so adjacent-edit
	// conflicts between parallel task branches resolve automatically while
	// genuinely overlapping edits still conflict for attribution. Empty
	// (the default) skips detection: Run falls back to a PATH lookup for
	// "mergiraf"; if that also fails, land attempts use plain git merge.
	// Pinned version for this machine: mergiraf 0.18.0, installed via
	// `cargo install --locked mergiraf` to ~/.cargo/bin/mergiraf.
	MergirafPath string
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
	if own, ok := procid.Current(procid.Default); ok {
		e.identity = own
	}
	if cfg.Isolated {
		if cfg.WorktreeRoot == "" {
			return nil, errors.New("isolated runs require WorktreeRoot")
		}
		mergirafPath := cfg.MergirafPath
		if mergirafPath == "" {
			if found, err := exec.LookPath("mergiraf"); err == nil {
				mergirafPath = found
			}
		} else if _, err := exec.LookPath(mergirafPath); err != nil {
			// A configured-but-unusable path must fall back cleanly, same
			// as no configuration at all — not surface as a broken driver.
			mergirafPath = ""
		}
		if mergirafPath != "" {
			log.Printf("engine: land queue merge driver: mergiraf (%s)", mergirafPath)
		} else {
			log.Printf("engine: land queue merge driver: git (mergiraf not found)")
		}
		e.wt = &worktrees{
			primary: cfg.Cwd, root: cfg.WorktreeRoot,
			dataURL: cfg.DataURL, sessionID: cfg.SessionID,
			data: cfg.Data, mergirafPath: mergirafPath,
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
	cfg      Config
	agents   map[string]contracts.Agent
	conds    []*template.Cond
	wt       *worktrees              // nil unless Isolated
	identity contracts.LeaseIdentity // this engine's own process identity (reap-on-launch); zero value if unavailable

	mu     sync.Mutex
	loops  map[string]int // task|stage → loop-backs taken
	runSeq int64          // the pipeline.started event's seq; written once before any task goroutine starts
}

func (e *engine) run(ctx context.Context) (*Summary, error) {
	e.reapDeadLeases(ctx)
	e.runSeq = e.event(ctx, contracts.PipelineStarted, "", map[string]any{
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
	if err := e.cfg.Data.ClaimWithIdentity(ctx, task.ID, ttl, e.identity); err != nil {
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
				err := e.cfg.Data.ClaimWithIdentity(ctx, taskID, ttl, e.identity)
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
	// A blocked task keeps its worktree for inspection; say where it is.
	blockReason := func(reason string) string {
		if run.worktree != "" && !run.merged {
			return fmt.Sprintf("%s\n\nThe task's worktree is kept at %s (branch %s).", reason, run.worktree, run.branch)
		}
		return reason
	}

	stages := e.cfg.Template.Stages
	var feedback string // most recent failure, fed to the next agent attempt
	var loopedInto bool // true for exactly the stage index just jumped to via loop_back
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
		onLoop := loopedInto
		loopedInto = false
		if !e.conds[i].Eval(task.Labels, onLoop) {
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
			// Exactly one land attempt (rebase -> gate -> merge) at a
			// time, stable FIFO order (spec: "Not Rocket Science Rule").
			release := e.wt.land.acquire()
			err = e.landAttempt(ctx, st, stages, &run, task)
			release()
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
			// The gate stage's command already ran (as the land attempt's
			// pre-land gate, landAttempt); don't run it again as a normal
			// stage.
			if e.wt != nil && isMergeStage(st) {
				if gidx, ok := gateStage(stages); ok && gidx >= i {
					i = gidx + 1
				}
			}
			continue
		}
		feedback = err.Error()

		// A post-rebase gate ("verify") failure is an ordinary on_fail-
		// capable stage failure — only WHEN it runs moved into the land
		// attempt (spec: "the gate always runs post-rebase"), not what
		// happens on failure. Route it through the gate stage's own
		// on_fail, not the merge stage's, and attribute the failed/blocked
		// event to the gate by name — landAttempt already emitted the
		// gate's own stage.failed, so don't double-report it under the
		// merge stage's name here.
		failedStage := st
		var gf *gateFailure
		if errors.As(err, &gf) {
			if gidx, ok := gateStage(stages); ok {
				failedStage = stages[gidx]
			}
		} else {
			e.event(ctx, contracts.StageFailed, task.ID, map[string]any{
				"stage": st.Name, "reason": trim(feedback, 2000),
			})
		}

		if of := failedStage.OnFail; of != nil && of.LoopBack != "" {
			key := task.ID + "|" + failedStage.Name
			e.mu.Lock()
			e.loops[key]++
			taken := e.loops[key]
			e.mu.Unlock()
			if taken <= of.MaxLoops {
				e.note(ctx, task.ID, contracts.NoteObservation, fmt.Sprintf(
					"stage %s failed (loop-back %d/%d to %s): %s",
					failedStage.Name, taken, of.MaxLoops, of.LoopBack, trim(output, 2000)))
				i = stageIndex(stages, of.LoopBack)
				loopedInto = true
				continue
			}
		}
		terminal.Store(true)
		e.block(ctx, &task, failedStage.Name, blockReason(trim(output, 2000)))
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

// buildStageCommand finds a stage literally named "build" with a non-empty
// command, by convention — symmetric with how isMergeStage recognizes the
// merge stage by its command prefix rather than its name. Used as the
// post-rebase sanity gate; absent, the gate is simply skipped.
func buildStageCommand(stages []template.Stage) (string, bool) {
	for _, st := range stages {
		if st.Name == "build" && st.Command != "" {
			return st.Command, true
		}
	}
	return "", false
}

// gateStage finds the stage literally named "verify" with a non-empty
// command, by the same naming convention as buildStageCommand. Its command
// is the land queue's gate: run post-rebase, immediately before every land
// attempt's merge (spec: "the gate always runs post-rebase; a task's
// earlier green in its own worktree does not count for landing") —
// landAttempt runs it directly instead of the normal stage loop, which
// skips over its index once the land attempt that consumed it succeeds.
// Only WHERE it runs moved into the land attempt: its own `when:` guard
// still governs WHETHER it runs at all (landAttempt evaluates e.conds[gidx]
// exactly as the normal stage loop would, so the template editor's "when"
// field on a verify stage means the same thing it says), and its own
// on_fail (loop_back included) still governs what happens when its command
// fails — see gateFailure and its handling in runTask.
func gateStage(stages []template.Stage) (idx int, ok bool) {
	for i, st := range stages {
		if st.Name == "verify" && st.Command != "" {
			return i, true
		}
	}
	return 0, false
}

// gateFailure marks a post-rebase gate ("verify") command failure, distinct
// from a rebase/merge conflict, so runTask's on_fail routing (above) can
// attribute it — and any loop_back — to the gate stage itself rather than
// the merge stage whose land attempt ran it.
type gateFailure struct{ err error }

func (g *gateFailure) Error() string { return g.err.Error() }
func (g *gateFailure) Unwrap() error { return g.err }

// landAttempt runs one FIFO-serialized land attempt for the merge stage:
// commit stray work, rebase the branch onto current trunk, run the
// post-rebase sanity build (if a "build" stage exists) when the rebase
// actually moved the branch, sweep any stray HANDOFF.md, gate the rebased
// result (the "verify" stage's command, if any and if its own `when:` guard
// passes — its command always runs post-rebase rather than counting an
// earlier green in the task's own worktree, per spec, but the guard still
// decides whether it runs at all), then merge into the primary. Must be
// called with the land queue held. Emits
// merge.attempted/merge.conflicted/merge.completed naming the task's fork
// SHA and the trunk HEAD this attempt gated and landed against, plus (on
// conflict) structured pairwise attribution — the acceptance criterion a
// future merge_resolve stage and the Merge Queue UI consume.
func (e *engine) landAttempt(ctx context.Context, st template.Stage, stages []template.Stage, run *taskRun, task contracts.Task) error {
	forkSHA := e.wt.fork(task.ID)
	mergeDriver := "git"
	if e.wt.mergirafPath != "" {
		mergeDriver = mergirafDriverName
	}
	e.event(ctx, contracts.MergeAttempted, task.ID, map[string]any{
		"branch": run.branch, "target_branch": "trunk", "fork_sha": forkSHA,
		"merge_driver": mergeDriver,
	})

	err := e.wt.commitStray(ctx, run.dir, task)
	var trunkHead string
	var rebased bool
	if err == nil {
		rebased, trunkHead, err = e.wt.rebaseBeforeMerge(ctx, run.dir, task)
	}
	if err == nil && rebased {
		if buildCmd, ok := buildStageCommand(stages); ok {
			if _, berr := e.runCommand(ctx, buildCmd, run.dir); berr != nil {
				err = fmt.Errorf("post-rebase sanity build failed: %w", berr)
			}
		}
	}
	if err == nil {
		err = e.wt.sweepHandoff(ctx, run.dir, task)
	}
	if err == nil {
		if gidx, ok := gateStage(stages); ok {
			gname, gcmd := stages[gidx].Name, stages[gidx].Command
			if !e.conds[gidx].Eval(task.Labels, false) {
				e.event(ctx, contracts.StageSkipped, task.ID, map[string]any{
					"stage": gname, "when": stages[gidx].When,
				})
			} else {
				e.event(ctx, contracts.StageStarted, task.ID, map[string]any{"stage": gname})
				if _, gerr := e.runCommand(ctx, gcmd, run.dir); gerr != nil {
					gfErr := e.wt.gateFailureErr(ctx, forkSHA, trunkHead, fmt.Errorf("%s: %w", gname, gerr))
					e.event(ctx, contracts.StageFailed, task.ID, map[string]any{
						"stage": gname, "reason": trim(gfErr.Error(), 2000),
					})
					err = &gateFailure{err: gfErr}
				} else {
					e.event(ctx, contracts.StageCompleted, task.ID, map[string]any{"stage": gname})
				}
			}
		}
	}
	if err == nil {
		err = e.wt.merge(ctx, st.Command, task, run.branch, trunkHead)
	}
	if err != nil {
		var ce *ConflictError
		if errors.As(err, &ce) {
			e.event(ctx, contracts.MergeConflicted, task.ID, map[string]any{
				"branch": run.branch, "target_branch": "trunk", "files": ce.Files,
				"hunks": ce.Hunks, "landed_task_ids": ce.LandedTaskIDs,
				"fork_sha": forkSHA, "trunk_head": trunkHead,
			})
		}
		return err
	}
	run.dir = e.cfg.Cwd // post-merge stages run on the merged tree
	run.merged = true
	e.event(ctx, contracts.MergeCompleted, task.ID, map[string]any{
		"branch": run.branch, "target_branch": "trunk", "fork_sha": forkSHA, "trunk_head": trunkHead,
	})
	return nil
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
		model = st.Model
	}
	if model == "" {
		model = agent.Preferences.Model
	}

	e.mu.Lock()
	attempt := e.loops[task.ID+"|"+st.Name] + 1
	e.mu.Unlock()
	log := openOutputLog(e.cfg.MadsHome, e.cfg.SessionID, e.runSeq, task.ID, st.Name, attempt, model)

	res, err := e.cfg.Provider.OneShot(ctx, provider.OneShotRequest{
		Prompt: taskPrompt(st.Name, task, feedback),
		System: agent.Prompt,
		Model:  model,
		Cwd:    dir,
		OnEvent: func(ev provider.Event) {
			switch ev.Kind {
			case provider.EventText:
				log.write(ev.Text)
			case provider.EventToolUse:
				log.tool(ev.Tool)
			case provider.EventSessionRef:
				e.recordRef(ctx, task.ID, st.Name, attempt, ev.Ref)
			}
		},
	})
	if err != nil {
		log.close("error", err.Error())
		return "", fmt.Errorf("agent %s: %w", name, err)
	}
	e.recordUsage(ctx, task.ID, st.Name, attempt, res.Usage)
	if line := lastLine(res.Text); strings.HasPrefix(strings.ToUpper(line), strings.ToUpper(FailMarker)) {
		log.close("fail", res.Text)
		return res.Text, fmt.Errorf("agent %s failed the stage: %s", name, line)
	}
	log.close("pass", res.Text)
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

// recordUsage posts a usage.recorded event for one agent one-shot (spec §8's
// cost metrics hookpoint). stage and attempt attribute the shot directly —
// without them, per-stage cost analysis needs a fragile seq-adjacency join
// against stage.completed, which breaks under interleaved histories.
// Best-effort: a post that fails never fails the stage — the one-shot
// already succeeded, and cost data is a nice-to-have, not a correctness gate.
func (e *engine) recordUsage(ctx context.Context, taskID, stage string, attempt int, usage provider.Usage) {
	raw, err := json.Marshal(map[string]any{
		"model":                       usage.Model,
		"input_tokens":                usage.InputTokens,
		"output_tokens":               usage.OutputTokens,
		"cache_creation_input_tokens": usage.CacheCreationInputTokens,
		"cache_read_input_tokens":     usage.CacheReadInputTokens,
		"cost_usd":                    usage.CostUSD,
		"stage":                       stage,
		"attempt":                     attempt,
	})
	if err != nil {
		raw = []byte(`{}`)
	}
	err = e.cfg.Data.Append(context.WithoutCancel(ctx), &contracts.Envelope{
		Type: contracts.UsageRecorded, SessionID: e.cfg.SessionID, TaskID: taskID, Payload: raw,
	})
	if err != nil {
		log.Printf("engine: post usage.recorded for task %s: %v", taskID, err)
	}
}

// recordRef posts a stage.attempt_ref event as soon as the provider
// announces its native session ref for this one-shot — exact attribution for
// transcript lookup by task+stage+attempt (no path guessing), available
// before the one-shot completes so a live drill-in can tail the transcript.
// Best-effort, like recordUsage: a post that fails never fails the stage.
func (e *engine) recordRef(ctx context.Context, taskID, stage string, attempt int, ref string) {
	raw, err := json.Marshal(map[string]any{
		"stage":   stage,
		"attempt": attempt,
		"ref":     ref,
	})
	if err != nil {
		raw = []byte(`{}`)
	}
	err = e.cfg.Data.Append(context.WithoutCancel(ctx), &contracts.Envelope{
		Type: contracts.StageAttemptRef, SessionID: e.cfg.SessionID, TaskID: taskID, Payload: raw,
	})
	if err != nil {
		log.Printf("engine: post stage.attempt_ref for task %s: %v", taskID, err)
	}
}

// event posts one envelope and returns the server-assigned Seq (0 if the
// post failed) — the caller decides whether that matters (run() captures
// PipelineStarted's Seq as the run's stable identity; every other call site
// discards it).
func (e *engine) event(ctx context.Context, t contracts.EventType, taskID string, payload map[string]any) int64 {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{}`)
	}
	env := &contracts.Envelope{
		Type: t, SessionID: e.cfg.SessionID, TaskID: taskID, Payload: raw,
	}
	_ = e.cfg.Data.Append(context.WithoutCancel(ctx), env)
	return env.Seq
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
