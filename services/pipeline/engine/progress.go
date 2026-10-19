package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
)

// Picking up an Interrupted task (docs/PIPELINE-LEASES-DESIGN.md B2).
//
// An Interrupted task is in_progress with no live lease: work started and
// nobody is doing it now. It is a scheduler concept, derived and never
// stored. A run that claims one continues the task's progress record —
// rebuilt from the events earlier runs left behind (progressOf) — instead of
// starting from scratch.
//
// Events this relies on, all for the task (Envelope.TaskID) unless noted:
//
//   - pipeline.started (session-wide): {run_id, template, template_version}.
//     Its seq is the run's run_seq.
//   - stage.started / stage.completed / stage.failed / stage.skipped /
//     stage.retried / stage.attempt_ref: each carries run_seq (the
//     pipeline.started seq of the run that wrote it) so it is attributed to
//     its run exactly, even when two runs overlap on one session.
//     stage.started also carries attempt; stage.failed carries reason;
//     stage.retried carries stage_attempt; stage.attempt_ref carries
//     {attempt, ref}.
//   - stage.picked_up: written when a run picks an Interrupted task up (see
//     contracts.StagePickedUp for the payload). A "resume" pickup chains the
//     record to the run it resumed from, so a task interrupted twice still
//     knows its loop counts and attempt numbers.
//
// Events from before run_seq was added carry none: such a task has no usable
// record and restarts from the first stage, keeping its worktree.

// progress is one Interrupted task's progress record: where its last run
// left it.
type progress struct {
	RunID           string // the last run's id ("" if it had none)
	RunSeq          int64  // the last run's pipeline.started seq
	Template        string // the last run's template name
	TemplateVersion int    // and registry version; 0 when unknown

	// Stage is the stage that was in flight when the run stopped (started,
	// never finished), or "" when it stopped between stages. Attempt is that
	// stage's last attempt number, and Ref the provider session ref that
	// attempt recorded ("" if none).
	Stage   string
	Attempt int
	Ref     string

	// Between stages: the last stage that finished, and how.
	Last        string
	LastOutcome string // "completed", "skipped" or "failed"

	// Feedback is the most recent stage failure's reason: what the engine
	// was feeding the next agent attempt.
	Feedback  string
	Completed []string       // stages completed, in order of completion (deduplicated)
	Loops     map[string]int // stage -> stage.failed count (on_fail loop-backs taken)
	Attempts  map[string]int // stage -> highest attempt number seen
}

// pickupEventTypes is every event type progressOf reads.
var pickupEventTypes = []string{
	string(contracts.PipelineStarted),
	string(contracts.StageStarted), string(contracts.StageCompleted),
	string(contracts.StageFailed), string(contracts.StageSkipped),
	string(contracts.StageRetried), string(contracts.StageAttemptRef),
	string(contracts.StagePickedUp),
}

// stagePayload is the union of the stage event payload fields progressOf
// reads.
type stagePayload struct {
	Stage        string `json:"stage"`
	Attempt      int    `json:"attempt"`
	StageAttempt int    `json:"stage_attempt"`
	RunSeq       int64  `json:"run_seq"`
	Reason       string `json:"reason"`
	Ref          string `json:"ref"`
	Mode         string `json:"mode"`
	FromRunSeq   int64  `json:"resumed_from_run_seq"`
}

type runInfo struct {
	RunID           string `json:"run_id"`
	Template        string `json:"template"`
	TemplateVersion int    `json:"template_version"`
}

// progressOf rebuilds taskID's progress record from a session's events
// (any order; pickupEventTypes is enough). nil means no usable record: the
// task never ran a stage, or its last run's events predate run_seq.
func progressOf(evs []contracts.Envelope, taskID string) *progress {
	sorted := append([]contracts.Envelope(nil), evs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	runs := map[int64]runInfo{}
	type taskEv struct {
		typ contracts.EventType
		p   stagePayload
	}
	var mine []taskEv
	var lastRun int64
	haveLast := false
	for _, ev := range sorted {
		if ev.Type == contracts.PipelineStarted {
			var ri runInfo
			_ = json.Unmarshal(ev.Payload, &ri)
			runs[ev.Seq] = ri
			continue
		}
		if ev.TaskID != taskID {
			continue
		}
		var p stagePayload
		_ = json.Unmarshal(ev.Payload, &p)
		mine = append(mine, taskEv{ev.Type, p})
		switch ev.Type {
		case contracts.StageStarted, contracts.StageCompleted, contracts.StageFailed, contracts.StageSkipped:
			lastRun, haveLast = p.RunSeq, true
		}
	}
	if !haveLast || lastRun <= 0 {
		return nil
	}

	var build func(run int64, depth int) *progress
	build = func(run int64, depth int) *progress {
		ri, ok := runs[run]
		if !ok {
			return nil
		}
		rec := &progress{
			RunID: ri.RunID, RunSeq: run, Template: ri.Template, TemplateVersion: ri.TemplateVersion,
			Loops: map[string]int{}, Attempts: map[string]int{},
		}
		var base *progress
		for _, te := range mine {
			if te.typ == contracts.StagePickedUp && te.p.RunSeq == run && te.p.Mode == pickupResume &&
				te.p.FromRunSeq > 0 && te.p.FromRunSeq < run && depth < 32 {
				base = build(te.p.FromRunSeq, depth+1)
			}
		}
		completed := map[string]bool{}
		if base != nil {
			for k, v := range base.Loops {
				rec.Loops[k] = v
			}
			for k, v := range base.Attempts {
				rec.Attempts[k] = v
			}
			rec.Feedback = base.Feedback
			for _, s := range base.Completed {
				completed[s] = true
				rec.Completed = append(rec.Completed, s)
			}
		}
		bump := func(stage string, n int) {
			if n > rec.Attempts[stage] {
				rec.Attempts[stage] = n
			}
		}
		open := map[string]int{} // stage -> order it was opened in
		order := 0
		refs := map[string]string{} // stage|attempt -> ref
		for _, te := range mine {
			if te.p.RunSeq != run {
				continue
			}
			st := te.p.Stage
			switch te.typ {
			case contracts.StageStarted:
				n := te.p.Attempt
				if n <= 0 {
					n = rec.Attempts[st] + 1
				}
				bump(st, n)
				if _, ok := open[st]; !ok {
					order++
					open[st] = order
				}
			case contracts.StageCompleted:
				delete(open, st)
				rec.Last, rec.LastOutcome = st, "completed"
				if !completed[st] {
					completed[st] = true
					rec.Completed = append(rec.Completed, st)
				}
			case contracts.StageSkipped:
				rec.Last, rec.LastOutcome = st, "skipped"
			case contracts.StageFailed:
				// A failure ends the attempt, including a land attempt
				// whose gate failed while the merge stage was open
				// around it: the engine moves on to on_fail.
				clear(open)
				rec.Loops[st]++
				rec.Last, rec.LastOutcome = st, "failed"
				rec.Feedback = te.p.Reason
			case contracts.StageRetried:
				bump(st, te.p.StageAttempt+1)
			case contracts.StageAttemptRef:
				bump(st, te.p.Attempt)
				if te.p.Ref != "" {
					refs[fmt.Sprintf("%s|%d", st, te.p.Attempt)] = te.p.Ref
				}
			}
		}
		// The earliest stage still open: a merge stage's land attempt opens
		// the gate stage inside it, so both can be open at once.
		first := 0
		for st, o := range open {
			if first == 0 || o < first {
				rec.Stage, first = st, o
			}
		}
		if rec.Stage != "" {
			rec.Attempt = rec.Attempts[rec.Stage]
			rec.Ref = refs[fmt.Sprintf("%s|%d", rec.Stage, rec.Attempt)]
			if rec.Ref == "" && base != nil && base.Stage == rec.Stage && base.Attempt == rec.Attempt {
				rec.Ref = base.Ref // resumed in this run, interrupted before it re-announced its ref
			}
		}
		return rec
	}
	return build(lastRun, 0)
}

// Pickup modes, as stage.picked_up's mode says them.
const (
	pickupResume  = "resume"  // same template and version: continue where it stopped
	pickupRestart = "restart" // anything else: first stage, worktree kept
)

// pickupPlan is how a run continues one Interrupted task.
type pickupPlan struct {
	mode string
	rec  *progress // nil: no usable record

	start    int    // stage index to continue at
	merged   bool   // the task's branch already landed (start is past the merge stage)
	onLoop   bool   // start was reached by an on_fail loop-back
	feedback string // loop-back feedback in effect for start

	// Set when start is the stage that was in flight: it is continued, not
	// entered afresh (its when-guard already passed).
	inFlight bool
	attempt  int    // the in-flight attempt
	ref      string // its provider session ref, if it recorded one
}

// planPickup decides how to continue an Interrupted task under tpl (version
// tplVersion): resume exactly when the record is from the same template
// name and the same, known, version; otherwise restart at the first stage.
func planPickup(rec *progress, tpl *template.Template, tplVersion int, isolated bool) pickupPlan {
	restart := pickupPlan{mode: pickupRestart, rec: rec}
	if rec == nil || tpl == nil || rec.Template != tpl.Name || tplVersion <= 0 || rec.TemplateVersion != tplVersion {
		return restart
	}
	stages := tpl.Stages
	idx := func(name string) int {
		for i, st := range stages {
			if st.Name == name {
				return i
			}
		}
		return -1
	}
	plan := pickupPlan{mode: pickupResume, rec: rec, feedback: rec.Feedback}
	switch {
	case rec.Stage != "":
		i := idx(rec.Stage)
		if i < 0 {
			return restart
		}
		plan.start, plan.inFlight, plan.attempt, plan.ref = i, true, rec.Attempt, rec.Ref
	case rec.Last != "":
		i := idx(rec.Last)
		if i < 0 {
			return restart
		}
		switch rec.LastOutcome {
		case "failed":
			// The on_fail decision the run was about to act on: loop back
			// while loops remain, else run the failed stage again.
			if of := stages[i].OnFail; of != nil && of.LoopBack != "" && rec.Loops[rec.Last] <= of.MaxLoops {
				if j := idx(of.LoopBack); j >= 0 {
					plan.start, plan.onLoop = j, true
					break
				}
			}
			plan.start = i
		default:
			plan.start = i + 1
			if isolated && isMergeStage(stages[i]) {
				if gidx, ok := gateStage(stages); ok && gidx >= plan.start {
					plan.start = gidx + 1 // the land attempt already ran the gate
				}
			}
		}
	}
	if isolated {
		for i := 0; i < plan.start && i < len(stages); i++ {
			if isMergeStage(stages[i]) {
				plan.merged = true
			}
		}
	}
	return plan
}

// completedBefore names the stages the plan skips that the record shows
// completed, in template order — stage.picked_up's completed_stages.
func (p pickupPlan) completedBefore(stages []template.Stage) []string {
	if p.rec == nil || p.mode != pickupResume {
		return nil
	}
	done := map[string]bool{}
	for _, s := range p.rec.Completed {
		done[s] = true
	}
	var out []string
	for i := 0; i < p.start && i < len(stages); i++ {
		if done[stages[i].Name] {
			out = append(out, stages[i].Name)
		}
	}
	return out
}

// loadProgress pages through the session's events and rebuilds taskID's
// progress record. Best-effort: a read failure is no record (restart).
func (e *engine) loadProgress(ctx context.Context, taskID string) *progress {
	var all []contracts.Envelope
	var since int64
	for {
		batch, err := e.cfg.Data.Events(ctx, since, e.cfg.SessionID, pickupEventTypes)
		if err != nil {
			return nil
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		since = batch[len(batch)-1].Seq
	}
	return progressOf(all, taskID)
}

// interruptedTail is the end of an interrupted attempt's captured output, or
// "" when none was captured.
func (e *engine) interruptedTail(taskID string, rec *progress, stage string, attempt int) string {
	if e.cfg.MadsHome == "" || rec == nil {
		return ""
	}
	raw, err := os.ReadFile(outputLogPath(e.cfg.MadsHome, e.cfg.SessionID, rec.RunSeq, taskID, stage, attempt))
	if err != nil {
		return ""
	}
	return trim(string(raw), 3000)
}

// resumingNote is the brief a fresh attempt gets when it takes over an
// interrupted one: say so, and pass on what the interrupted attempt said.
func resumingNote(attempt int, tail string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are resuming interrupted work: attempt %d at this stage was interrupted before it finished, and its changes are still in the working tree. Inspect them and continue from there rather than starting over.\n", attempt)
	if tail != "" {
		fmt.Fprintf(&b, "\nThe interrupted attempt's output ended with:\n%s\n", tail)
	}
	return b.String()
}

// priorWorkNote is the brief the first agent stage of a restarted task gets.
const priorWorkNote = "An earlier pipeline run worked on this task and was interrupted; its work is still in this working tree (committed on the task branch). Inspect it before starting, and build on it where it fits.\n"

// resumePrompt continues an agent session that was interrupted.
const resumePrompt = "You were interrupted; continue where you left off."
