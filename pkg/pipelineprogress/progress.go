// Package pipelineprogress rebuilds an Interrupted task's progress record
// from a session's events and decides how a run picks it up. It is pure:
// the pipeline engine uses it to continue the task, and the Router uses the
// same functions to tell the Tasks tab's Plan what a run would do.
package pipelineprogress

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
)

// Picking up an Interrupted task (docs/PIPELINE-LEASES-DESIGN.md B2).
//
// An Interrupted task is in_progress with no live lease, moved to
// in_progress by the pipeline (InProgressLog): pipeline work started and
// nobody is doing it now. It is a scheduler concept, derived and never
// stored. A run that claims one continues the task's progress record —
// rebuilt from the events earlier runs left behind (Of) — instead of
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

// Progress is one Interrupted task's progress record: where its last run
// left it.
type Progress struct {
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

// InProgressLog remembers, per task, who made its most recent
// task.status_changed into in_progress (the envelope's actor). Interrupted
// is a pipeline-only concept: only a task the pipeline itself moved to
// in_progress (the engine's setStatus, under its actor, "pipeline") can be
// Interrupted. A task a person, a session or the synthesizer set in
// progress is theirs, and no run picks it up. The most recent transition
// wins, so a pipeline-started task that someone later sets in progress
// again is theirs too. Fold is fed the session's task.status_changed
// events, any number of times, in seq order (the scheduler pages them
// incrementally).
type InProgressLog struct {
	by    map[string]string // task -> actor of its latest move into in_progress
	since int64             // the last seq folded
}

func (l *InProgressLog) Fold(evs []contracts.Envelope) {
	if l.by == nil {
		l.by = map[string]string{}
	}
	for _, ev := range evs {
		if ev.Seq > l.since {
			l.since = ev.Seq
		}
		if ev.Type != contracts.TaskStatusChanged || ev.TaskID == "" {
			continue
		}
		var p struct {
			To string `json:"to"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.To == string(contracts.StatusInProgress) {
			l.by[ev.TaskID] = ev.Actor
		}
	}
}

// Since is the last seq folded: where the next page of events starts.
func (l *InProgressLog) Since() int64 { return l.since }

// MovedBy reports whether actor made taskID's latest move into in_progress.
func (l *InProgressLog) MovedBy(taskID, actor string) bool {
	return l.by[taskID] == actor
}

// EventTypes is every event type Of reads.
var EventTypes = []string{
	string(contracts.PipelineStarted),
	string(contracts.StageStarted), string(contracts.StageCompleted),
	string(contracts.StageFailed), string(contracts.StageSkipped),
	string(contracts.StageRetried), string(contracts.StageAttemptRef),
	string(contracts.StagePickedUp),
}

// stagePayload is the union of the stage event payload fields Of
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

// Of rebuilds taskID's progress record from a session's events
// (any order; EventTypes is enough). nil means no usable record: the
// task never ran a stage, or its last run's events predate run_seq.
func Of(evs []contracts.Envelope, taskID string) *Progress {
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

	var build func(run int64, depth int) *Progress
	build = func(run int64, depth int) *Progress {
		ri, ok := runs[run]
		if !ok {
			return nil
		}
		rec := &Progress{
			RunID: ri.RunID, RunSeq: run, Template: ri.Template, TemplateVersion: ri.TemplateVersion,
			Loops: map[string]int{}, Attempts: map[string]int{},
		}
		var base *Progress
		for _, te := range mine {
			if te.typ == contracts.StagePickedUp && te.p.RunSeq == run && te.p.Mode == ModeResume &&
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
	ModeResume  = "resume"  // same template and version: continue where it stopped
	ModeRestart = "restart" // anything else: first stage, worktree kept
)

// Plan is how a run continues one Interrupted task.
type Plan struct {
	Mode string
	Rec  *Progress // nil: no usable record

	Start    int    // stage index to continue at
	Merged   bool   // the task's branch already landed (Start is past the merge stage)
	OnLoop   bool   // Start was reached by an on_fail loop-back
	Feedback string // loop-back feedback in effect for Start

	// Set when Start is the stage that was in flight: it is continued, not
	// entered afresh (its when-guard already passed).
	InFlight bool
	Attempt  int    // the in-flight attempt
	Ref      string // its provider session ref, if it recorded one
}

// PlanPickup decides how to continue an Interrupted task under tpl (version
// tplVersion): resume exactly when the record is from the same template
// name and the same, known, version; otherwise restart at the first stage.
func PlanPickup(rec *Progress, tpl *template.Template, tplVersion int, isolated bool) Plan {
	restart := Plan{Mode: ModeRestart, Rec: rec}
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
	plan := Plan{Mode: ModeResume, Rec: rec, Feedback: rec.Feedback}
	switch {
	case rec.Stage != "":
		i := idx(rec.Stage)
		if i < 0 {
			return restart
		}
		plan.Start, plan.InFlight, plan.Attempt, plan.Ref = i, true, rec.Attempt, rec.Ref
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
					plan.Start, plan.OnLoop = j, true
					break
				}
			}
			plan.Start = i
		default:
			plan.Start = i + 1
			if isolated && IsMergeStage(stages[i]) {
				if gidx, ok := GateStage(stages); ok && gidx >= plan.Start {
					plan.Start = gidx + 1 // the land attempt already ran the gate
				}
			}
		}
	}
	if isolated {
		for i := 0; i < plan.Start && i < len(stages); i++ {
			if IsMergeStage(stages[i]) {
				plan.Merged = true
			}
		}
	}
	return plan
}

// CompletedBefore names the stages the plan skips that the record shows
// completed, in template order — stage.picked_up's completed_stages.
func (p Plan) CompletedBefore(stages []template.Stage) []string {
	if p.Rec == nil || p.Mode != ModeResume {
		return nil
	}
	done := map[string]bool{}
	for _, s := range p.Rec.Completed {
		done[s] = true
	}
	var out []string
	for i := 0; i < p.Start && i < len(stages); i++ {
		if done[stages[i].Name] {
			out = append(out, stages[i].Name)
		}
	}
	return out
}

// IsMergeStage recognizes the template's merge directive; the engine
// supplies the branch and serialization (spec §6).
func IsMergeStage(st template.Stage) bool {
	return strings.HasPrefix(strings.TrimSpace(st.Command), "git merge")
}

// GateStage finds the stage literally named "verify" with a non-empty
// command: the land queue's gate, which the merge stage's land attempt runs
// post-rebase (see the engine's gateStage notes).
func GateStage(stages []template.Stage) (idx int, ok bool) {
	for i, st := range stages {
		if st.Name == "verify" && st.Command != "" {
			return i, true
		}
	}
	return 0, false
}
