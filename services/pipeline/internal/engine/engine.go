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
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
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
		tasks, err := e.cfg.Data.Tasks(ctx, e.cfg.SessionID, "")
		if err != nil {
			if len(running) > 0 {
				continue // drain in-flight tasks before giving up
			}
			return nil, err
		}
		byID := map[string]contracts.Task{}
		for _, t := range tasks {
			byID[t.ID] = t
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
		r := <-results
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
	if err := e.cfg.Data.Claim(ctx, task.ID, 10*time.Minute); err != nil {
		return task.Status // someone else holds it; not ours this run
	}
	defer func() { _ = e.cfg.Data.Release(context.WithoutCancel(ctx), task.ID) }()

	if !e.setStatus(ctx, &task, contracts.StatusInProgress) {
		return task.Status
	}

	// Where stages run: the primary repo, or (isolated) the task's own
	// worktree until the merge stage moves work back to the primary.
	run := taskRun{dir: e.cfg.Cwd}
	if e.wt != nil {
		dir, branch, err := e.wt.create(ctx, task)
		if err != nil {
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
			output, err = e.runStage(ctx, st, task, feedback, run.dir)
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
		e.block(ctx, &task, st.Name, blockReason(trim(output, 2000)))
		return contracts.StatusBlocked
	}

	if e.wt != nil && run.merged {
		e.wt.remove(context.WithoutCancel(ctx), run.worktree, run.branch)
	}
	if !e.setStatus(ctx, &task, contracts.StatusDone) {
		return contracts.StatusBlocked
	}
	return contracts.StatusDone
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
