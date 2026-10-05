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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/autostaff"
	"github.com/drewkidwell/mads/pkg/capability"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/inhibit"
	"github.com/drewkidwell/mads/pkg/procid"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fastmodel"
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
	// RunID names this run on pipeline.started and pipeline.completed, the
	// same id the Router puts on the pipeline.cancelled/failed it emits for
	// the run, so the run history pairs a run with its end by id rather than
	// by order (two engines can overlap on one session). Empty omits it.
	RunID string
	// Template is the frozen copy (spec §6: saves never disturb in-flight
	// runs) — parse+validate before calling Run; Run re-checks structure.
	Template *template.Template
	// TemplateVersion is the contracts.Template registry record's Version
	// this Template was parsed from, purely for the pipeline.started event's
	// audit trail (attributing a run to the exact revision it launched
	// from). It's a separate field rather than something Run reads off
	// Template itself because Template is pkg/template's parsed YAML
	// structure — it has no storage-side identity or version at all, only
	// the shape the pipeline actually executes; the version lives on the
	// registry record (contracts.Template) that produced it, which callers
	// fetch separately (see pipelinerun.LoadTemplate). Zero means "unknown"
	// (pre-versioning row, or a caller that bypassed LoadTemplate) and is
	// omitted from the event payload rather than reported as a real version.
	TemplateVersion int
	// Cwd is where command stages run and agent one-shots execute. W3.2
	// replaces this with per-task worktrees.
	Cwd string
	// Concurrency caps simultaneously-running tasks (default 5). Dependencies
	// in the template graph express prerequisites, not anti-collision — two
	// tasks with no dependency between them are safe to run as agent stages
	// in parallel because collision avoidance is the land queue's job, not
	// the scheduler's: every merge stage funnels through the single FIFO
	// landQueue (Not-Rocket-Science Rule, see landAttempt), so integration
	// itself always serializes regardless of how many stages run at once.
	// Real collisions come back as a blocked task with attribution (the
	// conflicting file plus the trunk-side task that landed first), not a
	// silent trunk break.
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
	// Clock supplies wall-clock time for lease-gap detection and stage
	// timeouts (spec: suspend survival). nil defaults to the real clock;
	// tests inject a fake to simulate a suspend gap without sleeping.
	Clock Clock

	// DisableSleepInhibit turns off the OS sleep/idle inhibitor normally
	// held for the run's duration (linux: logind + ScreenSaver where
	// available — COSMIC's idle-suspend only honors the latter; darwin:
	// caffeinate). Default false (inhibitor enabled). Acquiring it never
	// fails or delays the run: a missing/refused service or a failed spawn
	// just logs a warning (pkg/inhibit).
	DisableSleepInhibit bool
	// SleepInhibitor is the inhibitor implementation; nil defaults to the
	// real per-OS inhibit.NewLocker(). Tests inject a fake.
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

	// FastModel powers agent:auto stages that find no surviving candidate: it
	// generates a new agent from the task (pkg/agentgen), which is registered
	// and used for the stage — "that's what auto means", no confirmation
	// step. nil (the default) means a no-candidate auto stage blocks with a
	// clear reason instead of generating.
	FastModel *fastmodel.Runner

	// Session, when set, says Cwd is an isolated session's own worktree
	// (docs/SESSION-WORKTREES-DESIGN.md D8): tasks branch from the session
	// branch and merge back into it, under the session's lock rather than
	// the repo's, after a pre-merge check that saves the agent's
	// overlapping edits (waiting for it to go idle first, up to the merge
	// stage's timeout). Counts on the session record are refreshed after
	// each merge. Nil (the default) merges into Cwd as the user's own
	// checkout, under the repo lock. Used only with Isolated.
	Session *sessionwt.TaskMerge

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
		cfg.Concurrency = 5
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
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
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

	e := &engine{cfg: cfg, agents: agents, conds: conds, loops: map[string]int{}, attempts: map[string]int{}, clock: cfg.Clock}
	if own, ok := procid.Current(procid.Default); ok {
		e.identity = own
	}
	if cfg.MadsHome != "" {
		e.mcpRegistry = capability.NewRegistry(filepath.Join(cfg.MadsHome, "mcp"))
		// A credstore failure degrades to "no credential env for mounted MCP
		// servers", not a fatal error — same best-effort philosophy as
		// services/session/main.go's identical open.
		if cs, err := credstore.Open(filepath.Join(cfg.MadsHome, "credentials.json")); err != nil {
			log.Printf("pipeline: open credential store: %v (mounted MCP servers get no credential env)", err)
		} else {
			e.credStore = cs
		}
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
		repo := cfg.Cwd
		if cfg.Session != nil {
			repo = cfg.Session.Target.Primary
		}
		e.wt = &worktrees{
			primary: cfg.Cwd, repo: repo, root: cfg.WorktreeRoot,
			dataURL: cfg.DataURL, sessionID: cfg.SessionID,
			data: cfg.Data, mergirafPath: mergirafPath,
		}
	}
	return e.run(ctx)
}

// resolveAgents freezes registry lookups at launch: every agent explicitly
// named by the template. agent:auto stages resolve per-task at run time
// (resolveStageAgent) — there's nothing to freeze in advance since the
// selected (or generated) agent depends on the task.
func resolveAgents(ctx context.Context, cfg Config) (map[string]contracts.Agent, error) {
	names := map[string]bool{}
	for _, st := range cfg.Template.Stages {
		if st.Agent != "" && st.Agent != "auto" {
			names[st.Agent] = true
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
	clock    Clock                   // wall-clock source; never nil after Run's defaulting

	// mcpRegistry/credStore back session-time capability mounting (scoped
	// context assembly). Both are nil unless cfg.MadsHome is set — mounting
	// then degrades to a no-op (skill install/MCP start skipped, decision
	// still computed and logged), the same best-effort philosophy as
	// services/session/internal/runner's nil-safe CredStore/MCPRegistry.
	mcpRegistry *capability.Registry
	credStore   *credstore.Store

	mu    sync.Mutex
	loops map[string]int // task|stage → loop-backs taken

	// attempts counts, per task|stage, how many times that stage has
	// actually run — bumped once on every entry (including a loop-back's
	// re-entry into a DIFFERENT stage, and a per-stage retry policy's
	// re-entry into the SAME stage via nextAttempt), never on a when-guard
	// skip. This answers a different question from loops: loops is keyed on
	// the stage whose on_fail fired (the FAILING stage) and caps how many
	// times ITS on_fail may loop back before giving up; attempts is keyed on
	// the stage being ENTERED and answers "which run of this stage is
	// this" — the number openOutputLog, recordUsage, and recordRef need. A
	// build → executor loop-back increments loops["task|build"], but it's
	// executor's OWN attempt counter that must advance so its second run
	// gets its own log file instead of appending into the first one; loops
	// never touches executor's key at all. A policy retry (runStageWithRetry,
	// below) advances this SAME counter for the SAME stage instead — it is a
	// genuinely new attempt, unlike a rate-limit pause (runStageRateLimited),
	// which deliberately keeps re-running the one attempt number it started
	// with. Keep all of these meters conceptually separate even though two
	// of them share this one map.
	attempts map[string]int

	runSeq int64 // the pipeline.started event's seq; written once before any task goroutine starts
}

func (e *engine) run(ctx context.Context) (*Summary, error) {
	e.reapDeadLeases(ctx)
	payload := map[string]any{
		"template":    e.cfg.Template.Name,
		"concurrency": e.cfg.Concurrency,
	}
	if e.cfg.RunID != "" {
		payload["run_id"] = e.cfg.RunID
	}
	// Omitted rather than sent as 0: a genuine version is always >= 1
	// (contracts.Template.Version), so 0 only ever means "the caller
	// couldn't tell us" (a pre-versioning row, or a bypassed LoadTemplate) —
	// reporting that as a real version would be misleading to anything
	// reading the audit trail back later.
	if e.cfg.TemplateVersion > 0 {
		payload["template_version"] = e.cfg.TemplateVersion
	}
	e.runSeq = e.event(ctx, contracts.PipelineStarted, "", payload)

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
	completed := map[string]any{
		"outcome": outcome,
		"done":    len(summary.Done),
		"blocked": len(summary.Blocked),
		"skipped": len(summary.Skipped),
	}
	if e.cfg.RunID != "" {
		completed["run_id"] = e.cfg.RunID
	}
	e.event(ctx, contracts.PipelineCompleted, "", completed)
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
	// ten-minute one-shot). Heartbeat at a third of the TTL, but never
	// trust the ticker's own firing to mean "on schedule": a suspended
	// machine pauses CLOCK_MONOTONIC (what the ticker runs on) for its
	// entire downtime, so every tick compares wall-clock time (e.clock,
	// CLOCK_REALTIME) against the last successful renewal first — a gap
	// past the TTL means the lease is gone even before attempting a
	// renewal. Lease loss of any kind — a proactive wall-clock gap, a
	// renewal rejected outright, or a renewal that succeeded by silently
	// re-taking a lease the reaper already freed while the task's own
	// status moved out from under the engine — aborts the task's in-flight
	// stage the same way: kill it, snapshot the worktree, release, and
	// stop writing; the task is not this engine's to finish anymore.
	ctx, cancelTask := context.WithCancel(ctx)
	defer cancelTask()
	var lost, terminal atomic.Bool
	taskID, taskTitle := task.ID, task.Title // the goroutine must not touch task: UpdateTask rewrites it
	var dirMu sync.Mutex
	var snapshotDir string
	setSnapshotDir := func(dir string) {
		dirMu.Lock()
		snapshotDir = dir
		dirMu.Unlock()
	}
	getSnapshotDir := func() string {
		dirMu.Lock()
		defer dirMu.Unlock()
		return snapshotDir
	}
	// aborted closes once the abort path's snapshot/backup/release work has
	// fully finished — the main goroutine below waits on it at every
	// lost.Load() exit so runTask (and Run, and its caller's summary) never
	// returns while that work is still in flight.
	aborted := make(chan struct{})
	go func() {
		abort := func() {
			lost.Store(true)
			cancelTask()
			e.abortLeaseLoss(context.WithoutCancel(ctx), taskID, taskTitle, getSnapshotDir())
			_ = e.cfg.Data.Release(context.WithoutCancel(ctx), taskID)
			close(aborted)
		}
		tick := time.NewTicker(ttl / 3)
		defer tick.Stop()
		lastRenewal := e.clock.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if e.clock.Now().Sub(lastRenewal) > ttl {
					abort()
					return
				}
				err := e.cfg.Data.ClaimWithIdentity(ctx, taskID, ttl, e.identity)
				if errors.Is(err, dataclient.ErrConflict) || errors.Is(err, dataclient.ErrNotFound) {
					abort()
					return
				}
				if err != nil {
					continue // transient; the meter has slack for a retry
				}
				lastRenewal = e.clock.Now()
				// terminal guards the engine's own legitimate block/done
				// writes: only a status that moved to something other than
				// in_progress WITHOUT the engine doing it (the reaper, on a
				// suspend gap) means the lease this renewal just re-took is
				// not actually ours to keep working under.
				if cur, err := e.cfg.Data.Task(ctx, taskID); err == nil &&
					cur.Status != contracts.StatusInProgress && !terminal.Load() {
					abort()
					return
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
		setSnapshotDir(dir)
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
				<-aborted
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

		// One counter bump per actual stage entry — including a fresh entry
		// via loop_back into a stage the failure jumped to, which is exactly
		// the case e.loops cannot answer (see the attempts field comment):
		// it's keyed on this stage regardless of which stage's on_fail sent
		// execution here. Computed once here, not re-derived at the point of
		// use, so a rate-limit retry of the same attempt (runStageRateLimited,
		// below) never double-counts it.
		attemptKey := task.ID + "|" + st.Name
		e.mu.Lock()
		e.attempts[attemptKey]++
		attempt := e.attempts[attemptKey]
		e.mu.Unlock()

		var output string
		var err error
		if e.wt != nil && isMergeStage(st) {
			// Exactly one land attempt (rebase -> gate -> merge) at a
			// time, stable FIFO order (spec: "Not Rocket Science Rule").
			// The in-process landQueue only serializes within this engine;
			// acquireWorkspaceLock extends that across processes, so a CLI
			// `mads run` and a Router-spawned pipeline child on the same
			// workspace never interleave a land attempt either.
			//
			// A session folder target (Config.Session) is the exception:
			// nothing here writes the user's checkout, so the repo lock
			// isn't taken at all, and landAttempt holds the session lock
			// only around the write itself (mergeIntoSession) — never
			// across the gate, which would stall the user's Commit for
			// as long as the gate runs.
			release := e.wt.land.acquire()
			if e.cfg.Session != nil {
				err = e.landAttempt(ctx, st, stages, &run, task)
			} else if wsRelease, wsErr := acquireWorkspaceLock(ctx, e.wt); wsErr != nil {
				err = wsErr
			} else {
				err = e.landAttempt(ctx, st, stages, &run, task)
				wsRelease()
			}
			release()
			if run.merged {
				setSnapshotDir("") // work already landed on the primary; nothing left to snapshot
			}
			output = ""
			if err != nil {
				output = err.Error()
			}
		} else {
			var blocked bool
			// runStageWithRetry layers the stage's retry: policy on top of
			// runStageRateLimited's own rate-limit handling; it never
			// applies to the merge stage (this branch excludes it —
			// pkg/template.Validate rejects retry: on the merge stage
			// outright, so st.Retry/Defaults.Retry resolving here would
			// only ever apply to a genuine non-merge stage anyway).
			output, err, blocked = e.runStageWithRetry(ctx, st, &task, feedback, run.dir, attempt, &lost, &terminal, blockReason)
			if blocked {
				return contracts.StatusBlocked
			}
		}
		if lost.Load() {
			<-aborted
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
		<-aborted
		return task.Status
	}
	if e.wt != nil && run.merged {
		e.wt.remove(context.WithoutCancel(ctx), run.worktree, run.branch)
	}
	terminal.Store(true)
	if !e.setStatus(ctx, &task, contracts.StatusDone) {
		return contracts.StatusBlocked
	}
	if e.wt != nil {
		// Stale-backup hygiene: any backup/<id> left from an earlier block on
		// this task is no longer the salvage-of-record now that real work has
		// landed.
		e.wt.deleteBackupBranch(context.WithoutCancel(ctx), task.ID)
	}
	return contracts.StatusDone
}

// runStageWithRetry runs st via runStageRateLimited, then — for an ordinary
// stage failure (not a rate-limit pause, which runStageRateLimited already
// resolved internally; not a lost lease) — consults the stage's retry:
// policy (pkg/template.Retry, resolved by retryPolicy) before handing the
// failure back to runTask's on_fail routing.
//
// This is the one place retry and loop_back meet, and they compose in
// exactly one direction: a stage retries ITSELF, here, up to policy.Max
// times; only once retries are exhausted (or the failure doesn't qualify
// under `when`, via retryEligible) does the failure fall through to
// runTask's on_fail handling — loop_back, e.loops, max_loops, the on_loop
// guard, `then: block` — none of which this function touches at all. From
// on_fail's point of view, a retry-exhausted failure and a failure that was
// never retried are the same value: nothing marks the difference.
//
// The local finalAttempt tracks the stage-entry attempt number the current
// try is running under. It starts as the caller's attempt (the number
// runTask's top-of-loop bump already computed for this stage entry), and
// every policy retry bumps it via e.nextAttempt — a policy retry is a
// genuinely new attempt, so it needs its own output log
// (<stage>-attempt-<n>.log) and its own usage.recorded/stage.attempt_ref
// attribution — which is the opposite of runStageRateLimited's OWN internal
// loop just below, which deliberately keeps re-running the SAME attempt
// number across every rate-limit pause. Those two loops are independent
// meters over the same stage entry; see runStageRateLimited's doc comment
// for why that one holds its attempt number fixed.
//
// It is deliberately NOT returned: everything that consumes an attempt
// number (the output log path, usage.recorded, stage.attempt_ref) is
// emitted downstream of the value threaded INTO runStage, so the caller has
// nothing to do with it. runTask recomputes its own attempt from e.attempts
// at the top of the next iteration.
func (e *engine) runStageWithRetry(ctx context.Context, st template.Stage, task *contracts.Task, feedback, dir string, attempt int, lost, terminal *atomic.Bool, blockReason func(string) string) (output string, err error, blocked bool) {
	maxRetries, when, backoff, hasPolicy := retryPolicy(e.cfg.Template, st)
	finalAttempt := attempt
	for retries := 0; ; retries++ {
		output, err, blocked = e.runStageRateLimited(ctx, st, task, feedback, dir, finalAttempt, lost, terminal, blockReason)
		if err == nil || blocked || lost.Load() {
			return
		}
		// ctx here is runStageWithRetry's OWN ctx, not runStage's per-call
		// sctx — runStage builds sctx via context.WithTimeout(ctx, timeout)
		// and defer cancel()s it internally, so a stage timeout only ever
		// cancels that inner sctx; ctx itself is untouched by it and this
		// guard never fires for one (a timed-out attempt is retried like
		// any other infra failure, subject only to the checks below). What
		// DOES cancel ctx — run cancellation or the task's lease being
		// lost — must not spend a retry trying to re-run into a context
		// that's already done; runStage would just fail again immediately
		// for an unrelated reason.
		if !hasPolicy || retries >= maxRetries || ctx.Err() != nil || !retryEligible(err, when) {
			return
		}
		e.event(ctx, contracts.StageRetried, task.ID, map[string]any{
			"stage": st.Name, "attempt": retries + 1, "stage_attempt": finalAttempt, "max": maxRetries, "reason": trim(err.Error(), 2000),
		})
		// backoff: zero (the template default) means "retry immediately",
		// a legitimate choice pkg/template's checkRetry explicitly allows —
		// so only pause when the template actually asked for one, and race
		// the pause against ctx so a cancelled run doesn't sit out a long
		// backoff before noticing.
		if backoff > 0 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
			}
		}
		// Feed the failed attempt's output to the next try, the same way
		// loop_back feedback reaches an agent today (taskPrompt) — a retry
		// with no context about why the last try failed is a wasted call.
		feedback = err.Error()
		finalAttempt = e.nextAttempt(task.ID, st.Name)
	}
}

// retryPolicy resolves the effective retry policy for st, most-specific
// level first: the stage's own retry: overrides the template's defaults:
// retry:; if neither is set, there is no policy at all. Mirrors
// stageTimeout's resolution shape and is equally defensive about a nil
// Template/Defaults/Retry — a directly constructed Config in a test may
// carry any of those as nil, since engine.Run does not itself call
// pkg/template.Validate (only pipelinerun.LoadTemplate does).
//
// hasPolicy is what distinguishes "no retry: block at all" from an explicit
// "retry: {max: 0}": both produce zero actual retries, but only the latter
// is a real, validated policy (pkg/template.Validate already enforces max
// in [0,10] and when in {"", "error", "always", "never"} — retryPolicy does
// not re-validate either, it only resolves precedence).
func retryPolicy(t *template.Template, st template.Stage) (maxRetries int, when string, backoff time.Duration, hasPolicy bool) {
	if st.Retry != nil {
		return st.Retry.Max, st.Retry.When, time.Duration(st.Retry.Backoff), true
	}
	if t != nil && t.Defaults != nil && t.Defaults.Retry != nil {
		return t.Defaults.Retry.Max, t.Defaults.Retry.When, time.Duration(t.Defaults.Retry.Backoff), true
	}
	return 0, "", 0, false
}

// retryEligible reports whether a stage failure qualifies for a policy
// retry under when (pkg/template.Retry.When — already validated by
// pkg/template to be "", "error", "always", or "never"). The distinction it
// keys off is exactly stageVerdictError: an infrastructure failure (a
// crashed provider call, a transport error, or the stage timeout/lease-loss
// context cancelling the attempt out from under it) is worth retrying by
// default; a considered verdict about the work itself (an agent's
// FailMarker, a command's own non-zero exit) is not — a reviewer that
// rejected the work will reject it again. "always" exists precisely for the
// rare stage that wants that anyway (e.g. flaky-detection).
func retryEligible(err error, when string) bool {
	if when == "never" {
		return false
	}
	var verdict *stageVerdictError
	if errors.As(err, &verdict) {
		return when == "always"
	}
	return true // infra failure: "" (the default, meaning "error") and "always" both retry it
}

// nextAttempt bumps and returns the stage-entry attempt counter for
// task|stage — the same e.attempts meter runTask's top-of-loop bump uses
// for a fresh stage entry, exposed here for a policy retry
// (runStageWithRetry): unlike runStageRateLimited's rate-limit pauses,
// which deliberately keep re-running the SAME attempt number, a policy
// retry is a genuinely new attempt and must get a new number — getting this
// wrong silently recreates the exact "second run appends into the first
// attempt's log instead of opening its own" bug the attempts field exists
// to prevent (see its doc comment on the engine struct).
func (e *engine) nextAttempt(taskID, stage string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.attempts[taskID+"|"+stage]++
	return e.attempts[taskID+"|"+stage]
}

// runStageRateLimited runs st, pausing and retrying the SAME attempt (same
// feedback, no loop-back consumed) each time the provider reports a rolling
// rate limit — the engine waits it out instead of treating it like a stage
// failure (spec §11.9). blocked reports whether it already parked the task
// at the retry cap; callers must return StatusBlocked without further
// handling in that case. stageAttempt is the stage-entry attempt number the
// caller already computed (e.attempts) — passed straight through to
// runStage/runAgent unchanged across every rate-limit retry below, since a
// rate limit retry re-runs the SAME attempt rather than starting a new one;
// it is unrelated to this function's own local attempt counter, which counts
// rate-limit retries instead.
func (e *engine) runStageRateLimited(ctx context.Context, st template.Stage, task *contracts.Task, feedback, dir string, stageAttempt int, lost *atomic.Bool, terminal *atomic.Bool, blockReason func(string) string) (output string, err error, blocked bool) {
	var attempt int
	for {
		output, err = e.runStage(ctx, st, *task, feedback, dir, stageAttempt)
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
			"stage": st.Name, "attempt": attempt, "stage_attempt": stageAttempt, "message": rle.Message,
			"backoff": e.cfg.RateLimitBackoff.String(),
		})
		select {
		case <-time.After(e.cfg.RateLimitBackoff):
		case <-ctx.Done():
		}
		e.event(ctx, contracts.StageRateLimitCleared, task.ID, map[string]any{
			"stage": st.Name, "attempt": attempt, "stage_attempt": stageAttempt,
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

// stageVerdictError marks a stage failure that is a considered verdict about
// the work itself — an agent's FailMarker line (runAgent), or a command's
// own non-zero exit (runCommand) — as distinct from an infrastructure
// failure (a crashed or errored provider call, a transport error, or the
// stage timeout/lease-loss context cancelling the attempt out from under
// it). retryEligible keys retry policy's `when` off exactly this split: an
// infra failure is worth retrying by default, a verdict usually isn't (a
// reviewer that rejected the work will reject it again). Modeled on
// gateFailure (above), which wraps a different distinction the same way and
// is unwrapped the same way, via errors.As — never string-matched.
type stageVerdictError struct{ err error }

func (v *stageVerdictError) Error() string { return v.err.Error() }
func (v *stageVerdictError) Unwrap() error { return v.err }

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
//
// A gate failure is never taken at face value: it's the rebase-before-merge
// conflict path (unchanged, above) that a genuinely unmergeable pair of
// tasks hits, but a gate FAILURE this late — after both the task's own gate
// at fork and trunk's gate before this attempt were green — is exactly as
// likely to be flake/load interference from the land queue's own
// serialization (a still-warming cache, a port collision, a slow disk) as a
// real regression. triageGateFailure reruns the failing portion in isolation
// before deciding which; see its doc comment. Isolation-green retries the
// whole land attempt (rebase + full gate), bounded at maxLandRetries so a
// stubborn flake still surfaces to a human rather than looping forever;
// isolation-red fails fast — a deterministic failure costs exactly one
// triage round, never an extra land-attempt retry.
//
// A gate failure whose output is lint-shaped (see classifyLintFailure) never
// goes through that flake triage at all — a lint tool sees the same source
// every rerun, so "maybe it's a flake" is never the right question. Instead:
// a failure entirely within the whitelisted formatter-class linters
// (gofmt/gofumpt/goimports) gets exactly one mechanical autofix attempt
// (runLintAutofix) — the emergent class two individually-clean branches can
// combine into (stale imports, formatting drift after a semantic merge) that
// no single task's own gate at fork could have caught. A lint failure always
// breaks out of the retry loop below (landing or blocking, never looping),
// so the one-attempt bound holds by construction: there is no second pass
// through this branch to guard against. A change is committed as its own
// "land: mechanical lint fixes" commit and the gate reruns once more;
// a failure naming anything outside the whitelist (errcheck, staticcheck,
// unused, ...) never gets an autofix attempt — "unused after merge" can mean
// the merge dropped a caller, and that's information for a human, not noise.
// Either way a lint failure that isn't resolved by the one autofix attempt
// blocks immediately (spec: trunk went lint-red silently once because the
// land gate ran build+test but not lint — see 672a6bc and the ladder this
// documents in services/pipeline/README.md).
func (e *engine) landAttempt(ctx context.Context, st template.Stage, stages []template.Stage, run *taskRun, task contracts.Task) error {
	forkSHA := e.wt.fork(task.ID)
	mergeDriver := "git"
	if e.wt.mergirafPath != "" {
		mergeDriver = mergirafDriverName
	}
	target := "trunk"
	var into *sessionMerge
	if e.cfg.Session != nil {
		target = e.cfg.Session.Target.Branch
		into = &sessionMerge{deadline: time.Now().Add(e.stageTimeout(st))}
	}
	e.event(ctx, contracts.MergeAttempted, task.ID, map[string]any{
		"branch": run.branch, "target_branch": target, "fork_sha": forkSHA,
		"merge_driver": mergeDriver,
	})

	err := e.wt.commitStray(ctx, run.dir, task)
	var trunkHead string
	// Each round is rebase -> gate -> merge. Merging into the user's
	// checkout takes one round: the repo lock has been held throughout, so
	// trunk can't have moved. A session folder is only locked for the merge
	// itself (mergeIntoSession), so when its branch moved after the gate —
	// a Commit, or the safety commit of the agent's overlapping edits — the
	// round starts over and the task branch absorbs the move in its own
	// worktree, where a conflict is resolvable, rather than in the folder
	// the agent works in.
	for round := 0; err == nil; round++ {
		trunkHead, err = e.rebaseAndGate(ctx, stages, run, task, forkSHA)
		if err != nil {
			break
		}
		if into == nil {
			err = e.wt.merge(ctx, st.Command, task, run.branch, trunkHead)
			break
		}
		var again bool
		again, err = e.mergeIntoSession(ctx, st, task, run, trunkHead, round >= maxSessionMergeRounds-1, into)
		if !again {
			break
		}
	}
	if err != nil {
		var ce *ConflictError
		if errors.As(err, &ce) {
			e.event(ctx, contracts.MergeConflicted, task.ID, map[string]any{
				"branch": run.branch, "target_branch": target, "files": ce.Files,
				"hunks": ce.Hunks, "landed_task_ids": ce.LandedTaskIDs,
				"fork_sha": forkSHA, "trunk_head": trunkHead,
			})
		}
		return err
	}
	run.dir = e.cfg.Cwd // post-merge stages run on the merged tree
	run.merged = true
	done := map[string]any{
		"branch": run.branch, "target_branch": target, "fork_sha": forkSHA, "trunk_head": trunkHead,
	}
	if into != nil {
		into.annotate(done)
		if rerr := e.cfg.Session.Refresh(ctx); rerr != nil {
			log.Printf("engine: refresh session counts after merging %s: %v", task.ID, rerr)
		}
	}
	e.event(ctx, contracts.MergeCompleted, task.ID, done)
	return nil
}

// rebaseAndGate is one land round's rebase -> gate, retried on a triaged
// flake (see landAttempt): it brings trunk into the task branch and runs
// the gate against the result, returning the trunk HEAD it gated against.
func (e *engine) rebaseAndGate(ctx context.Context, stages []template.Stage, run *taskRun, task contracts.Task, forkSHA string) (trunkHead string, err error) {
	for attempt := 0; err == nil; attempt++ {
		var rebased bool
		rebased, trunkHead, err = e.wt.rebaseBeforeMerge(ctx, run.dir, task)
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
		if err != nil {
			break
		}

		gidx, hasGate := gateStage(stages)
		if !hasGate {
			break // no verify stage configured: nothing to gate, proceed to merge
		}
		gname, gcmd := stages[gidx].Name, stages[gidx].Command
		if !e.conds[gidx].Eval(task.Labels, false) {
			e.event(ctx, contracts.StageSkipped, task.ID, map[string]any{
				"stage": gname, "when": stages[gidx].When,
			})
			break
		}
		e.event(ctx, contracts.StageStarted, task.ID, map[string]any{"stage": gname})
		gout, gerr := e.runCommand(ctx, gcmd, run.dir)
		if gerr == nil {
			e.event(ctx, contracts.StageCompleted, task.ID, map[string]any{"stage": gname})
			break
		}

		// Lint failures are deterministic and never flake-retried (a rerun of
		// the same command sees the same source): a mechanical, whitelisted
		// fixer pass gets exactly one shot at a fixable-class failure before
		// falling to block; a non-fixable-class failure (errcheck,
		// staticcheck, unused — "unused after merge" can mean the merge
		// dropped a caller, that's information for a human, not noise) never
		// gets an autofix attempt at all. Only when the output isn't
		// lint-shaped at all does the existing flake-triage path run,
		// unchanged.
		if fixable, isLint, linters := classifyLintFailure(gout); isLint {
			if fixable {
				if res := e.runLintAutofix(ctx, run.dir); res.changed {
					if cerr := e.wt.commitLintAutofix(ctx, run.dir, task, res); cerr != nil {
						err = cerr
						break
					}
					e.event(ctx, contracts.LandAutofixed, task.ID, map[string]any{
						"branch": run.branch, "fork_sha": forkSHA, "trunk_head": trunkHead,
						"stage": gname, "fixers": res.fixers, "diff_summary": res.summary,
					})
					_, gerr = e.runCommand(ctx, gcmd, run.dir)
				}
			}
			if gerr == nil {
				e.event(ctx, contracts.StageCompleted, task.ID, map[string]any{"stage": gname})
				break
			}
			gfErr := e.wt.gateFailureErr(ctx, forkSHA, trunkHead, fmt.Errorf("%s: %w", gname, gerr))
			evidence := fmt.Sprintf("\n\ngate auto-triage: classification=lint, linters=%s (deterministic, never flake-retried)\n",
				strings.Join(linters, ", "))
			gfErr = fmt.Errorf("%w%s", gfErr, evidence)
			e.event(ctx, contracts.StageFailed, task.ID, map[string]any{
				"stage": gname, "reason": trim(gfErr.Error(), 2000),
			})
			err = &gateFailure{err: gfErr}
			break
		}

		triage := e.triageGateFailure(ctx, run.dir, gcmd, gout)
		if triage.classification == "flake" && attempt < maxLandRetries {
			e.event(ctx, contracts.LandRetried, task.ID, map[string]any{
				"branch": run.branch, "fork_sha": forkSHA, "trunk_head": trunkHead,
				"stage": gname, "attempt": attempt + 1, "classification": triage.classification,
				"packages": triage.packages, "isolation_runs": len(triage.runs),
			})
			continue // retry the whole land attempt: rebase + full gate
		}

		gfErr := e.wt.gateFailureErr(ctx, forkSHA, trunkHead, fmt.Errorf("%s: %w", gname, gerr))
		gfErr = fmt.Errorf("%w%s", gfErr, triage.evidence(trunkHead))
		e.event(ctx, contracts.StageFailed, task.ID, map[string]any{
			"stage": gname, "reason": trim(gfErr.Error(), 2000),
		})
		err = &gateFailure{err: gfErr}
	}
	return trunkHead, err
}

// stageTimeoutPoll is how often the wall-clock timeout watchdog checks
// elapsed time against the stage timeout. This interval itself doesn't need
// to be suspend-aware — the watchdog's own lateness firing doesn't matter,
// it only needs to eventually notice a timeout has passed once real time
// resumes — only the elapsed comparison it makes needs the wall clock.
const stageTimeoutPoll = 200 * time.Millisecond

// stageTimeout resolves the effective timeout for st, most specific level
// first: the stage's own timeout: overrides the template's defaults:
// timeout:, which overrides the engine-wide cfg.StageTimeout (already
// defaulted to 10m by Run if the caller left it zero). Most-specific-wins
// is the same precedence the doc comments on Stage.Timeout and
// Defaults.Timeout promise — a stage that bothers to spell out its own
// timeout: is stating intent explicitly, and no broader fallback should
// second-guess it.
//
// A level is skipped, rather than propagated, whenever it is non-positive.
// That covers two cases the same way on purpose: a template author who
// wrote a genuinely non-positive timeout (template.Validate rejects those
// before a template-driven launch ever reaches here) and pkg/template's
// invalidDuration sentinel for a timeout string that failed to parse
// (Validate rejects that too, but engine.Run does not itself call
// Validate — only pipelinerun.LoadTemplate does — so a directly
// constructed Config in a test can still carry one). The sentinel is
// math.MinInt64 nanoseconds, deeply negative, so the same ">0" check that
// rejects an author's "timeout: 0" also rejects it without needing to
// import pkg/template's unexported sentinel check. Falling through to the
// next level is safer than handing a stage a deadline that fires before
// it can do anything, or never.
func (e *engine) stageTimeout(st template.Stage) time.Duration {
	if st.Timeout != nil {
		if d := time.Duration(*st.Timeout); d > 0 {
			return d
		}
	}
	if t := e.cfg.Template; t != nil && t.Defaults != nil && t.Defaults.Timeout != nil {
		if d := time.Duration(*t.Defaults.Timeout); d > 0 {
			return d
		}
	}
	return e.cfg.StageTimeout
}

// runStage executes one stage in dir; a nil error is a pass. output carries
// the failure detail for feedback notes.
func (e *engine) runStage(ctx context.Context, st template.Stage, task contracts.Task, feedback, dir string, attempt int) (output string, err error) {
	timeout := e.stageTimeout(st)
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// context.WithTimeout runs on CLOCK_MONOTONIC, which pauses for a
	// suspended machine's entire downtime: a stage 5 minutes into a
	// 10-minute budget before a 13-hour suspend would otherwise wake with
	// its deadline still 5 minutes away, handing the agent 13 extra hours
	// it never should have had. Race a wall-clock watchdog alongside it —
	// whichever notices the timeout first cancels sctx; the watchdog exits
	// on its own once sctx is done either way, so a stage that finishes
	// normally never waits on stageTimeoutPoll.
	start := e.clock.Now()
	watchdog := time.NewTicker(stageTimeoutPoll)
	go func() {
		defer watchdog.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-watchdog.C:
				if e.clock.Now().Sub(start) >= timeout {
					cancel()
					return
				}
			}
		}
	}()
	if st.Command != "" {
		return e.runCommand(sctx, st.Command, dir)
	}
	return e.runAgent(sctx, st, task, feedback, dir, attempt)
}

func (e *engine) runCommand(ctx context.Context, command, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	// sh doesn't always exec-optimize a single simple command (e.g. under
	// go test's environment it forks instead) — so on cancel, killing only
	// cmd.Process can leave a grandchild alive holding the CombinedOutput
	// pipe open, and Wait/CombinedOutput would block until that grandchild
	// exits on its own instead of returning promptly. Run sh as its own
	// process group leader and kill the whole group on cancel; WaitDelay is
	// a backstop that force-closes the pipes if a stray process still
	// refuses to die, so this always returns instead of hanging the task's
	// lease release.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	raw, err := cmd.CombinedOutput()
	out := string(raw)
	if err != nil {
		wrapped := fmt.Errorf("command %q failed: %v\n%s", command, err, trim(out, 2000))
		if ctx.Err() != nil {
			// ctx was cancelled out from under the command — the stage
			// timeout watchdog or the task's own lease-loss cancellation
			// fired, and cmd.Cancel SIGKILLed the process. That surfaces
			// through CombinedOutput as an ordinary non-zero exit (a
			// killed process is not a "success status" in exec.Cmd's
			// terms, so Wait reports the kill, not ctx.Err() itself) —
			// but it is an infrastructure failure, not the command's own
			// considered exit status, so it must NOT be wrapped as a
			// stageVerdictError: retry policy's default (when: error)
			// needs to see this as retryable.
			return out, wrapped
		}
		// The command ran to completion under its own steam and simply
		// exited non-zero: a considered verdict, exactly like an agent's
		// FailMarker — retry policy's `when: error` (the default) does
		// not retry this; only `when: always` does.
		return out, &stageVerdictError{err: wrapped}
	}
	return out, nil
}

// resolveStageAgent resolves which agent runs st for task: an explicitly
// named agent is exactly what the template froze at launch (resolveAgents).
// "auto" instead resolves task's effective (task+workspace) capability set
// as a hard filter (spec §9) via effectiveCapabilities, then routes through
// pkg/autostaff.SelectOrGenerate — the same shared select-then-generate-
// fallback staffing path interactive session-start agent auto uses (owner
// decision, 2026-07-30: one implementation, two call sites). SelectOrGenerate
// logs the decision as an agent.selected event regardless of outcome, and
// falls back to on-demand generation via pkg/agentgen when no candidate
// survives (registry-grounded — invented capability references are
// dropped, never persisted). A generation failure — including no FastModel
// configured at all — fails the stage with a clear reason; it never
// silently falls back to a hardcoded agent.
func (e *engine) resolveStageAgent(ctx context.Context, st template.Stage, task contracts.Task) (string, contracts.Agent, error) {
	if st.Agent != "auto" {
		return st.Agent, e.agents[st.Agent], nil
	}

	tools, mcpServers, skills, capErr := e.effectiveCapabilities(ctx, task)
	if capErr != nil {
		return "", contracts.Agent{}, fmt.Errorf("auto-select: %w", capErr)
	}
	req := agentselect.Request{
		TaskID: task.ID, SessionID: e.cfg.SessionID, Stage: st.Name,
		Title: task.Title, Description: task.Description, Labels: task.Labels,
		RequiredTools: tools, RequiredMCPServers: mcpServers, RequiredSkills: skills,
	}
	return autostaff.SelectOrGenerate(ctx, e.cfg.Data, e.cfg.FastModel, req)
}

// effectiveCapabilities resolves task's effective required capabilities —
// the task+workspace union (agentselect.EffectiveCapabilities) expanded
// against the live capability registry (agentselect.ExpandCapabilities) —
// into the Tools/MCPServers/Skills slices a Request needs so auto-select
// treats declared requirements as a hard filter (spec §9), not merely a
// ranking signal.
func (e *engine) effectiveCapabilities(ctx context.Context, task contracts.Task) (tools, mcpServers, skills []string, err error) {
	sess, err := e.cfg.Data.Session(ctx, e.cfg.SessionID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fetch session %q: %w", e.cfg.SessionID, err)
	}
	var workspace contracts.Workspace
	if sess.Workspace != "" {
		workspace, err = e.cfg.Data.Workspace(ctx, sess.Workspace)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fetch workspace %q: %w", sess.Workspace, err)
		}
	}
	registry, err := e.cfg.Data.Capabilities(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fetch capability registry: %w", err)
	}
	names := agentselect.EffectiveCapabilities(task, workspace)
	tools, mcpServers, skills = agentselect.ExpandCapabilities(names, registry)
	return tools, mcpServers, skills, nil
}

// scopedCapabilities computes which of agent's declared MCP servers and
// skills a stage session should mount: the task's effective capability set
// (task+workspace required capabilities) intersected with what the agent
// declares — never more than the agent declared. An empty effective set (no
// task/workspace capability requirements on this task) mounts the agent's
// full declaration unchanged: scoped is false and nothing is skipped,
// matching behavior from before this scoping existed.
//
// System/baseline capabilities that every session needs are never scoped
// out — but no such marker exists on contracts.Capability today
// (Capability.System is a registry lifecycle flag, "ships with mads;
// editable but not deletable" §12, unrelated to session-time mounting), so
// there is nothing to exempt yet. This is where that exemption belongs if
// one is introduced later.
//
// Best-effort: a Data Service error falls back to the full agent
// declaration (scoped=false) rather than failing the stage, since scoping
// is an optimization, not a correctness requirement.
func (e *engine) scopedCapabilities(ctx context.Context, task contracts.Task, agent contracts.Agent) (mountedMCP, skippedMCP, mountedSkills, skippedSkills []string, scoped bool) {
	full := func() (mountedMCP, skippedMCP, mountedSkills, skippedSkills []string, scoped bool) {
		return agent.Capabilities.MCPServers, nil, agent.Capabilities.Skills, nil, false
	}
	sess, err := e.cfg.Data.Session(ctx, e.cfg.SessionID)
	if err != nil {
		log.Printf("pipeline: scope capabilities: fetch session %q: %v (mounting full agent declaration)", e.cfg.SessionID, err)
		return full()
	}
	var workspace contracts.Workspace
	if sess.Workspace != "" {
		workspace, err = e.cfg.Data.Workspace(ctx, sess.Workspace)
		if err != nil {
			log.Printf("pipeline: scope capabilities: fetch workspace %q: %v (mounting full agent declaration)", sess.Workspace, err)
			return full()
		}
	}
	names := agentselect.EffectiveCapabilities(task, workspace)
	if len(names) == 0 {
		return full()
	}
	effective := make(map[string]struct{}, len(names))
	for _, n := range names {
		effective[n] = struct{}{}
	}
	mountedMCP, skippedMCP = intersectCapabilities(agent.Capabilities.MCPServers, effective)
	mountedSkills, skippedSkills = intersectCapabilities(agent.Capabilities.Skills, effective)
	return mountedMCP, skippedMCP, mountedSkills, skippedSkills, true
}

// intersectCapabilities splits declared into what's in the effective set
// (mounted) and what isn't (skipped), preserving declared's order.
func intersectCapabilities(declared []string, effective map[string]struct{}) (mounted, skipped []string) {
	for _, name := range declared {
		if _, ok := effective[name]; ok {
			mounted = append(mounted, name)
		} else {
			skipped = append(skipped, name)
		}
	}
	return mounted, skipped
}

// mountCapabilities installs the mounted skills and starts the mounted MCP
// servers for one stage session (spec §12.4's availability/injection split:
// this is session-time mounting, not a rewrite of provider agent config
// files), then logs the mount/skip decision so a "tool missing" report is
// diagnosable as scoping vs agent declaration vs health. Best-effort
// throughout — a capability that fails to resolve, install, or start is
// logged and the stage proceeds; mounting never fails the stage itself.
func (e *engine) mountCapabilities(ctx context.Context, task contracts.Task, stage string, mountedMCP, skippedMCP, mountedSkills, skippedSkills []string, dir string, scoped bool) {
	for _, name := range mountedSkills {
		c, err := e.cfg.Data.Capability(ctx, name)
		if err != nil {
			log.Printf("pipeline: mount skill %q: %v", name, err)
			continue
		}
		if err := capability.InstallSkill(c, dir); err != nil {
			log.Printf("pipeline: install skill %q: %v", name, err)
		}
	}
	if e.mcpRegistry != nil {
		for _, name := range mountedMCP {
			c, err := e.cfg.Data.Capability(ctx, name)
			if err != nil {
				log.Printf("pipeline: mount mcp %q: %v", name, err)
				continue
			}
			var env []string
			if e.credStore != nil {
				env, _ = e.credStore.ResolveEnv(c.CredentialEnvVars)
			}
			if _, err := e.mcpRegistry.EnsureStarted(ctx, c, env); err != nil {
				log.Printf("pipeline: start mcp %q: %v", name, err)
			}
		}
	}
	e.event(ctx, contracts.StageCapabilitiesMounted, task.ID, map[string]any{
		"stage":               stage,
		"scoped":              scoped,
		"mounted_mcp_servers": mountedMCP,
		"skipped_mcp_servers": skippedMCP,
		"mounted_skills":      mountedSkills,
		"skipped_skills":      skippedSkills,
	})
}

func (e *engine) runAgent(ctx context.Context, st template.Stage, task contracts.Task, feedback, dir string, attempt int) (string, error) {
	name, agent, err := e.resolveStageAgent(ctx, st, task)
	if err != nil {
		return err.Error(), err
	}

	mountedMCP, skippedMCP, mountedSkills, skippedSkills, scoped := e.scopedCapabilities(ctx, task, agent)
	e.mountCapabilities(ctx, task, st.Name, mountedMCP, skippedMCP, mountedSkills, skippedSkills, dir, scoped)

	model := e.cfg.Model
	if model == "" {
		model = st.Model
	}
	if model == "" {
		model = agent.Preferences.Model
	}

	// attempt is this stage's entry count (e.attempts), computed once by the
	// caller at stage entry — not e.loops, which counts a DIFFERENT stage's
	// loop-back consumption (see the attempts field comment on the engine
	// struct) and would leave a looped-into stage's own attempt number stuck
	// at 1 forever, silently appending its second run's log into the first's
	// file instead of opening executor-attempt-2.log.
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
		// Infrastructure failure — a crash, a transport error, or (sctx's
		// deadline in runStage) the stage timeout cancelling this one-shot
		// out from under it. Left unwrapped so retryEligible's default
		// (when: error) retries it.
		log.close("error", err.Error())
		return "", fmt.Errorf("agent %s: %w", name, err)
	}
	e.recordUsage(ctx, task.ID, st.Name, attempt, res.Usage)
	if line := lastLine(res.Text); strings.HasPrefix(strings.ToUpper(line), strings.ToUpper(FailMarker)) {
		log.close("fail", res.Text)
		// A considered verdict, not an infrastructure failure — wrapped so
		// retryEligible only retries it under `when: always`: a reviewer
		// that rejected the work will reject it again, so retrying under
		// the default policy would just waste the call.
		return res.Text, &stageVerdictError{err: fmt.Errorf("agent %s failed the stage: %s", name, line)}
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
		// The worktree is never reset on retry or loop-back (owner
		// decision): the failing stage may be far down the pipeline, and
		// discarding it would throw away everything earlier stages
		// legitimately produced. Whenever there's feedback to relay, this
		// attempt is re-entering the SAME working tree an earlier attempt
		// (of this stage, or a later one that sent work back) left
		// behind — say so, since the agent is now looking at a tree it (or
		// a prior attempt) may have half-edited, not a clean checkout.
		b.WriteString("\nThe working tree may contain partial work from a previous failed attempt at this task — inspect it before assuming a clean slate.\n")
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
// When the task's branch (mads/<id>) exists, it's force-backed-up to
// backup/<id> before the block is recorded and the backup branch is named in
// the note — salvage-safe by construction, since the claim path (create,
// above) wipes the live worktree/branch the moment this task is requeued and
// claimed again (the agent convention of checking backup/tsk_<id> before
// re-deriving a design now holds for every block, not just a lucky find).
func (e *engine) block(ctx context.Context, task *contracts.Task, stage, reason string) {
	ctx = context.WithoutCancel(ctx)
	if e.wt != nil && e.wt.backupBranch(ctx, task.ID) {
		reason = fmt.Sprintf("%s\n\nBranch backed up to backup/%s before this block.", reason, task.ID)
	}
	e.note(ctx, task.ID, contracts.NoteBlocker, fmt.Sprintf("stage %s: %s", stage, reason))
	e.setStatus(ctx, task, contracts.StatusBlocked)
}

// abortLeaseLoss is the lease-loss counterpart to block: the engine no
// longer owns this task — the lease was lost outright, or a renewal
// succeeded by silently re-taking a lease the reaper already freed while
// the task's status moved out from under the engine (spec: suspend gap) —
// so unlike block, it must never write task status; the reaper or the user
// who re-readied the task owns the outcome now. It still snapshots
// whatever the in-flight stage left dirty, exactly like block's
// backup-on-block, so lease loss never silently drops uncommitted work:
// commit any stray worktree changes, then force-update backup/<id> from
// mads/<id>. dir is empty for non-isolated runs or once the task's branch
// has already merged into the primary, in which case there is nothing left
// to snapshot. Best-effort throughout — a snapshot that fails just means a
// worse salvage story, never a stuck abort.
func (e *engine) abortLeaseLoss(ctx context.Context, taskID, taskTitle, dir string) {
	if e.wt == nil {
		return
	}
	if dir != "" {
		if err := e.wt.commitStray(ctx, dir, contracts.Task{ID: taskID, Title: taskTitle}); err != nil {
			log.Printf("engine: lease lost, snapshot task %s: %v", taskID, err)
		}
	}
	e.wt.backupBranch(ctx, taskID)
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
