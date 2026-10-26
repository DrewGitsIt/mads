package claudecode

import (
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// wireLine is one stream-json output line (or the whole one-shot JSON
// result). Field shapes verified against claude-code 2.1.207.
type wireLine struct {
	Type      string       `json:"type"`
	Subtype   string       `json:"subtype"`
	SessionID string       `json:"session_id"`
	Message   *wireMessage `json:"message"`
	// UUID is the line's id: on an assistant or user line, the same uuid
	// the session's JSONL line carries (keys.go).
	UUID string `json:"uuid"`

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

	// init-line fields. APIKeySource says how the CLI authenticated: "none"
	// for a subscription login, otherwise where its API key came from
	// ("ANTHROPIC_API_KEY", "apiKeyHelper", ...) — verified against
	// claude-code 2.1.285, testdata/stream-auto-mode.jsonl.
	Model        string `json:"model"`
	APIKeySource string `json:"apiKeySource"`
	// MessagingSocketPath is the local socket other claude processes reach
	// this one on (cross-session peer messages); PeerAddress is it with the
	// "uds:" prefix the senders' origin.from carries — verified against
	// claude-code 2.1.285, testdata/stream-peer-idle.jsonl.
	MessagingSocketPath string `json:"messaging_socket_path"`

	// IsReplay marks a "user" line the CLI echoes back because the process
	// runs with --replay-user-messages: every user message it takes in,
	// mads's own sends included (those carry no Origin and say nothing new).
	// One with Origin.Kind "peer" is a cross-session message from another
	// claude process — the only live sign one arrived.
	IsReplay bool `json:"isReplay"`

	// peer_message_hold system-line fields: the CLI held a peer message back
	// instead of handing it to the model (State "held", Cause e.g.
	// "mode-mismatch"), and later says what became of it (State "dropped").
	// From/FromName are the sender, as on a delivered message's origin.
	State    string `json:"state"`
	From     string `json:"from"`
	FromName string `json:"from_name"`
	Cause    string `json:"cause"`

	// rate_limit_event field: the plan's rate-limit standing, sent around
	// each model call on a subscription login.
	RateLimitInfo *wireRateLimitInfo `json:"rate_limit_info"`

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
	TaskType       string `json:"task_type"`
	TaskID         string `json:"task_id"`
	ToolUseID      string `json:"tool_use_id"`
	Description    string `json:"description"`
	Prompt         string `json:"prompt"`
	IsBackgrounded bool   `json:"is_backgrounded"`
	// OwnedBySubagent marks a background task a subagent started. Only
	// task_started carries it (never the roster or the later phases), and
	// only when true — verified against claude-code 2.1.285.
	OwnedBySubagent bool           `json:"owned_by_subagent"`
	SpawnDepth      int            `json:"spawn_depth"`
	LastToolName    string         `json:"last_tool_name"`
	Summary         string         `json:"summary"`
	OutputFile      string         `json:"output_file"`
	Patch           *wireTaskPatch `json:"patch"`
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

// wireOrigin is the origin marker on a line the CLI produced itself rather
// than from mads's stdin. Kind "task-notification": the re-invocation that
// delivers a background subagent's completion closes with a second result
// line stamped with it. Kind "peer": a cross-session message from another
// claude process — on its replayed user line, its transcript user line or
// queued_command attachment, and the result of the turn it opened — where
// the rest of the fields say who sent it and what it said. Verified against
// claude-code 2.1.285 (testdata/stream-peer-*.jsonl, transcript_peer.jsonl).
type wireOrigin struct {
	Kind string `json:"kind"`
	// From is the sender's socket address ("uds:/tmp/cc-socks/N.sock"),
	// Name the CLI's label for the sender ("a-3c"), Body the message text
	// without the <cross-session-message> wrapper, MsgID its id.
	From  string `json:"from"`
	Name  string `json:"name"`
	Body  string `json:"body"`
	MsgID string `json:"msg_id"`
}

// peerChannel is the Inbound.Channel of claude-code's cross-session messages.
const peerChannel = "peer"

// isPeer reports whether o marks a cross-session peer message.
func (o *wireOrigin) isPeer() bool { return o != nil && o.Kind == "peer" }

// inbound is the neutral form of a peer origin.
func (o *wireOrigin) inbound() *provider.Inbound {
	return &provider.Inbound{Channel: peerChannel, From: o.From, FromName: o.Name}
}

// isPeerReplay reports whether this is the replayed user line of a
// cross-session message (see IsReplay).
func (l *wireLine) isPeerReplay() bool {
	return l.Type == "user" && l.IsReplay && l.Origin.isPeer()
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
// figure, and counting it again would double-count the session's cost —
// the streaming transport, which knows the running total, puts back the
// turn's own share (streamSession.costSeen).
func (l *wireLine) notificationTurnEnd(interrupted bool) []provider.Event {
	ev := l.turnEnd(interrupted)
	if ev.Usage != nil {
		ev.Usage.CostUSD = 0
	}
	if l.IsError {
		return []provider.Event{{Kind: provider.EventError, Text: l.Result}, ev}
	}
	return []provider.Event{ev}
}

// wireRateLimitInfo is a rate_limit_event's rate_limit_info. Only
// unifiedWindows is read: it carries every window at once, where the
// top-level status/resetsAt describe just the one rateLimitType names.
type wireRateLimitInfo struct {
	UnifiedWindows map[string]wireRateLimitWindow `json:"unifiedWindows"`
}

// wireRateLimitWindow is one unifiedWindows entry: utilization 0-1,
// resetsAt in epoch seconds.
type wireRateLimitWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    int64   `json:"resetsAt"`
}

// rateLimitWindowOrder lists the windows in the order the /usage card shows
// them; any window not named here follows, sorted by id.
var rateLimitWindowOrder = []string{"five_hour", "seven_day"}

// accountEvents builds the EventAccount an init line (billing mode) or a
// rate_limit_event (plan windows) stands for; nil for a line that says
// neither.
func (l *wireLine) accountEvents() []provider.Event {
	switch {
	case l.Type == "system" && l.Subtype == "init" && l.APIKeySource != "":
		mode := provider.BillingUsage
		if l.APIKeySource == "none" {
			mode = provider.BillingPlan
		}
		return []provider.Event{{Kind: provider.EventAccount, Account: &provider.AccountStatus{BillingMode: mode}}}
	case l.Type == "rate_limit_event" && l.RateLimitInfo != nil && len(l.RateLimitInfo.UnifiedWindows) > 0:
		ws := l.RateLimitInfo.UnifiedWindows
		ids := make([]string, 0, len(ws))
		for id := range ws {
			if !slices.Contains(rateLimitWindowOrder, id) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		limits := make([]provider.RateLimitWindow, 0, len(ws))
		for _, id := range append(slices.Clone(rateLimitWindowOrder), ids...) {
			w, ok := ws[id]
			if !ok {
				continue
			}
			lw := provider.RateLimitWindow{ID: id, UsedPct: math.Round(w.Utilization*1000) / 10}
			if w.ResetsAt > 0 {
				lw.ResetsAt = time.Unix(w.ResetsAt, 0).UTC()
			}
			limits = append(limits, lw)
		}
		return []provider.Event{{Kind: provider.EventAccount, Account: &provider.AccountStatus{Limits: limits}}}
	}
	return nil
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
	// ID is an assistant message's API id (msg_…), shared by every line
	// the message's blocks are written on; a synthetic line's is a UUID.
	ID      string          `json:"id"`
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

// baseModel is a modelUsage key without its bracketed window suffix:
// "claude-opus-5[1m]" → "claude-opus-5".
func baseModel(key string) string {
	if i := strings.LastIndex(key, "["); i > 0 && strings.HasSuffix(key, "]") {
		return key[:i]
	}
	return key
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
			if baseModel(key) == model {
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
	return l.keyedEvents(interrupted, partial, nil)
}

// keyedEvents is events with each conversation item keyed by k (keys.go);
// nil k leaves every Key empty.
func (l *wireLine) keyedEvents(interrupted bool, partial bool, k *lineKeys) []provider.Event {
	switch l.Type {
	case "system":
		return l.systemEvents()
	case "stream_event":
		return l.tagged(l.streamDeltaEvents(k))
	case "assistant":
		var evs []provider.Event
		for i, b := range l.Message.blocks() {
			switch b.Type {
			case "text":
				if !partial && b.Text != "" {
					evs = append(evs, provider.Event{Kind: provider.EventText, Key: k.block(i), Text: b.Text})
				}
			case "thinking":
				// Emitted in both partial and whole modes: streamDeltaEvents
				// ignores thinking_delta lines, so unlike text there is no
				// duplicate to drop. Signature-only blocks (empty thinking
				// text — the shape the real capture in testdata/
				// stream-task-subagent.jsonl actually carries) say nothing.
				if b.Thinking != "" {
					evs = append(evs, provider.Event{Kind: provider.EventThinking, Key: k.block(i), Text: b.Thinking})
				}
			case "tool_use":
				evs = append(evs, provider.Event{Kind: provider.EventToolUse, Key: k.tool(b.ID), Tool: newToolUse(b.ID, b.Name, b.Input)})
			}
		}
		// A subagent's API call: its context and model live nowhere else on
		// the stream. The CLI splits one call into a line per content block,
		// each repeating the usage; the record dedupes.
		if l.ParentToolUseID != "" && l.Message.callUsage() != nil {
			evs = append(evs, provider.Event{Kind: provider.EventSubagentUsage, Usage: l.Message.callUsage()})
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
		if l.isPeerReplay() {
			// A message from another claude process, arrived idle (it opens
			// a turn of its own) or mid-turn (absorbed after the running
			// tool call). Its body rides on the origin; the content is the
			// CLI's wrapper around it.
			return []provider.Event{{Kind: provider.EventInbound, Inbound: l.Origin.inbound(), Text: l.Origin.Body}}
		}
		// Any other replayed user line is mads's own send echoed back: text
		// blocks only, which nothing below reads.
		//
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
				Key:     k.tool(b.ToolUseID),
				Tool:    &provider.ToolUse{ID: b.ToolUseID},
				Text:    toolResultText(b.Content),
				IsError: b.IsError,
			})
		}
		return l.tagged(evs)
	case "rate_limit_event":
		return l.accountEvents()
	default:
		// control_response, ...: bookkeeping, not conversation.
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
// ref/model, and its refFired guard keeps the repeat harmless) yields only
// its billing mode (accountEvents); any other subtype is bookkeeping, same
// as the events() default case.
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
	case l.Subtype == "peer_message_hold" && l.State == provider.InboundHeld:
		// A peer message the CLI held back (the sender's permission mode
		// doesn't match this one's, ...). The model never sees it, so it
		// opens no turn. The follow-up line saying what became of it (state
		// "dropped") is the CLI's bookkeeping, not another message.
		return []provider.Event{{Kind: provider.EventInbound, Inbound: &provider.Inbound{
			Channel: peerChannel, From: l.From, FromName: l.FromName, State: provider.InboundHeld, HoldReason: l.Cause,
		}}}
	case l.Subtype == "thinking_tokens":
		return []provider.Event{{Kind: provider.EventThinking, ThinkingTokens: l.EstimatedTokens}}
	case l.Subtype == "init":
		// Its billing mode only; the ref and model it also carries are
		// session.go's (see above). A repeat init says the same thing again,
		// which the runner's write-on-change absorbs.
		return l.accountEvents()
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
func (l *wireLine) streamDeltaEvents(k *lineKeys) []provider.Event {
	if len(l.Event) == 0 {
		return nil
	}
	var e struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(l.Event, &e); err != nil {
		return nil
	}
	if e.Type == "content_block_delta" && e.Delta.Type == "text_delta" && e.Delta.Text != "" {
		return []provider.Event{{Kind: provider.EventText, Key: k.streamBlock(e.Index), Text: e.Delta.Text}}
	}
	return nil
}
