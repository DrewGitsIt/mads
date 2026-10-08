package runner

import (
	"context"
	"log"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// observeConversation is the pump's single hook for the conversation-pane
// redesign's provider contract (docs/CONVERSATION-REDESIGN-TASKS.md T5+T6),
// applied to every provider event before synth/digest/broadcast see it:
//
//   - T5: stamps the tool call's lifecycle status (pkg/toolstatus — the one
//     authoritative rule set) onto tool_use/tool_result events, so live
//     subscribers and everything persisted downstream carry the same
//     synthesized-or-provider-reported status; at turn end it sweeps calls
//     that never got a result to "interrupted" as persisted
//     session.tool_status_changed events.
//   - T6: persists what the harness transcript can never replay — subagent
//     sidechain events (parent-tagged), task lifecycle updates, thinking
//     blocks, and compaction status — into the Data Service event log.
//   - the session rail's agent-state label (agentstate.go): what the MAIN
//     agent is doing right now, floored and transition-only. The state edges
//     themselves ride the turn lifecycle instead (Send/onTurnEnd), not this
//     hook, because onTurnEnd runs before the turn-end event reaches here
//     and can already have started a queued next turn.
//
// Returns the (possibly re-stamped) event for the caller to pass on.
func (r *Runner) observeConversation(ev provider.Event) provider.Event {
	ev = r.stampToolStatus(ev)
	r.observeAgentState(ev)
	r.observeDelegates(ev)
	if ev.Kind == provider.EventTurnEnd {
		r.flushInterruptedTools()
	}
	r.persistConversation(ev)
	return ev
}

// flushTurnState closes out both signals a turn leaves dangling when the
// provider stream closes underneath it (Shutdown, or the process dying):
// every tool call still without a result, and the agent state — which must
// never stay "working" just because no EventTurnEnd ever arrived. Both are
// no-ops when the ordinary turn-end path already ran.
func (r *Runner) flushTurnState() {
	r.flushInterruptedTools()
	r.markAgentIdle()
}

// stampToolStatus computes the status for a tool_use/tool_result event and
// stamps it on a copy of the event's ToolUse — a copy because ev.Tool is
// shared with every hub subscriber's channel buffer, and a later in-place
// mutation could race a slow reader. Events without a wire tool ID keep
// whatever the adapter stated (possibly nothing — honest unknown).
func (r *Runner) stampToolStatus(ev provider.Event) provider.Event {
	if ev.Tool == nil {
		return ev
	}
	var status string
	switch ev.Kind {
	case provider.EventToolUse:
		status = r.toolStatus.ToolUse(ev.Tool.ID, ev.Tool.Name, ev.ParentToolUseID, string(ev.Tool.Status))
	case provider.EventToolResult:
		status = r.toolStatus.ToolResult(ev.Tool.ID, ev.IsError, ev.Text, string(ev.Tool.Status))
	default:
		return ev
	}
	if status == string(ev.Tool.Status) {
		return ev
	}
	tool := *ev.Tool
	tool.Status = provider.ToolStatus(status)
	ev.Tool = &tool
	return ev
}

// flushInterruptedTools closes out every tool call still without a result —
// the turn ended (completed or interrupted) or the provider stream closed
// (session stopped, process died) underneath it. Each becomes a persisted
// session.tool_status_changed "interrupted" event; there is no wire event
// left that could carry the status, so the event log (whose live SSE feed
// the UI also subscribes to) is both the durable record and the live signal.
func (r *Runner) flushInterruptedTools() {
	// A delegate still running past this turn keeps its calls in flight
	// (their results are still coming); they close out when it ends.
	for _, up := range r.toolStatus.TurnEndExcept(r.keepLiveDelegateCalls) {
		r.appendConversation(contracts.SessionToolStatusChanged, contracts.ToolStatusChange{
			ToolUseID:       up.ToolUseID,
			Status:          up.Status,
			ParentToolUseID: up.ParentToolUseID,
		})
	}
}

// persistConversation writes the events the provider's own transcript can
// never give back — claude-code's on-disk transcript omits subagent
// sidechains entirely, so this event log is the ONLY historical source for
// them (T6). Main-agent text/tool events are NOT persisted here: the
// transcript refetch already replays those. Thinking token-count ticks are
// live-only indicator fuel and skipped. Best-effort throughout, like every
// other pump-side append: a failed write logs and never perturbs the turn.
func (r *Runner) persistConversation(ev provider.Event) {
	switch {
	case ev.Kind == provider.EventTask && ev.Task != nil:
		// Every phase persists, including "progress": a client reloading
		// mid-task reconstructs the live counters from the log (the live
		// /output stream has no replay), and volume is the same order as
		// the sidechain's own persisted tool events.
		r.appendConversation(contracts.SessionSubagentTask, contractsTask(ev))
	case ev.ParentToolUseID != "":
		var kind string
		switch ev.Kind {
		case provider.EventText:
			kind = "text"
		case provider.EventToolUse:
			kind = "tool_use"
		case provider.EventToolResult:
			kind = "tool_result"
		case provider.EventThinking:
			if ev.Text == "" {
				return // token tick — live-only
			}
			kind = "thinking"
		default:
			return
		}
		r.appendConversation(contracts.SessionSubagentEvent, contracts.SubagentEvent{
			ParentToolUseID: ev.ParentToolUseID,
			SubagentType:    ev.SubagentType,
			TaskDescription: ev.TaskDescription,
			Kind:            kind,
			Text:            ev.Text,
			Tool:            contractsToolUse(ev.Tool),
			IsError:         ev.IsError,
		})
	case ev.Kind == provider.EventThinking && ev.Text != "":
		r.appendConversation(contracts.SessionThinking, contracts.ThinkingBlock{Text: ev.Text})
	case ev.Kind == provider.EventCompact && ev.Compact != nil:
		r.appendConversation(contracts.SessionCompact, contracts.CompactStatus{
			Phase:   ev.Compact.Phase,
			Success: ev.Compact.Success,
			Error:   ev.Compact.Error,
		})
	}
}

// appendConversation appends one conversation-replay event, best-effort.
// Nil r.data — unit tests build Runners without a Data Service — is a
// no-op, matching appendPermissionEvent.
func (r *Runner) appendConversation(t contracts.EventType, payload any) {
	if r.data == nil {
		return
	}
	if err := r.data.Append(context.Background(), &contracts.Envelope{
		Type:      t,
		SessionID: r.sessionID,
		Payload:   mustJSON(payload),
	}); err != nil {
		log.Printf("runner: append %s: %v", t, err)
	}
}

// contractsToolUse maps the provider's enriched ToolUse onto the contracts
// mirror — every field the wire carries, including the T4 enrichment
// (kind/title/status/locations/server). Shared by the transcript read path
// and the persisted subagent stream so no consumer sees a stripped copy.
func contractsToolUse(t *provider.ToolUse) *contracts.ToolUse {
	if t == nil {
		return nil
	}
	out := &contracts.ToolUse{
		ID:     t.ID,
		Name:   t.Name,
		Input:  t.Input,
		Path:   t.Path,
		Kind:   string(t.Kind),
		Title:  t.Title,
		Status: string(t.Status),
		Server: t.Server,
	}
	for _, loc := range t.Locations {
		out.Locations = append(out.Locations, contracts.ToolLocation{Path: loc.Path, Line: loc.Line})
	}
	return out
}
