package engine

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
)

// evLog builds an event history for progressOf, assigning seqs in order.
type evLog struct{ evs []contracts.Envelope }

func (l *evLog) add(t contracts.EventType, task string, payload map[string]any) int64 {
	raw, _ := json.Marshal(payload)
	seq := int64(len(l.evs) + 1)
	l.evs = append(l.evs, contracts.Envelope{Seq: seq, Type: t, TaskID: task, Payload: raw})
	return seq
}

func (l *evLog) run(id, tpl string, version int) int64 {
	p := map[string]any{"run_id": id, "template": tpl}
	if version > 0 {
		p["template_version"] = version
	}
	return l.add(contracts.PipelineStarted, "", p)
}

func (l *evLog) stage(t contracts.EventType, run int64, task, stage string, extra map[string]any) {
	p := map[string]any{"stage": stage, "run_seq": run}
	for k, v := range extra {
		p[k] = v
	}
	l.add(t, task, p)
}

func TestProgressOfMidStage(t *testing.T) {
	var l evLog
	r := l.run("run_a", "tpl", 3)
	other := l.run("run_b", "tpl", 3) // an overlapping run, other tasks
	l.stage(contracts.StageStarted, r, "t1", "executor", map[string]any{"attempt": 1})
	l.stage(contracts.StageStarted, other, "t2", "executor", map[string]any{"attempt": 1})
	l.stage(contracts.StageAttemptRef, r, "t1", "executor", map[string]any{"attempt": 1, "ref": "ref-1"})
	l.stage(contracts.StageCompleted, r, "t1", "executor", nil)
	l.stage(contracts.StageStarted, r, "t1", "code_review", map[string]any{"attempt": 1})
	l.stage(contracts.StageFailed, r, "t1", "code_review", map[string]any{"reason": "missing tests"})
	l.stage(contracts.StageStarted, r, "t1", "executor", map[string]any{"attempt": 2})
	l.stage(contracts.StageAttemptRef, r, "t1", "executor", map[string]any{"attempt": 2, "ref": "ref-2"})

	got := progressOf(l.evs, "t1")
	want := &progress{
		RunID: "run_a", RunSeq: r, Template: "tpl", TemplateVersion: 3,
		Stage: "executor", Attempt: 2, Ref: "ref-2",
		Last: "code_review", LastOutcome: "failed", Feedback: "missing tests",
		Completed: []string{"executor"},
		Loops:     map[string]int{"code_review": 1},
		Attempts:  map[string]int{"executor": 2, "code_review": 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("progressOf:\n got %+v\nwant %+v", got, want)
	}
}

func TestProgressOfNoUsableRecord(t *testing.T) {
	if got := progressOf(nil, "t1"); got != nil {
		t.Errorf("no events: %+v, want nil", got)
	}
	var l evLog
	l.run("run_a", "tpl", 1)
	// Legacy events: no run_seq.
	l.add(contracts.StageStarted, "t1", map[string]any{"stage": "executor"})
	if got := progressOf(l.evs, "t1"); got != nil {
		t.Errorf("legacy events: %+v, want nil (restart)", got)
	}
}

func TestProgressOfBetweenStagesAndGateNesting(t *testing.T) {
	var l evLog
	r := l.run("run_a", "tpl", 1)
	l.stage(contracts.StageStarted, r, "t1", "executor", map[string]any{"attempt": 1})
	l.stage(contracts.StageCompleted, r, "t1", "executor", nil)
	l.stage(contracts.StageStarted, r, "t1", "merge", map[string]any{"attempt": 1})
	l.stage(contracts.StageStarted, r, "t1", "verify", nil) // the land attempt's gate
	l.stage(contracts.StageCompleted, r, "t1", "verify", nil)
	got := progressOf(l.evs, "t1")
	if got.Stage != "merge" || got.Attempt != 1 {
		t.Errorf("in flight = %s/%d, want merge/1 (the gate nests inside it)", got.Stage, got.Attempt)
	}

	l.stage(contracts.StageCompleted, r, "t1", "merge", nil)
	got = progressOf(l.evs, "t1")
	if got.Stage != "" || got.Last != "merge" || got.LastOutcome != "completed" {
		t.Errorf("after merge: %+v, want between stages after merge", got)
	}
}

// A gate failure abandons the merge stage that was open around it; the next
// loop-back's stage is what's in flight.
func TestProgressOfFailureClosesOpenStages(t *testing.T) {
	var l evLog
	r := l.run("run_a", "tpl", 1)
	l.stage(contracts.StageStarted, r, "t1", "merge", map[string]any{"attempt": 1})
	l.stage(contracts.StageStarted, r, "t1", "verify", nil)
	l.stage(contracts.StageFailed, r, "t1", "verify", map[string]any{"reason": "red"})
	l.stage(contracts.StageStarted, r, "t1", "executor", map[string]any{"attempt": 2})
	got := progressOf(l.evs, "t1")
	if got.Stage != "executor" || got.Attempt != 2 || got.Loops["verify"] != 1 {
		t.Errorf("got %+v, want executor/2 in flight and one verify loop", got)
	}
}

// A run that resumed the task chains to the run it resumed from: loop
// counts carry over, and the in-flight attempt's ref falls back to the
// earlier run's when the resumed call hadn't re-announced it.
func TestProgressOfChainsResumes(t *testing.T) {
	var l evLog
	r1 := l.run("run_a", "tpl", 1)
	l.stage(contracts.StageStarted, r1, "t1", "executor", map[string]any{"attempt": 1})
	l.stage(contracts.StageCompleted, r1, "t1", "executor", nil)
	l.stage(contracts.StageStarted, r1, "t1", "review", map[string]any{"attempt": 1})
	l.stage(contracts.StageFailed, r1, "t1", "review", map[string]any{"reason": "nope"})
	l.stage(contracts.StageStarted, r1, "t1", "executor", map[string]any{"attempt": 2})
	l.stage(contracts.StageAttemptRef, r1, "t1", "executor", map[string]any{"attempt": 2, "ref": "ref-2"})
	r2 := l.run("run_b", "tpl", 1)
	l.add(contracts.StagePickedUp, "t1", map[string]any{"run_seq": r2, "mode": "resume", "resumed_from_run_seq": r1, "from_stage": "executor"})
	l.stage(contracts.StageStarted, r2, "t1", "executor", map[string]any{"attempt": 2, "resumed": true})

	got := progressOf(l.evs, "t1")
	if got.RunSeq != r2 || got.RunID != "run_b" {
		t.Errorf("last run = %d/%s, want %d/run_b", got.RunSeq, got.RunID, r2)
	}
	if got.Stage != "executor" || got.Attempt != 2 || got.Ref != "ref-2" {
		t.Errorf("in flight = %s/%d/%q, want executor/2/ref-2", got.Stage, got.Attempt, got.Ref)
	}
	if got.Loops["review"] != 1 || got.Feedback != "nope" {
		t.Errorf("loops/feedback = %v/%q, want carried from run_a", got.Loops, got.Feedback)
	}
	if !reflect.DeepEqual(got.Completed, []string{"executor"}) {
		t.Errorf("completed = %v", got.Completed)
	}
}

const pickupTpl = `name: tpl
stages:
  - name: executor
    agent: executor
  - name: review
    agent: reviewer
    on_fail: { loop_back: executor, max_loops: 1 }
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: make test
  - name: docs
    agent: executor
`

func TestPlanPickup(t *testing.T) {
	tpl, verr := template.Check([]byte(pickupTpl), func(string) bool { return true })
	if verr != nil {
		t.Fatal(verr)
	}
	base := func() *progress {
		return &progress{RunSeq: 9, Template: "tpl", TemplateVersion: 2, Loops: map[string]int{}, Attempts: map[string]int{}}
	}
	cases := []struct {
		name    string
		rec     func() *progress
		version int
		want    pickupPlan
	}{
		{"no record", func() *progress { return nil }, 2, pickupPlan{mode: pickupRestart}},
		{"other template", func() *progress { r := base(); r.Template = "other"; r.Stage = "review"; return r }, 2,
			pickupPlan{mode: pickupRestart}},
		{"edited template", func() *progress { r := base(); r.Stage = "review"; return r }, 3, pickupPlan{mode: pickupRestart}},
		{"unknown version", func() *progress { r := base(); r.TemplateVersion = 0; r.Stage = "review"; return r }, 0,
			pickupPlan{mode: pickupRestart}},
		{"in flight", func() *progress { r := base(); r.Stage, r.Attempt, r.Ref = "review", 2, "ref"; return r }, 2,
			pickupPlan{mode: pickupResume, start: 1, inFlight: true, attempt: 2, ref: "ref"}},
		{"after completed", func() *progress { r := base(); r.Last, r.LastOutcome = "executor", "completed"; return r }, 2,
			pickupPlan{mode: pickupResume, start: 1}},
		{"after merge skips the gate", func() *progress { r := base(); r.Last, r.LastOutcome = "merge", "completed"; return r }, 2,
			pickupPlan{mode: pickupResume, start: 4, merged: true}},
		{"after a failure with loops left", func() *progress {
			r := base()
			r.Last, r.LastOutcome, r.Feedback = "review", "failed", "fix it"
			r.Loops["review"] = 1
			return r
		}, 2, pickupPlan{mode: pickupResume, start: 0, onLoop: true, feedback: "fix it"}},
		{"after a failure with no loops left", func() *progress {
			r := base()
			r.Last, r.LastOutcome = "review", "failed"
			r.Loops["review"] = 2
			return r
		}, 2, pickupPlan{mode: pickupResume, start: 1}},
		{"in flight past merge", func() *progress { r := base(); r.Stage, r.Attempt = "docs", 1; return r }, 2,
			pickupPlan{mode: pickupResume, start: 4, merged: true, inFlight: true, attempt: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := c.rec()
			got := planPickup(rec, tpl, c.version, true)
			c.want.rec = rec
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}
