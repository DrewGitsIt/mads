package claudecode

import (
	"encoding/json"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// wireLine is one stream-json output line (or the whole one-shot JSON
// result). Field shapes verified against claude-code 2.1.207.
type wireLine struct {
	Type      string       `json:"type"`
	Subtype   string       `json:"subtype"`
	SessionID string       `json:"session_id"`
	Message   *wireMessage `json:"message"`

	// result-line fields. Origin distinguishes the extra result line a
	// background-subagent notification re-invocation closes with (kind
	// "task-notification") from the ordinary user-turn result, which carries
	// no origin at all — see events()'s "result" case.
	Result       string                    `json:"result"`
	IsError      bool                      `json:"is_error"`
	Usage        *wireUsage                `json:"usage"`
	TotalCostUSD float64                   `json:"total_cost_usd"`
	ModelUsage   map[string]wireModelUsage `json:"modelUsage"`
	Origin       *wireOrigin               `json:"origin"`

	// init-line fields
	Model string `json:"model"`

	// system-line fields ("system" type; Subtype distinguishes "init",
	// "status", and "compact_boundary" — verified against claude-code
	// 2.1.220). Status carries "compacting" on the start-of-compaction line;
	// the completion line instead carries CompactResult ("success"/"failed")
	// with Status empty, and CompactError set on failure (e.g. "Not enough
	// messages to compact."). The completion status line alone is enough for
	// the UI's status item; the "compact_boundary" line after it carries
	// CompactMetadata, whose post_tokens is the only occupancy the /compact
	// turn reports — its result line's usage is all zeros (streamSession.
	// handleLine; verified against claude-code 2.1.285, testdata/
	// stream-compact.jsonl).
	Status          string               `json:"status"`
	CompactResult   string               `json:"compact_result"`
	CompactError    string               `json:"compact_error"`
	CompactMetadata *wireCompactMetadata `json:"compact_metadata"`

	// task-lifecycle system-line fields (subtypes "task_started",
	// "task_progress", "task_updated", "task_notification" — claude-code 2.x,
	// verified against the real capture in testdata/
	// stream-task-subagent.jsonl). Status doubles as task_notification's
	// final state; Usage doubles as the {total_tokens, tool_uses,
	// duration_ms} counters on progress/notification lines (see wireUsage).
	// TaskType says what kind of background task it is ("local_agent" for a
	// subagent, "local_bash" for a run_in_background Bash call — verified
	// against claude-code 2.1.290, testdata/stream-task-bash.jsonl) and rides
	// task_started only; the later phases carry just the task_id.
	TaskType       string         `json:"task_type"`
	TaskID         string         `json:"task_id"`
	ToolUseID      string         `json:"tool_use_id"`
	Description    string         `json:"description"`
	Prompt         string         `json:"prompt"`
	IsBackgrounded bool           `json:"is_backgrounded"`
	SpawnDepth     int            `json:"spawn_depth"`
	LastToolName   string         `json:"last_tool_name"`
	Summary        string         `json:"summary"`
	OutputFile     string         `json:"output_file"`
	Patch          *wireTaskPatch `json:"patch"`
	// Tasks is a "background_tasks_changed" line's roster: every background
	// task still running, subagents and background Bash alike.
	Tasks []wireRosterTask `json:"tasks"`

	// thinking_tokens system-line field: the running estimate streamed while
	// the model thinks.
	EstimatedTokens int `json:"estimated_tokens"`

	// subagent-sidechain tagging, present at top level on the assistant/user
	// lines a Task/Agent subagent produces (claude-code 2.x; the main
	// agent's lines carry parent_tool_use_id null). SubagentType is shared
	// with the task-lifecycle system lines above — same key, same meaning.
	ParentToolUseID string `json:"parent_tool_use_id"`
	SubagentType    string `json:"subagent_type"`
	TaskDescription string `json:"task_description"`

	// stream_event-line field: the raw Anthropic streaming event, present only
	// when claude runs with --include-partial-messages (interactive transport).
	// Carries token-level content_block_delta text.
	Event json.RawMessage `json:"event"`
}

// wireOrigin is a result line's origin marker. Only the "task-notification"
// kind is known today — the re-invocation that delivers a background
// subagent's completion closes with a second result line stamped with it.
type wireOrigin struct {
	Kind string `json:"kind"`
}

// isTaskNotificationResult reports whether this is the result line that
// closes a background-task notification's re-invocation (see events).
func (l *wireLine) isTaskNotificationResult() bool {
	return l.Type == "result" && l.Origin != nil && l.Origin.Kind == "task-notification"
}

// turnEnd builds the EventTurnEnd a result line stands for, usage included.
func (l *wireLine) turnEnd(interrupted bool) provider.Event {
	ev := provider.Event{Kind: provider.EventTurnEnd, Interrupted: interrupted}
	if l.Usage != nil {
		ev.Usage = &provider.Usage{
			InputTokens:              l.Usage.InputTokens,
			OutputTokens:             l.Usage.OutputTokens,
			CacheCreationInputTokens: l.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     l.Usage.CacheReadInputTokens,
			Model:                    l.model(),
			CostUSD:                  l.TotalCostUSD,
			ContextTokens:            l.Usage.contextTokens(),
			ContextWindow:            l.contextWindowFor(l.model()),
		}
	}
	return ev
}

// notificationTurnEnd is the end of an UNPROMPTED turn: the one the CLI runs
// on its own when a background task finishes after the user's turn already
// closed. Only the streaming transport can know that's what the line closes
// (streamSession.handleLine). Its usage is the turn's real occupancy, so it
// is kept. total_cost_usd is not: on this line it repeats a cumulative
// figure, and counting it again would double-count the session's cost.
func (l *wireLine) notificationTurnEnd() []provider.Event {
	ev := l.turnEnd(false)
	if ev.Usage != nil {
		ev.Usage.CostUSD = 0
	}
	if l.IsError {
		return []provider.Event{{Kind: provider.EventError, Text: l.Result}, ev}
	}
	return []provider.Event{ev}
}

// wireRosterTask is one entry of a "background_tasks_changed" roster.
type wireRosterTask struct {
	TaskID      string `json:"task_id"`
	TaskType    string `json:"task_type"`
	Description string `json:"description"`
}

// isAgent mirrors wireLine.isAgentTask for a roster entry.
func (t wireRosterTask) isAgent() bool {
	return t.TaskType == "" || t.TaskType == "local_agent"
}

// wireTaskPatch is a task_updated line's patch object. end_time also rides
// along on the wire but the status transition is all consumers need.
type wireTaskPatch struct {
	Status string `json:"status"`
}

// model names the model where the line says: init lines carry it directly,
// result lines only as the modelUsage key.
func (l *wireLine) model() string {
	if l.Model != "" {
		return l.Model
	}
	if len(l.ModelUsage) == 1 {
		for m := range l.ModelUsage {
			return m
		}
	}
	return ""
}

// wireMessage is an API message; content is either a plain string or a
// block list, so it stays raw until blocks() decodes it.
type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	// Model and Usage ride on assistant messages: on a transcript line they
	// are that one API call's own accounting (history.go reads them off the
	// last assistant line).
	Model string     `json:"model"`
	Usage *wireUsage `json:"usage"`
}

type wireBlock struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	// tool_result-block fields (only present in a "user" role message that
	// feeds a tool's outcome back into context).
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	// Iterations, present only on a result line's usage, breaks the turn
	// down per API call. The top-level token fields are the SUM across
	// iterations — a billing figure that overstates context occupancy by
	// ~len(Iterations) on multi-tool turns, since every iteration re-reads
	// the whole cached prefix. The last iteration's prompt side is the real
	// end-of-turn occupancy (contextTokens).
	Iterations []wireUsage `json:"iterations"`
	// TotalTokens, ToolUses, and DurationMS are the task-counter shape a
	// task_progress/task_notification system line carries under the same
	// "usage" key (see wireLine's task-lifecycle fields); absent on result
	// lines, where the fields above apply instead.
	TotalTokens int `json:"total_tokens"`
	ToolUses    int `json:"tool_uses"`
	DurationMS  int `json:"duration_ms"`
}

// contextTokens is the prompt-side size of the turn's final API call — the
// true context occupancy after this turn, as opposed to the aggregate the
// top-level fields report. Zero when the line carries no iteration breakdown
// (older CLIs), letting consumers fall back to the aggregate.
func (u *wireUsage) contextTokens() int {
	if len(u.Iterations) == 0 {
		return 0
	}
	last := u.Iterations[len(u.Iterations)-1]
	return last.InputTokens + last.CacheReadInputTokens + last.CacheCreationInputTokens
}

// countsNoTokens reports whether a result line's usage counted nothing at
// all: the shape of a turn that made no model call, like /compact's.
func (u *wireUsage) countsNoTokens() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheCreationInputTokens == 0 &&
		u.CacheReadInputTokens == 0 && u.contextTokens() == 0
}

// wireCompactMetadata is a compact_boundary line's compact_metadata. Only
// PostTokens is read: the context the compacted conversation now carries
// (absent from older CLIs, where it decodes to zero).
type wireCompactMetadata struct {
	PostTokens int `json:"post_tokens"`
}

// wireModelUsage is one modelUsage entry on a result line. Only the runtime-
// reported window size is consumed today; the per-model token/cost fields are
// redundant with the top-level usage/total_cost_usd.
type wireModelUsage struct {
	ContextWindow int `json:"contextWindow"`
}

// contextWindowFor resolves model's runtime-reported window from the line's
// modelUsage map. The keys don't reliably equal the model id: a scaled-window
// session suffixes it ("claude-opus-5[1m]"), and a turn that ran helper
// models lists several entries — an exact lookup alone drops the window
// there, so a genuine 1M session gets judged against the static 200k table
// (~5× overstated occupancy). Exact key first, then a key whose trailing
// bracketed suffix stripped equals model, then the sole entry when the map
// is unambiguous; 0 when it stays ambiguous.
func (l *wireLine) contextWindowFor(model string) int {
	if mu, ok := l.ModelUsage[model]; ok {
		return mu.ContextWindow
	}
	if model != "" {
		for key, mu := range l.ModelUsage {
			if i := strings.LastIndex(key, "["); i > 0 && key[:i] == model {
				return mu.ContextWindow
			}
		}
	}
	if len(l.ModelUsage) == 1 {
		for _, mu := range l.ModelUsage {
			return mu.ContextWindow
		}
	}
	return 0
}

// blocks decodes message content: a bare string becomes one text block.
func (m *wireMessage) blocks() []wireBlock {
	if m == nil || len(m.Content) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return []wireBlock{{Type: "text", Text: s}}
	}
	var bs []wireBlock
	if err := json.Unmarshal(m.Content, &bs); err != nil {
		return nil
	}
	return bs
}

// toolResultText decodes a tool_result block's content: a bare string, or an
// array of blocks (mirroring wireMessage.blocks' string-vs-array handling) —
// only text blocks contribute.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var bs []wireBlock
	if err := json.Unmarshal(raw, &bs); err != nil {
		return ""
	}
	var texts []string
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// toolMutates reports whether a claude-code tool writes the file its input
// names. Everything else that carries a file_path/path (Read, Grep, Glob,
// unknown MCP tools) is a read as far as §5 file tracking is concerned —
// fail closed: an unrecognized tool is not reported as a file change.
func toolMutates(name string) bool {
	switch name {
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		return true
	}
	return false
}

// toolPath digs the referenced file out of a tool input, if any — rendered
// in digests/transcripts for reads and writes alike; pair with toolMutates
// for the §5 file-change signal.
func toolPath(input json.RawMessage) string {
	var in struct {
		FilePath     string `json:"file_path"`
		Path         string `json:"path"`
		NotebookPath string `json:"notebook_path"`
	}
	_ = json.Unmarshal(input, &in)
	switch {
	case in.FilePath != "":
		return in.FilePath
	case in.NotebookPath != "":
		return in.NotebookPath
	default:
		return in.Path
	}
}

// events normalizes one stream line. interrupted marks the turn end when an
// interrupt was requested since the turn began. partial is set on the
// interactive transport (--include-partial-messages), where token-level text
// arrives first via "stream_event" content_block_delta lines — the
// whole-message "assistant" text block that follows is then a duplicate of
// what already streamed, so it is dropped and only its tool_use blocks emit.
func (l *wireLine) events(interrupted bool, partial bool) []provider.Event {
	switch l.Type {
	case "system":
		return l.systemEvents()
	case "stream_event":
		return l.tagged(l.streamDeltaEvents())
	case "assistant":
		var evs []provider.Event
		for _, b := range l.Message.blocks() {
			switch b.Type {
			case "text":
				if !partial && b.Text != "" {
					evs = append(evs, provider.Event{Kind: provider.EventText, Text: b.Text})
				}
			case "thinking":
				// Emitted in both partial and whole modes: streamDeltaEvents
				// ignores thinking_delta lines, so unlike text there is no
				// duplicate to drop. Signature-only blocks (empty thinking
				// text — the shape the real capture in testdata/
				// stream-task-subagent.jsonl actually carries) say nothing.
				if b.Thinking != "" {
					evs = append(evs, provider.Event{Kind: provider.EventThinking, Text: b.Thinking})
				}
			case "tool_use":
				evs = append(evs, provider.Event{Kind: provider.EventToolUse, Tool: newToolUse(b.ID, b.Name, b.Input)})
			}
		}
		return l.tagged(evs)
	case "result":
		// A background subagent's completion notification re-invokes the
		// model inside the same user turn, and that internal continuation
		// closes with a second result line — stamped origin
		// "task-notification", while the canonical user-turn result carries
		// no origin. Emitting both as turn ends would double-fire every
		// per-turn consumer (queued-message delivery, digest turn records, a
		// second turn boundary in the UI) and double-count total_cost_usd,
		// which the notification line repeats as a cumulative figure. So the
		// notification result is not a turn boundary: suppress it, keeping
		// only an error surface should the re-invocation itself fail. Its
		// per-invocation usage detail is dropped with it — the next real
		// turn's result refreshes occupancy.
		//
		// That holds only while the continuation sits inside the user turn.
		// A background task that finishes after the turn has already closed
		// makes the CLI run a whole unprompted turn, and there this line is
		// the only end that turn gets. The streaming transport tells the two
		// apart (streamSession.handleLine) and builds that end itself via
		// notificationTurnEnd; this stateless per-line view can't.
		if l.isTaskNotificationResult() {
			if l.IsError {
				return []provider.Event{{Kind: provider.EventError, Text: l.Result}}
			}
			return nil
		}
		ev := l.turnEnd(interrupted)
		if l.IsError {
			return []provider.Event{
				{Kind: provider.EventError, Text: l.Result},
				ev,
			}
		}
		return []provider.Event{ev}
	case "user":
		// claude-code feeds a tool's outcome back into the model's context
		// as a "user" role message — the same shape normalizeTranscript
		// reads from disk — including permission denials, which otherwise
		// produce no event at all on a headless transport with nobody to
		// answer the CLI's own prompt. Surfacing it live, rather than
		// waiting for the next transcript refetch, is what lets a blocked
		// tool call render instead of silently stalling.
		var evs []provider.Event
		for _, b := range l.Message.blocks() {
			if b.Type != "tool_result" {
				continue
			}
			evs = append(evs, provider.Event{
				Kind:    provider.EventToolResult,
				Tool:    &provider.ToolUse{ID: b.ToolUseID},
				Text:    toolResultText(b.Content),
				IsError: b.IsError,
			})
		}
		return l.tagged(evs)
	default:
		// rate_limit_event, control_response, ...: bookkeeping, not
		// conversation.
		return nil
	}
}

// tagged stamps the line's subagent-sidechain identity onto every event it
// produced. Untagged lines (the main agent's — parent_tool_use_id absent or
// null) pass through unchanged, so the zero value keeps meaning "main
// agent". Without this, a Task subagent's thinking/text/tool events
// interleave into the main transcript as if the main agent produced them —
// the live bug P0 exists to fix.
func (l *wireLine) tagged(evs []provider.Event) []provider.Event {
	if l.ParentToolUseID == "" {
		return evs
	}
	for i := range evs {
		evs[i].ParentToolUseID = l.ParentToolUseID
		evs[i].SubagentType = l.SubagentType
		evs[i].TaskDescription = l.TaskDescription
	}
	return evs
}

// systemEvents normalizes a "system"-type line: compaction status, task
// lifecycle, and thinking progress. "init" (session start/reconnect
// announcement, the line claude-code re-emits right after a compaction
// completes, and the second one a background subagent's notification
// re-invocation opens with — session.go's handleLine still reads it for
// ref/model, and its refFired guard keeps the repeat harmless) and any other
// subtype are bookkeeping, same as the events() default case.
func (l *wireLine) systemEvents() []provider.Event {
	switch {
	case l.Subtype == "status" && l.Status == "compacting":
		return []provider.Event{{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "start"}}}
	case l.Subtype == "status" && l.CompactResult != "":
		return []provider.Event{{Kind: provider.EventCompact, Compact: &provider.CompactStatus{
			Phase:   "done",
			Success: l.CompactResult == "success",
			Error:   l.CompactError,
		}}}
	case l.isTaskLine():
		return []provider.Event{{Kind: provider.EventTask, Task: l.taskStatus()}}
	case l.Type == "system" && l.Subtype == "background_tasks_changed":
		return []provider.Event{{Kind: provider.EventTaskRoster, TaskRoster: l.agentRoster()}}
	case l.Subtype == "thinking_tokens":
		return []provider.Event{{Kind: provider.EventThinking, ThinkingTokens: l.EstimatedTokens}}
	default:
		return nil
	}
}

// isTaskLine reports whether this is a task-lifecycle system line.
func (l *wireLine) isTaskLine() bool {
	return l.Type == "system" && (l.Subtype == "task_started" || l.Subtype == "task_progress" ||
		l.Subtype == "task_updated" || l.Subtype == "task_notification")
}

// agentRoster is the roster's subagent task ids, in order: background Bash
// and the other task types are dropped the same way isAgentTask drops their
// lifecycle lines. Never nil, so an emptied roster still says "none".
func (l *wireLine) agentRoster() []string {
	ids := []string{}
	for _, t := range l.Tasks {
		if t.TaskID != "" && t.isAgent() {
			ids = append(ids, t.TaskID)
		}
	}
	return ids
}

// isAgentTask reports whether a task_started line spawns a subagent. The
// task_* lines are claude-code's lifecycle for every background task, not
// just subagents: a run_in_background Bash call gets them too, and mads
// renders each EventTask as a subagent (the substrip, the rail's count). An
// absent task_type keeps the old reading — streams from before the field
// existed only ever carried subagent tasks.
func (l *wireLine) isAgentTask() bool {
	return l.TaskType == "" || l.TaskType == "local_agent"
}

// taskStatus maps one task-lifecycle system line onto the normalized
// TaskStatus. The wire spreads the fields unevenly across subtypes (see the
// TaskStatus field docs); absent ones simply stay zero. task_updated is the
// one shape that nests its payload — the status transition rides in a patch
// object rather than at top level.
func (l *wireLine) taskStatus() *provider.TaskStatus {
	ts := &provider.TaskStatus{
		Phase:          strings.TrimPrefix(l.Subtype, "task_"),
		TaskID:         l.TaskID,
		ToolUseID:      l.ToolUseID,
		Description:    l.Description,
		SubagentType:   l.SubagentType,
		Prompt:         l.Prompt,
		IsBackgrounded: l.IsBackgrounded,
		SpawnDepth:     l.SpawnDepth,
		LastToolName:   l.LastToolName,
		Status:         l.Status,
		Summary:        l.Summary,
		OutputFile:     l.OutputFile,
	}
	if ts.Status == "" && l.Patch != nil {
		ts.Status = l.Patch.Status
	}
	if l.Usage != nil {
		ts.TotalTokens = l.Usage.TotalTokens
		ts.ToolUses = l.Usage.ToolUses
		ts.DurationMS = l.Usage.DurationMS
	}
	return ts
}

// streamDeltaEvents pulls token-level text out of a "stream_event" line's
// inner Anthropic event. Only content_block_delta/text_delta contributes —
// message/content-block start/stop framing, thinking deltas, and tool-input
// (input_json_delta) deltas are bookkeeping the whole-message "assistant"
// line already carries.
func (l *wireLine) streamDeltaEvents() []provider.Event {
	if len(l.Event) == 0 {
		return nil
	}
	var e struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(l.Event, &e); err != nil {
		return nil
	}
	if e.Type == "content_block_delta" && e.Delta.Type == "text_delta" && e.Delta.Text != "" {
		return []provider.Event{{Kind: provider.EventText, Text: e.Delta.Text}}
	}
	return nil
}
