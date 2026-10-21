// Package delegates is the one place a session's delegate lifecycle is
// decided (docs/DELEGATES-DESIGN.md): the session runner feeds it the
// harness's evidence — task lifecycle lines, the background-task roster, the
// main agent's turn ends, the harness process ending — and publishes the
// records it returns. Every other surface (the rail, the conversation, the
// tool-status sweep) reads the outcome instead of re-deriving it from turn
// edges, which is where mads's subagent bugs came from.
//
// The rules:
//
//   - A delegate is identified by the harness's id (Claude Code's task_id),
//     never by the spawning tool_use id: a resumed delegate reports a new
//     one (the SendMessage call's) while its own events keep the original.
//   - The parent's turn ending does not end a backgrounded delegate. It does
//     end a foreground one still marked running: the parent waits on those,
//     so a turn that ended without the delegate's report means it was cut
//     short (an interrupt).
//   - A harness report always wins. States mads concludes on its own
//     (roster expiry, process exit, a runner restart) are marked Inferred,
//     and a later report overrides them.
//   - Leaving the roster is not an ending: Claude Code drops a task from its
//     roster just before reporting how it ended. Only a roster absence that
//     outlasts Grace with no report concludes "stopped".
//   - A finished delegate the harness runs again under the same id re-enters
//     running (Runs counts the runs). The harness counts each run's tools and
//     time from zero; the record's are totals across runs, and the runs
//     before the latest are kept apart (Prior*) for the live run to add to.
package delegates

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// Grace is how long a backgrounded delegate may be missing from the roster
// without a report before mads concludes it was stopped. The report normally
// follows within milliseconds (the next lines on the wire); this only has to
// outlast a busy pump, not a slow harness.
const Grace = 5 * time.Second

// UsageEvery is how often a running delegate's record takes its latest API
// call's usage. A busy subagent makes a call every second or two, and each
// record write is an event on the log; the latest is held, never dropped
// (FlushUsage).
const UsageEvery = 5 * time.Second

// Inferred reasons, for Delegate.Reason when mads concluded the state.
const (
	ReasonRoster        = "roster"         // left the roster, no report within Grace
	ReasonTurnEnd       = "turn_end"       // foreground, still running when its parent's turn ended
	ReasonProcessExit   = "process_exit"   // the harness process ended under it
	ReasonRunnerRestart = "runner_restart" // found running when a runner started
	ReasonNoEnding      = "no_ending"      // imported: its harness's history records no ending
)

type entry struct {
	d contracts.Delegate
	// leftAt is when a backgrounded delegate dropped out of the roster while
	// running; zero when it hasn't (or has come back).
	leftAt time.Time
	// usageAt is when the record last took a usage report; usageHeld that a
	// later one is on the record but hasn't been returned yet.
	usageAt   time.Time
	usageHeld bool
	// runToolUses and runDurationMS are the latest run's counters as the
	// harness last reported them — on a progress line or at its ending.
	// Held here, not on the record (progress ticks don't write it), so a
	// run that ended without a report still counts when the next begins.
	runToolUses, runDurationMS int
}

// Tracker holds one session's delegates. Safe for concurrent use.
type Tracker struct {
	mu        sync.Mutex
	sessionID string
	grace     time.Duration
	byID      map[string]*entry
	// unseen holds the ending a harness reported for a task the tracker
	// never saw start, until Inspect finds the delegate in the harness's
	// store. A harness reports every background task it leaves behind
	// across a restart, shell commands included; only one with a store
	// entry is a delegate.
	unseen map[string]contracts.SubagentTask
	// defaults is the harness's capability set from its provider matrix
	// (SetDefaults).
	defaults CapabilityDefaults
}

// Observation is what the harness's own store says about one delegate at
// run time (provider.DelegateInspector, mapped by the runner). Zero values
// mean it didn't say.
type Observation struct {
	TranscriptPath string
	Type           string
	Depth          int
	WorktreePath   string
	Background     bool
	// Model, Branch and Usage are what its latest recorded line says.
	Model  string
	Branch string
	Usage  *provider.Usage
}

// CapabilityDefaults is a harness's delegate capabilities before any
// evidence: its provider matrix's answer, and whether a delegate whose
// transcript is found could be continued as a forked session (the harness can
// fork-on-resume). Transcript itself is never a default: it is only ever true
// on evidence (Inspect, FromHistory).
type CapabilityDefaults struct {
	Caps        contracts.DelegateCapabilities
	Continuable bool
}

// Defaults maps a provider's matrix onto its delegates' capability defaults
// (docs/DELEGATES-DESIGN.md C2b/C3): native steer is direct, emulated is a
// relay through the parent. StopsWithParent is mads's own choice, not the
// harness's: claude-code stops its background tasks with an interrupt unless
// perTaskStopAffordance is set, and mads leaves it unset (cascading stops) —
// and a harness whose delegates are foreground-only stops them with its turn
// by construction.
func Defaults(m provider.Matrix) CapabilityDefaults {
	caps := contracts.DelegateCapabilities{
		Steer:           contracts.DelegateSteerNone,
		Stop:            m[provider.BehaviorDelegateStop].Works(),
		StopsWithParent: true,
	}
	switch m[provider.BehaviorDelegateSteer] {
	case provider.Native:
		caps.Steer = contracts.DelegateSteerDirect
	case provider.Emulated:
		caps.Steer = contracts.DelegateSteerRelay
	}
	return CapabilityDefaults{Caps: caps, Continuable: m[provider.BehaviorBranching].Works()}
}

// SetDefaults sets the capability defaults every new delegate starts from.
func (t *Tracker) SetDefaults(d CapabilityDefaults) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.defaults = d
}

// Inspect folds a run-time observation into the delegate's record — its
// transcript, worktree, and what the store adds to its identity — and
// re-resolves its capabilities. Returns the record if it changed. An unknown
// delegate is ignored unless its ending was held back (unseen): finding it
// in the store is what makes it one.
func (t *Tracker) Inspect(harnessID string, o Observation) []contracts.Delegate {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, known := t.byID[harnessID]
	if !known {
		r, held := t.unseen[harnessID]
		if !held {
			return nil
		}
		delete(t.unseen, harnessID)
		now := time.Now()
		e = t.createLocked(r, now)
		t.endingLocked(e, r, now)
	}
	before := e.d
	if o.TranscriptPath != "" {
		e.d.TranscriptPath = o.TranscriptPath
	}
	if o.WorktreePath != "" {
		e.d.WorktreePath = o.WorktreePath
	}
	if e.d.Type == "" {
		e.d.Type = o.Type
	}
	if e.d.Depth == 0 {
		e.d.Depth = o.Depth
	}
	if o.Background {
		e.d.Backgrounded = true
	}
	if o.Branch != "" {
		e.d.Branch = o.Branch
	}
	// The live stream's usage is newer than the file's until the delegate
	// ends, when they agree: the file only fills a record that has none.
	if e.d.Usage == nil && o.Usage != nil {
		e.d.Model, e.d.Usage = o.Usage.Model, contractsUsage(o.Usage)
	}
	e.d.Capabilities = t.resolveLocked(e.d)
	if known && sameRecord(before, e.d) {
		return nil
	}
	return []contracts.Delegate{e.d}
}

// resolveLocked is the record's capability set: the defaults, corrected by
// the evidence on the record. Always a fresh value — records are compared by
// field, and a shared pointer would hide a change.
func (t *Tracker) resolveLocked(d contracts.Delegate) *contracts.DelegateCapabilities {
	return resolve(t.defaults, d)
}

// resolve is a record's capability set: the defaults, corrected by the
// evidence on the record.
func resolve(defaults CapabilityDefaults, d contracts.Delegate) *contracts.DelegateCapabilities {
	caps := defaults.Caps
	if caps.Steer == "" {
		caps.Steer = contracts.DelegateSteerNone
	}
	caps.Transcript = d.TranscriptPath != ""
	caps.Continuable = caps.Transcript && defaults.Continuable
	return &caps
}

// FromHistory is the record for a delegate mads never watched run — one
// found in an imported conversation's history — under the same rules as a
// live one: the harness's word for how it ended settles its state, and its
// capabilities are the defaults corrected by the evidence. A history with no
// ending records it orphaned, concluded by mads.
func FromHistory(sessionID string, r provider.DelegateRecord, defaults CapabilityDefaults) contracts.Delegate {
	d := contracts.Delegate{
		SessionID:       sessionID,
		HarnessID:       r.HarnessID,
		ToolUseID:       r.ToolUseID,
		Depth:           r.Depth,
		Type:            r.Type,
		Description:     r.Description,
		Backgrounded:    r.Background,
		Runs:            max(r.Runs, 1),
		StartedAt:       r.StartedAt.UTC(),
		TranscriptPath:  r.TranscriptPath,
		WorktreePath:    r.WorktreePath,
		Model:           r.Model,
		Branch:          r.Branch,
		Usage:           contractsUsage(r.Usage),
		TotalTokens:     r.TotalTokens,
		ToolUses:        r.ToolUses,
		DurationMS:      r.DurationMS,
		PriorToolUses:   r.PriorToolUses,
		PriorDurationMS: r.PriorDurationMS,
	}
	if r.Status == "" {
		d.State, d.Reason, d.Inferred = contracts.DelegateOrphaned, ReasonNoEnding, true
	} else {
		d.State, d.Reason = settle(r.Status)
		if !r.EndedAt.IsZero() {
			end := r.EndedAt.UTC()
			d.EndedAt = &end
		}
	}
	d.Capabilities = resolve(defaults, d)
	return d
}

// New returns an empty tracker for sessionID. grace ≤ 0 means Grace.
func New(sessionID string, grace time.Duration) *Tracker {
	if grace <= 0 {
		grace = Grace
	}
	return &Tracker{sessionID: sessionID, grace: grace, byID: map[string]*entry{}, unseen: map[string]contracts.SubagentTask{}}
}

// Seed loads the session's stored records — so a delegate resumed by a
// later harness process keeps its identity, anchor and run count — and
// returns the ones it had to correct: anything still running belonged to a
// harness process that is gone (a runner only seeds at start), so it becomes
// orphaned.
func (t *Tracker) Seed(stored []contracts.Delegate, now time.Time) []contracts.Delegate {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []contracts.Delegate
	for _, d := range stored {
		e := &entry{d: d}
		e.runToolUses = max(d.ToolUses-d.PriorToolUses, 0)
		e.runDurationMS = max(d.DurationMS-d.PriorDurationMS, 0)
		if e.d.Capabilities == nil {
			e.d.Capabilities = t.resolveLocked(e.d)
		}
		t.byID[d.HarnessID] = e
		if d.State == contracts.DelegateRunning {
			t.endLocked(e, contracts.DelegateOrphaned, ReasonRunnerRestart, true, now)
			out = append(out, e.d)
		}
	}
	return out
}

// Task folds one lifecycle report and returns the records it changed (at
// most one). Progress ticks change nothing on the record — live counters
// stay on the session.subagent_task stream; the tracker only keeps the
// latest for its run total — except to revive a delegate mads had wrongly
// concluded ended.
func (t *Tracker) Task(r contracts.SubagentTask, now time.Time) []contracts.Delegate {
	if r.TaskID == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e, known := t.byID[r.TaskID]
	before := contracts.Delegate{}
	if known {
		before = e.d
	}
	switch r.Phase {
	case "started":
		if !known {
			e = t.createLocked(r, now)
			break
		}
		t.fillLocked(e, r)
		if r.IsBackgrounded {
			e.d.Backgrounded = true
		}
		if e.d.State != contracts.DelegateRunning {
			t.reenterLocked(e, now)
		}
		e.leftAt = time.Time{}
	case "progress":
		if !known {
			e = t.createLocked(r, now)
			e.noteRun(r)
			break
		}
		if e.d.State != contracts.DelegateRunning && e.d.Inferred {
			switch e.d.Reason {
			case ReasonRunnerRestart, ReasonProcessExit:
				// The process it ran in is gone: this is a new run whose
				// start line came late, not the old one resuming.
				t.reenterLocked(e, now)
			default:
				e.d.State, e.d.Reason, e.d.Inferred, e.d.EndedAt = contracts.DelegateRunning, "", false, nil
			}
		}
		e.noteRun(r)
	case "updated", "notification":
		if r.Phase == "updated" && r.Status == "" {
			return nil // a patch without a status transition
		}
		if !known {
			// Held until Inspect finds it in the harness's store (unseen).
			if r.ToolUseID == "" {
				r.ToolUseID = t.unseen[r.TaskID].ToolUseID
			}
			t.unseen[r.TaskID] = r
			return nil
		}
		t.endingLocked(e, r, now)
	default:
		return nil
	}
	if known && sameRecord(before, e.d) {
		return nil
	}
	return []contracts.Delegate{e.d}
}

// Usage folds one of a running delegate's API calls — found by its spawning
// call, the id its own events are tagged with — into its record: the model
// it ran on and its context. Returns the record when it is due (the first
// report, a model change, or UsageEvery since the last); otherwise held is
// true and FlushUsage returns it later. A window the call doesn't report
// keeps the one already known for the same model.
func (t *Tracker) Usage(spawnToolUseID string, u *provider.Usage, now time.Time) (out []contracts.Delegate, held bool) {
	if spawnToolUseID == "" || u == nil {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var e *entry
	for _, c := range t.sortedLocked() {
		if c.d.ToolUseID == spawnToolUseID && (e == nil || c.d.State == contracts.DelegateRunning) {
			e = c
		}
	}
	if e == nil {
		return nil, false
	}
	next := contractsUsage(u)
	prev := e.d.Usage
	if next.ContextWindow == 0 && prev != nil && prev.Model == next.Model {
		next.ContextWindow = prev.ContextWindow
	}
	if prev != nil && *prev == *next {
		return nil, false
	}
	modelChanged := e.d.Model != next.Model
	e.d.Model, e.d.Usage = next.Model, next
	if !e.usageAt.IsZero() && !modelChanged && now.Sub(e.usageAt) < UsageEvery {
		e.usageHeld = true
		return nil, true
	}
	e.usageAt, e.usageHeld = now, false
	return []contracts.Delegate{e.d}, false
}

// FlushUsage returns every record holding a usage report Usage didn't
// return yet.
func (t *Tracker) FlushUsage(now time.Time) []contracts.Delegate {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []contracts.Delegate
	for _, e := range t.sortedLocked() {
		if e.usageHeld {
			e.usageAt, e.usageHeld = now, false
			out = append(out, e.d)
		}
	}
	return out
}

// Roster folds the harness's complete list of running background
// delegates. It never changes a record by itself — a running backgrounded
// delegate missing from it starts its Grace clock, and one that reappears
// stops it — so it returns nothing; Expire concludes. Ids it doesn't know
// are ignored: their task_started line follows.
func (t *Tracker) Roster(ids []string, now time.Time) {
	present := make(map[string]bool, len(ids))
	for _, id := range ids {
		present[id] = true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, e := range t.byID {
		if e.d.State != contracts.DelegateRunning || !e.d.Backgrounded {
			continue
		}
		switch {
		case present[id]:
			e.leftAt = time.Time{}
		case e.leftAt.IsZero():
			e.leftAt = now
		}
	}
}

// NextExpiry is when the earliest pending roster absence runs out, if any —
// when the caller should call Expire next.
func (t *Tracker) NextExpiry() (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var next time.Time
	for _, e := range t.byID {
		if e.leftAt.IsZero() || e.d.State != contracts.DelegateRunning {
			continue
		}
		if at := e.leftAt.Add(t.grace); next.IsZero() || at.Before(next) {
			next = at
		}
	}
	return next, !next.IsZero()
}

// Expire concludes every roster absence that has outlasted Grace with no
// report: stopped, inferred, ended when it left the roster.
func (t *Tracker) Expire(now time.Time) []contracts.Delegate {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []contracts.Delegate
	for _, e := range t.sortedLocked() {
		if e.leftAt.IsZero() || e.d.State != contracts.DelegateRunning || now.Sub(e.leftAt) < t.grace {
			continue
		}
		t.endLocked(e, contracts.DelegateStopped, ReasonRoster, true, e.leftAt)
		out = append(out, e.d)
	}
	return out
}

// TurnEnd folds the main agent's turn ending: a foreground delegate still
// running was cut short with its parent (see the package rules).
// Backgrounded delegates are untouched.
func (t *Tracker) TurnEnd(now time.Time) []contracts.Delegate {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []contracts.Delegate
	for _, e := range t.sortedLocked() {
		if e.d.State != contracts.DelegateRunning || e.d.Backgrounded {
			continue
		}
		t.endLocked(e, contracts.DelegateStopped, ReasonTurnEnd, true, now)
		out = append(out, e.d)
	}
	return out
}

// ProcessExit folds the harness process ending: every running delegate
// ran inside it (Claude Code's subagents are in-process), so each one stops.
func (t *Tracker) ProcessExit(now time.Time) []contracts.Delegate {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []contracts.Delegate
	for _, e := range t.sortedLocked() {
		if e.d.State != contracts.DelegateRunning {
			continue
		}
		t.endLocked(e, contracts.DelegateStopped, ReasonProcessExit, true, now)
		out = append(out, e.d)
	}
	return out
}

// LiveSpawn reports whether the delegate anchored at spawnToolUseID (the id
// its own events are tagged with) is running — the tool-status sweep's
// question at turn end.
func (t *Tracker) LiveSpawn(spawnToolUseID string) bool {
	if spawnToolUseID == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.byID {
		if e.d.ToolUseID == spawnToolUseID && e.d.State == contracts.DelegateRunning {
			return true
		}
	}
	return false
}

func (t *Tracker) createLocked(r contracts.SubagentTask, now time.Time) *entry {
	e := &entry{d: contracts.Delegate{
		SessionID: t.sessionID,
		HarnessID: r.TaskID,
		State:     contracts.DelegateRunning,
		Runs:      1,
		StartedAt: now,
	}}
	t.fillLocked(e, r)
	e.d.Backgrounded = r.IsBackgrounded
	e.d.Capabilities = t.resolveLocked(e.d)
	t.byID[r.TaskID] = e
	return e
}

// fillLocked takes descriptive fields the record doesn't have yet. First
// seen wins for the anchor; a re-entry's tool_use id is the resuming call's.
func (t *Tracker) fillLocked(e *entry, r contracts.SubagentTask) {
	if e.d.ToolUseID == "" {
		e.d.ToolUseID = r.ToolUseID
	}
	if e.d.Type == "" {
		e.d.Type = r.SubagentType
	}
	if e.d.Description == "" && r.Phase == "started" {
		e.d.Description = r.Description // a progress line's description is a step, not a name
	}
	if e.d.Depth == 0 {
		e.d.Depth = r.SpawnDepth
	}
}

// reenterLocked starts a finished delegate's next run: the run that ended
// joins the prior totals — here, once per run, so an ending reported twice
// (inferred, then the harness's late word) can't count it twice.
func (t *Tracker) reenterLocked(e *entry, now time.Time) {
	e.d.State, e.d.Reason, e.d.Inferred, e.d.EndedAt = contracts.DelegateRunning, "", false, nil
	e.d.Runs++
	e.d.StartedAt = now
	e.d.PriorToolUses += e.runToolUses
	e.d.PriorDurationMS += e.runDurationMS
	e.d.ToolUses, e.d.DurationMS = e.d.PriorToolUses, e.d.PriorDurationMS
	e.runToolUses, e.runDurationMS = 0, 0
}

// noteRun keeps a report's counters for the run it belongs to.
func (e *entry) noteRun(r contracts.SubagentTask) {
	if r.ToolUses != 0 || r.DurationMS != 0 {
		e.runToolUses, e.runDurationMS = r.ToolUses, r.DurationMS
	}
}

// endingLocked folds an updated/notification report into the record.
func (t *Tracker) endingLocked(e *entry, r contracts.SubagentTask, now time.Time) {
	if r.Phase == "notification" {
		if r.TotalTokens != 0 || r.ToolUses != 0 || r.DurationMS != 0 {
			e.d.TotalTokens = r.TotalTokens
			e.noteRun(r)
			e.setTotals()
		}
	}
	// An evidenced ending already stands; only an inferred one (or a
	// running delegate) takes this report's state.
	if e.d.State == contracts.DelegateRunning || e.d.Inferred {
		state, reason := settle(r.Status)
		t.endLocked(e, state, reason, false, now)
	}
}

// setTotals sets the record's totals: the runs before, plus this one as
// last reported. Assigned, never accumulated, so it is safe to repeat.
func (e *entry) setTotals() {
	e.d.ToolUses = e.d.PriorToolUses + e.runToolUses
	e.d.DurationMS = e.d.PriorDurationMS + e.runDurationMS
}

func (t *Tracker) endLocked(e *entry, state contracts.DelegateState, reason string, inferred bool, at time.Time) {
	e.d.State, e.d.Reason, e.d.Inferred = state, reason, inferred
	// An ending with no report still has the run's last progress counters.
	e.setTotals()
	ended := at
	e.d.EndedAt = &ended
	e.leftAt = time.Time{}
}

// sortedLocked is the entries in a stable order (start, then id), so every
// multi-record result is deterministic.
func (t *Tracker) sortedLocked() []*entry {
	out := make([]*entry, 0, len(t.byID))
	for _, e := range t.byID {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].d.StartedAt.Equal(out[j].d.StartedAt) {
			return out[i].d.StartedAt.Before(out[j].d.StartedAt)
		}
		return out[i].d.HarnessID < out[j].d.HarnessID
	})
	return out
}

// settle maps the harness's status word onto a terminal state, keeping the
// word as the reason. Claude Code reports "completed" / "failed" / "killed" /
// "stopped"; anything unrecognised is treated as a failure rather than
// silently passed as success — it surfaces, and the word says what it was.
func settle(status string) (contracts.DelegateState, string) {
	word := strings.ToLower(strings.TrimSpace(status))
	switch word {
	case "", "completed", "complete", "success", "succeeded", "done":
		if word == "" {
			word = "completed"
		}
		return contracts.DelegateCompleted, word
	case "killed", "stopped", "cancelled", "canceled", "interrupted", "aborted":
		return contracts.DelegateStopped, word
	default:
		return contracts.DelegateFailed, word
	}
}

// contractsUsage is the record's copy of one API call's accounting.
func contractsUsage(u *provider.Usage) *contracts.Usage {
	if u == nil {
		return nil
	}
	return &contracts.Usage{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		Model:                    u.Model,
		ContextTokens:            u.ContextTokens,
		ContextWindow:            u.ContextWindow,
	}
}

// sameRecord compares two records field for field — the times by instant,
// the capabilities and usage by value.
func sameRecord(a, b contracts.Delegate) bool {
	sameEnd := (a.EndedAt == nil && b.EndedAt == nil) ||
		(a.EndedAt != nil && b.EndedAt != nil && a.EndedAt.Equal(*b.EndedAt))
	sameCaps := (a.Capabilities == nil && b.Capabilities == nil) ||
		(a.Capabilities != nil && b.Capabilities != nil && *a.Capabilities == *b.Capabilities)
	sameUsage := (a.Usage == nil && b.Usage == nil) ||
		(a.Usage != nil && b.Usage != nil && *a.Usage == *b.Usage)
	if !sameEnd || !sameCaps || !sameUsage || !a.StartedAt.Equal(b.StartedAt) {
		return false
	}
	a.EndedAt, b.EndedAt = nil, nil
	a.Capabilities, b.Capabilities = nil, nil
	a.Usage, b.Usage = nil, nil
	a.StartedAt, b.StartedAt = time.Time{}, time.Time{}
	return a == b
}
