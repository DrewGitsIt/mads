package runner

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// agentStateFloor is the minimum spacing between two appended
// session.agent_state events. A turn that fires twenty tools in a second is
// ordinary; twenty appends for it are not — the rail reads one line, and an
// event log is a durable record, not a ticker. Only the label-inside-a-turn
// transitions are floored: the state edges themselves (working on send, idle
// at turn end) always append immediately, because those are what a cold
// reader needs to be correct and they can't be high-frequency (one turn at a
// time).
const agentStateFloor = 500 * time.Millisecond

// The generic clauses, matching the copy the open conversation's working
// indicator already shows for the same two situations
// (ui/src/conversation/ChatRenderer/WorkingIndicator.tsx and its ticker):
// a turn in flight with no activity reported yet, and extended thinking.
const (
	agentLabelWorking    = "Working…"
	agentLabelThinking   = "Thinking…"
	agentLabelCompacting = "Compacting…"
)

// agentState is the runner's view of what this session's main agent is doing,
// and what it has already told the event log. Its own lock, like
// toolStatus's: the pump goroutine (activity labels), an HTTP handler's
// Send/Inject (state edges) and the coalescing timer all drive it.
type agentState struct {
	mu sync.Mutex
	// floor overrides agentStateFloor; zero means the const, and a negative
	// value disables coalescing entirely (every transition appends). Tests
	// set it — nothing in production does.
	floor time.Duration

	// state/label/since are the live truth; sentState/sentLabel/sentAt are
	// the last values actually appended. Transitions are the diff between
	// the two, which is also what makes a redundant sweep (turn end, then
	// the stream closing behind it) append nothing.
	state string
	label string
	since time.Time

	sentState string
	sentLabel string
	sentAt    time.Time

	// timer is the pending trailing flush for a label change the floor
	// deferred, nil when none is armed. Trailing rather than dropping,
	// because the last tool of a burst is usually the long one: dropping it
	// would leave the rail naming a tool that finished a second ago for the
	// rest of the turn.
	timer *time.Timer
}

// markAgentWorking records that a turn has gone in flight. Called by every
// path that delivers one (Send, Inject, onTurnEnd's queued drain), right
// after the in-flight flag is set and the lock released — never under r.mu,
// since this appends over HTTP. text is the turn being delivered: a
// /compact turn is labelled as such from its first moment, since it reports
// no tool or thinking activity that would otherwise name it.
func (r *Runner) markAgentWorking(text string) {
	label := agentLabelWorking
	if isCompactCommand(text) {
		label = agentLabelCompacting
	}
	r.setAgentState(contracts.AgentWorking, label, true)
}

// relabelWorking changes the working label of a turn already in flight, and
// is a no-op when none is. Only the pump calls it, and the pump is also what
// ends a turn, so the check can't race the idle edge.
func (r *Runner) relabelWorking(label string) {
	r.agent.mu.Lock()
	working := r.agent.state == contracts.AgentWorking
	r.agent.mu.Unlock()
	if working {
		r.setAgentState(contracts.AgentWorking, label, false)
	}
}

// isCompactCommand reports whether a turn's text is the CLI's /compact
// command, with or without its optional focus instructions.
func isCompactCommand(text string) bool {
	t := strings.TrimSpace(text)
	return t == "/compact" || strings.HasPrefix(t, "/compact ")
}

// markAgentIdle records that no turn is in flight. Called at turn end and
// again when the provider stream closes underneath a turn (flushTurnState),
// so no path can leave a session stuck reporting "working": the second call
// is a no-op when the first already reported idle.
func (r *Runner) markAgentIdle() {
	r.setAgentState(contracts.AgentIdle, "", true)
}

// observeAgentState updates the working label from one live main-agent
// event. Parent-tagged events are skipped deliberately: a sidechain's tools
// are the subagent's activity, not the main agent's, they roll up in the
// rail as a count, and a busy subagent would otherwise churn this label
// several times a second.
func (r *Runner) observeAgentState(ev provider.Event) {
	if ev.ParentToolUseID != "" {
		return
	}
	switch {
	case ev.Kind == provider.EventToolUse && ev.Tool != nil:
		r.setAgentState(contracts.AgentWorking, agentToolLabel(ev.Tool), false)
	case ev.Kind == provider.EventCompact && ev.Compact != nil && ev.Compact.Phase == "start":
		// Any compaction, including the Runner's own near-limit trigger —
		// not only a typed /compact, which markAgentWorking already names.
		// Relabel only: "Compact now" (CompactNow) runs outside any turn,
		// and nothing would ever mark that idle again.
		r.relabelWorking(agentLabelCompacting)
	case ev.Kind == provider.EventThinking:
		// Deliberately not the ticker's "Thinking · N tokens": a token
		// count would make every tick its own transition, and the count is
		// live-only indicator fuel that this log never persists anyway
		// (see contracts.SessionThinking).
		r.setAgentState(contracts.AgentWorking, agentLabelThinking, false)
	}
}

// setAgentState folds one observation into the tracker and appends an event
// if it is a transition. edge marks a state boundary (working/idle), which
// bypasses the floor and cancels any deferred label flush — a label from
// mid-turn must never land after the turn's own idle.
func (r *Runner) setAgentState(state, label string, edge bool) {
	st := &r.agent
	st.mu.Lock()
	defer st.mu.Unlock()

	now := time.Now()
	if state != st.state {
		// A state edge restarts the clock; a label change inside a turn
		// deliberately does not, so an elapsed counter driven off Since
		// spans the whole turn instead of resetting per tool.
		st.since = now
	}
	st.state, st.label = state, label

	if state == st.sentState && label == st.sentLabel {
		return // not a transition
	}
	if edge {
		st.stopTimerLocked()
		r.emitAgentStateLocked(now)
		return
	}
	floor := st.floor
	if floor == 0 {
		floor = agentStateFloor
	}
	if wait := floor - now.Sub(st.sentAt); wait > 0 {
		if st.timer == nil {
			st.timer = time.AfterFunc(wait, r.flushAgentState)
		}
		return
	}
	r.emitAgentStateLocked(now)
}

// flushAgentState is the coalescing timer's callback: append whatever the
// latest label is now that the floor has passed. Everything in between is
// dropped on purpose — that is the coalescing.
func (r *Runner) flushAgentState() {
	st := &r.agent
	st.mu.Lock()
	defer st.mu.Unlock()
	st.timer = nil
	if st.state == st.sentState && st.label == st.sentLabel {
		return // an edge already appended this; nothing left to say
	}
	r.emitAgentStateLocked(time.Now())
}

// emitAgentStateLocked publishes the current state and records it as sent.
// One call both appends the session.agent_state event and mirrors the state
// onto the session record (store.Sessions.SetAgentState), because a cold
// reader — a page load or an SSE reconnect mid-turn — has only the record to
// go on, and a record that lagged the stream would report a live turn idle.
// Called with st.mu held, including across the call, so two racing callers
// can never invert working and idle. A nil Data Service is a no-op, matching
// every other pump-side append.
func (r *Runner) emitAgentStateLocked(now time.Time) {
	st := &r.agent
	st.sentState, st.sentLabel, st.sentAt = st.state, st.label, now
	if r.data == nil {
		return
	}
	state := contracts.AgentState{State: st.state, Label: st.label, Since: st.since}
	if err := r.data.SetSessionAgentState(context.Background(), r.sessionID, state); err != nil {
		log.Printf("runner: set agent state %s: %v", state.State, err)
	}
}

func (st *agentState) stopTimerLocked() {
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
}

// agentToolLabel renders one tool call as the rail's status clause: a verb
// for the enriched Kind plus the adapter-built Title verbatim ("Running bash
// · make check"). The Title half is exactly the string the open
// conversation's working-indicator ticker shows for the same call, so the two
// surfaces can never disagree about what the agent is doing — the enrichment
// is built once, server-side (providers/claudecode/toolinfo.go).
func agentToolLabel(t *provider.ToolUse) string {
	verb := agentToolVerb(t)
	if t.Title == "" {
		return verb
	}
	return verb + " · " + t.Title
}

// agentToolVerb is the clause's leading verb phrase, by kind — never by
// harness-specific name, same rule the UI's rendering follows. An
// unclassified or unenriched call falls back to naming the tool, which is
// honest rather than a guess at what it does.
func agentToolVerb(t *provider.ToolUse) string {
	switch t.Kind {
	case provider.ToolKindExecute:
		if t.Name != "" {
			return "Running " + strings.ToLower(t.Name)
		}
		return "Running"
	case provider.ToolKindRead:
		return "Reading"
	case provider.ToolKindEdit:
		return "Editing"
	case provider.ToolKindDelete:
		return "Deleting"
	case provider.ToolKindMove:
		return "Moving"
	case provider.ToolKindSearch:
		return "Searching"
	case provider.ToolKindThink:
		return "Thinking"
	case provider.ToolKindFetch:
		return "Fetching"
	case provider.ToolKindAgent:
		return "Delegating"
	case provider.ToolKindPlan:
		return "Planning"
	case provider.ToolKindSkill:
		return "Running skill"
	case provider.ToolKindMCP:
		if t.Server != "" {
			return "Calling " + t.Server
		}
		return "Calling"
	}
	if t.Name != "" {
		return "Using " + t.Name
	}
	return agentLabelWorking
}
