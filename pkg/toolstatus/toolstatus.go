// Package toolstatus is the single authoritative implementation of the
// tool-call lifecycle-status policy (docs/CONVERSATION-REDESIGN-TASKS.md T5):
// every place the platform stamps a ToolUse.Status — the session runner's
// live broadcast, its event-log persistence, and any later history merge —
// computes it here, never with its own ad-hoc rules.
//
// The locked policy:
//
//   - Happy path: a provider-reported status (ACP conformance — ACP adapters
//     feed their wire's tool_call/tool_call_update statuses straight through
//     as the reported argument) is accepted as-is and always wins.
//   - Defensive synthesis everywhere else: "in_progress" when the tool_use
//     arrives with no stated status; "completed" or "failed" when its
//     tool_result arrives (failed on the result's error flag);
//     "interrupted" when the turn ends — or the session stops or is
//     cancelled — with no result for that call; "pending" while the call is
//     parked behind a permission request; "blocked" when it never ran
//     because permission was refused — mads's own ask was denied, expired
//     or cancelled (PermissionDenied), or its error result is the harness's
//     own refusal for lack of a grant (PermissionRefusal).
//
// The input model is deliberately wider than any one harness needs, because
// real agents disagree (providers/acprpc/testdata/captures/README.md):
// claude-code never states a status on its wire; Hermes sends a tool_call
// with no initial status and may follow with a single completed-only update
// — or, as its pinned capture shows (hermes-acp-full.jsonl, tc-17014d3d5506),
// no update at all, leaving an orphaned call only the turn-end sweep can
// close out. Post-execution reporters may state a terminal status on the
// call itself and never send a result. All of these resolve to an honest
// status here.
package toolstatus

import (
	"strings"
	"sync"
)

// The status vocabulary — mirrors providers/provider/event.go ToolStatus and
// pkg/contracts/transcript.go ToolUse.Status (plain strings here so this
// package imports neither side of the boundary it serves).
const (
	Pending     = "pending"
	InProgress  = "in_progress"
	Completed   = "completed"
	Failed      = "failed"
	Interrupted = "interrupted"
	Blocked     = "blocked"
)

// Terminal reports whether s is a final state — one no later signal (result,
// permission park, turn-end sweep) may override.
func Terminal(s string) bool {
	return s == Completed || s == Failed || s == Interrupted || s == Blocked
}

// DenialPrefix starts every deny message mads itself hands a harness when
// it refuses a permission ask (services/session/internal/runner,
// permissions.go). Harnesses relay that message to the model verbatim as
// the refused call's error result — claude-code does — so the prefix is
// how a reloaded transcript still knows the call was blocked, not failed.
const DenialPrefix = "mads blocked this tool call: "

// PermissionRefusal reports whether an error tool_result's text says the
// call never ran because permission was refused. Two sources:
//
//   - mads's own deny messages, which all start with DenialPrefix.
//   - Claude Code's own refusals, harness-specific text pinned against
//     claude-code 2.1.285 (the 2026-10 permission investigation; the same
//     shapes appear for a subagent's calls):
//     "Claude requested permissions to <verb> <target>, but you haven't
//     granted it yet." — the call needed a grant nobody gave (no
//     permission channel wired); and "The user doesn't want to proceed
//     with this tool use. …" — a prompt rejected, or withdrawn by an
//     interrupt while it was still open (also what a human's "No" in
//     claude's terminal UI leaves behind).
//
// A future CLI rewording silently degrades such calls back to "failed" —
// honest, just less specific.
func PermissionRefusal(text string) bool {
	text = strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(text, DenialPrefix):
		return true
	case strings.HasPrefix(text, "Claude requested permissions to "):
		return strings.Contains(text, "but you haven't granted it yet")
	default:
		return strings.HasPrefix(text, "The user doesn't want to proceed with this tool use.")
	}
}

// Update is one call's current (or just-synthesized) status, keyed by the
// wire tool_use ID. ParentToolUseID rides along for calls a subagent
// sidechain made (providers/provider/event.go Event.ParentToolUseID), so a
// status change can be routed to the right stream without a lookup; empty
// means the main agent.
type Update struct {
	ToolUseID       string
	ParentToolUseID string
	Status          string
}

// call is one tracked tool invocation.
type call struct {
	id       string
	name     string
	parent   string
	status   string
	reported bool // status came from the provider, not synthesis
	refused  bool // mads refused its permission ask (PermissionDenied)
}

// Tracker synthesizes statuses for one session's live tool calls. All
// methods are safe for concurrent use — tool events arrive on the runner's
// pump goroutine while permission parks arrive on the adapter's asker
// goroutines.
//
// Calls without a wire tool_use ID cannot be correlated to any later signal
// (result, park, sweep), so they are not tracked at all: ToolUse returns the
// reported status unchanged (possibly empty — honest unknown) rather than
// synthesizing a state that could never be corrected.
type Tracker struct {
	mu    sync.Mutex
	calls map[string]*call
	order []string // arrival order, for deterministic sweeps
}

// New returns an empty Tracker.
func New() *Tracker {
	return &Tracker{calls: map[string]*call{}}
}

// ToolUse records a call's arrival and returns the status to stamp on it.
// reported is the provider-stated status ("" when the wire had none — the
// claude-code and Hermes shapes). A repeat arrival for an already-tracked id
// (an ACP adapter surfacing a tool_call_update as another tool_use) applies
// a non-empty reported status and otherwise leaves the current state alone —
// synthesis never regresses a pending or terminal state to in_progress.
func (t *Tracker) ToolUse(id, name, parentToolUseID, reported string) string {
	if id == "" {
		return reported
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.calls[id]
	if !ok {
		c = &call{id: id, name: name, parent: parentToolUseID, status: InProgress}
		t.calls[id] = c
		t.order = append(t.order, id)
	}
	if name != "" {
		c.name = name
	}
	if c.parent == "" {
		// A permission ask can be tracked before its call's tool_use line
		// (PermissionParked); the arrival fills in what the ask lacked.
		c.parent = parentToolUseID
	}
	if reported != "" {
		c.status = reported
		c.reported = true
	}
	return c.status
}

// ToolResult records the call's result arriving and returns the status to
// stamp on the result event: the provider-reported status when it states one
// (a Hermes completed-only update); else, on the result's error flag,
// "blocked" when the call never ran for want of permission (mads refused its
// ask, or text is a PermissionRefusal) and "failed" otherwise; else
// "completed". A result for an untracked id (a result-first wire, or a call
// whose tool_use this process never saw) is tracked from here so the
// turn-end sweep and later snapshots still know its fate.
func (t *Tracker) ToolResult(id string, isError bool, text, reported string) string {
	if id == "" {
		return reported
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.calls[id]
	if !ok {
		c = &call{id: id}
		t.calls[id] = c
		t.order = append(t.order, id)
	}
	c.status = reported
	if c.status == "" {
		c.status = resultStatus(isError, c.refused || PermissionRefusal(text))
	}
	c.reported = reported != ""
	return c.status
}

// resultStatus is the synthesized status a result implies.
func resultStatus(isError, refused bool) string {
	switch {
	case !isError:
		return Completed
	case refused:
		return Blocked
	default:
		return Failed
	}
}

// PermissionParked marks the call identified by id — the harness's ask
// names it exactly (provider.PermissionAsk.ToolUseID) — as "pending" behind
// a permission request and returns its update. The ask can arrive before
// the pump has processed the call's own tool_use line, so an untracked id
// is tracked from here (parent as the asker knew it; ToolUse fills in a
// missing one later). A terminal status is never overridden: ok is false
// then, and for an empty id.
func (t *Tracker) PermissionParked(id, parentToolUseID string) (Update, bool) {
	if id == "" {
		return Update{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.calls[id]
	if !ok {
		c = &call{id: id, parent: parentToolUseID}
		t.calls[id] = c
		t.order = append(t.order, id)
	}
	if Terminal(c.status) {
		return Update{}, false
	}
	if c.parent == "" {
		c.parent = parentToolUseID
	}
	c.status = Pending
	c.reported = false
	return Update{ToolUseID: c.id, ParentToolUseID: c.parent, Status: Pending}, true
}

// PermissionDenied records that mads refused the call's permission ask
// (denied, expired, or cancelled — the call will never run) and marks it
// "blocked" right away, returning the update; ok is false when the call
// isn't tracked or is already terminal. The harness's own error tool_result
// for the refused call follows and resolves to "blocked" too (ToolResult),
// whatever its text says.
func (t *Tracker) PermissionDenied(id string) (Update, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.calls[id]
	if !ok {
		return Update{}, false
	}
	c.refused = true
	if Terminal(c.status) {
		return Update{}, false
	}
	c.status = Blocked
	c.reported = false
	return Update{ToolUseID: c.id, ParentToolUseID: c.parent, Status: Blocked}, true
}

// PermissionResolved returns a parked call to "in_progress" once its
// permission request is allowed and execution proceeds. ok is false when the
// call isn't tracked or isn't pending (a refusal goes through
// PermissionDenied instead).
func (t *Tracker) PermissionResolved(id string) (Update, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.calls[id]
	if !ok || c.status != Pending {
		return Update{}, false
	}
	c.status = InProgress
	return Update{ToolUseID: c.id, ParentToolUseID: c.parent, Status: InProgress}, true
}

// TurnEnd closes out the turn: every tracked call not yet in a terminal
// state — no result arrived, whether the turn completed, was interrupted, or
// the session stopped — becomes "interrupted", returned in arrival order.
// All tracking state is then dropped; the next turn starts clean. Calling it
// again with nothing tracked is a no-op returning nil.
func (t *Tracker) TurnEnd() []Update {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Update
	for _, id := range t.order {
		c := t.calls[id]
		if Terminal(c.status) {
			continue
		}
		c.status = Interrupted
		out = append(out, Update{ToolUseID: c.id, ParentToolUseID: c.parent, Status: c.status})
	}
	t.calls = map[string]*call{}
	t.order = nil
	return out
}

// Snapshot returns every tracked call's current status in arrival order,
// without mutating anything — the history-merge read (a transcript refetch
// stamping statuses onto already-recorded tool messages), where no turn-end
// signal exists and open calls must stay honestly open, not be guessed
// interrupted.
func (t *Tracker) Snapshot() []Update {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Update, 0, len(t.order))
	for _, id := range t.order {
		c := t.calls[id]
		out = append(out, Update{ToolUseID: c.id, ParentToolUseID: c.parent, Status: c.status})
	}
	return out
}
