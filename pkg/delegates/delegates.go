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
//     running (Runs counts the runs).
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
}

// Tracker holds one session's delegates. Safe for concurrent use.
type Tracker struct {
	mu        sync.Mutex
	sessionID string
	grace     time.Duration
	byID      map[string]*entry
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
// re-resolves its capabilities. Returns the record if it changed; an unknown
// delegate is ignored.
func (t *Tracker) Inspect(harnessID string, o Observation) []contracts.Delegate {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.byID[harnessID]
	if !ok {
		return nil
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
	e.d.Capabilities = t.resolveLocked(e.d)
	if sameRecord(before, e.d) {
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
		SessionID:      sessionID,
		HarnessID:      r.HarnessID,
		ToolUseID:      r.ToolUseID,
		Depth:          r.Depth,
		Type:           r.Type,
		Description:    r.Description,
		Backgrounded:   r.Background,
		Runs:           max(r.Runs, 1),
		StartedAt:      r.StartedAt.UTC(),
		TranscriptPath: r.TranscriptPath,
		WorktreePath:   r.WorktreePath,
		TotalTokens:    r.TotalTokens,
		ToolUses:       r.ToolUses,
		DurationMS:     r.DurationMS,
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
	return &Tracker{sessionID: sessionID, grace: grace, byID: map[string]*entry{}}
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
// stay on the session.subagent_task stream — except to revive a delegate
// mads had wrongly concluded ended.
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
			e.d.State, e.d.Reason, e.d.Inferred, e.d.EndedAt = contracts.DelegateRunning, "", false, nil
			e.d.Runs++
			e.d.StartedAt = now
		}
		e.leftAt = time.Time{}
	case "progress":
		if !known {
			e = t.createLocked(r, now)
			break
		}
		if e.d.State != contracts.DelegateRunning && e.d.Inferred {
			e.d.State, e.d.Reason, e.d.Inferred, e.d.EndedAt = contracts.DelegateRunning, "", false, nil
		}
	case "updated", "notification":
		if r.Phase == "updated" && r.Status == "" {
			return nil // a patch without a status transition
		}
		if !known {
			e = t.createLocked(r, now)
		}
		if r.Phase == "notification" {
			if r.TotalTokens != 0 || r.ToolUses != 0 || r.DurationMS != 0 {
				e.d.TotalTokens, e.d.ToolUses, e.d.DurationMS = r.TotalTokens, r.ToolUses, r.DurationMS
			}
		}
		// An evidenced ending already stands; only an inferred one (or a
		// running delegate) takes this report's state.
		if e.d.State == contracts.DelegateRunning || e.d.Inferred {
			state, reason := settle(r.Status)
			t.endLocked(e, state, reason, false, now)
		}
	default:
		return nil
	}
	if known && sameRecord(before, e.d) {
		return nil
	}
	return []contracts.Delegate{e.d}
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

func (t *Tracker) endLocked(e *entry, state contracts.DelegateState, reason string, inferred bool, at time.Time) {
	e.d.State, e.d.Reason, e.d.Inferred = state, reason, inferred
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

// sameRecord compares two records field for field — the times by instant,
// the capabilities by value.
func sameRecord(a, b contracts.Delegate) bool {
	sameEnd := (a.EndedAt == nil && b.EndedAt == nil) ||
		(a.EndedAt != nil && b.EndedAt != nil && a.EndedAt.Equal(*b.EndedAt))
	sameCaps := (a.Capabilities == nil && b.Capabilities == nil) ||
		(a.Capabilities != nil && b.Capabilities != nil && *a.Capabilities == *b.Capabilities)
	if !sameEnd || !sameCaps || !a.StartedAt.Equal(b.StartedAt) {
		return false
	}
	a.EndedAt, b.EndedAt = nil, nil
	a.Capabilities, b.Capabilities = nil, nil
	a.StartedAt, b.StartedAt = time.Time{}, time.Time{}
	return a == b
}
