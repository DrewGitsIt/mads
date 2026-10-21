package codexcli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// closeGrace is how long Close lets app-server exit on stdin EOF before
// SIGKILLing its group.
const closeGrace = 3 * time.Second

// maxStreamedOutput caps the command output collected from outputDelta
// notifications per item, for the tool_result text when the completed item
// carries no aggregatedOutput.
const maxStreamedOutput = 64 * 1024

// tokenBreakdown is TokenUsageBreakdown: inputTokens INCLUDES
// cachedInputTokens, and outputTokens includes reasoningOutputTokens
// (run1: input 36253 with 21760 cached; output 122 with 44 reasoning).
type tokenBreakdown struct {
	TotalTokens           int `json:"totalTokens"`
	InputTokens           int `json:"inputTokens"`
	CachedInputTokens     int `json:"cachedInputTokens"`
	CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
	OutputTokens          int `json:"outputTokens"`
}

func (a tokenBreakdown) minus(b tokenBreakdown) (tokenBreakdown, bool) {
	d := tokenBreakdown{
		TotalTokens:           a.TotalTokens - b.TotalTokens,
		InputTokens:           a.InputTokens - b.InputTokens,
		CachedInputTokens:     a.CachedInputTokens - b.CachedInputTokens,
		CacheWriteInputTokens: a.CacheWriteInputTokens - b.CacheWriteInputTokens,
		OutputTokens:          a.OutputTokens - b.OutputTokens,
	}
	ok := d.TotalTokens >= 0 && d.InputTokens >= 0 && d.CachedInputTokens >= 0 && d.CacheWriteInputTokens >= 0 && d.OutputTokens >= 0
	return d, ok
}

// turnError is TurnError (turn/completed status "failed").
type turnError struct {
	Message        string          `json:"message"`
	CodexErrorInfo json.RawMessage `json:"codexErrorInfo,omitempty"`
}

// rateLimited reports whether codex classified the failure as a rate or
// usage limit (CodexErrorInfo "rateLimitExceeded" / "usageLimitExceeded").
func (e *turnError) rateLimited() bool {
	if e == nil {
		return false
	}
	var info string
	if json.Unmarshal(e.CodexErrorInfo, &info) == nil {
		return info == "rateLimitExceeded" || info == "usageLimitExceeded"
	}
	return false
}

// pendingAsk is one approval request forwarded to the PermissionAsker and
// not yet answered. Whoever deletes it from session.asks under s.mu — the
// asker's goroutine with a decision, or a withdrawal (serverRequest/resolved,
// Interrupt, process exit, Close) — owns the request's single reply.
type pendingAsk struct {
	id    json.RawMessage
	reply func(allow bool) any
	// abort is the reply this request's kind takes when the turn is
	// interrupted under it: each approval kind has its own response shape
	// (schema/*ApprovalResponse.json).
	abort  any
	cancel context.CancelFunc
}

// approvalReplies is one approval kind's answer shapes: reply for a
// decision, abort for an interrupt that withdrew the ask.
type approvalReplies struct {
	reply func(allow bool) any
	abort any
}

var (
	// item/commandExecution and item/fileChange: CommandExecutionApprovalDecision
	// / FileChangeApprovalDecision — accept, decline, cancel. Never
	// acceptForSession: mads asks per call, as on every harness.
	itemApproval = approvalReplies{
		reply: func(allow bool) any {
			if allow {
				return map[string]string{"decision": "accept"}
			}
			return map[string]string{"decision": "decline"}
		},
		abort: map[string]string{"decision": "cancel"},
	}
	// v1 execCommandApproval / applyPatchApproval: ReviewDecision —
	// approved, denied, abort.
	legacyApproval = approvalReplies{
		reply: func(allow bool) any {
			if allow {
				return map[string]string{"decision": "approved"}
			}
			return map[string]string{"decision": "denied"}
		},
		abort: map[string]string{"decision": "abort"},
	}
)

// permissionsApproval answers item/permissions/requestApproval
// (PermissionsRequestApprovalResponse: {permissions, scope}): an allow
// grants what was asked for this turn; a deny or an abort grants nothing —
// the shape has no decline of its own.
func permissionsApproval(requested json.RawMessage) approvalReplies {
	none := map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	return approvalReplies{
		reply: func(allow bool) any {
			if allow && len(requested) > 0 {
				return map[string]any{"permissions": requested, "scope": "turn"}
			}
			return none
		},
		abort: none,
	}
}

// session is one `codex app-server` process hosting one thread. The read
// goroutine (conn.read) is the sole writer to events and closes it when
// the process's stdout ends — a close with no EventTurnEnd before it is the
// process-died signal.
type session struct {
	p      *Provider
	c      *conn
	events chan provider.Event
	trust  provider.Trust
	asker  provider.PermissionAsker
	effort string
	// settle: the session asked for EventSettle (StartOptions.SettleHints).
	settle bool

	// sendMu serializes the turn-shaping calls (Send, Compact) so a second
	// Send sees the first one's turn as open, and lets Interrupt wait out a
	// turn/start still in flight.
	sendMu sync.Mutex
	// closing is closed by Close: emit stops waiting for a consumer.
	closing chan struct{}

	mu     sync.Mutex
	ref    string
	model  string
	closed bool

	// Turn state. turnOpen is set by Send/Compact before the request is
	// written and cleared by turn/completed; turnID is learned from
	// turn/started (wire order — never from the response, which a racing
	// turn/completed could overtake).
	turnOpen    bool
	turnID      string
	interrupted bool
	compacting  bool // the open turn is Compact's
	compactOpen bool // EventCompact start sent, done not yet
	compactSeen bool // this turn reported a contextCompaction item

	// Per-turn item bookkeeping, keyed by codex item id.
	items     map[string]*threadItem      // started tool items (approvals name them)
	usedTools map[string]bool             // EventToolUse already sent
	streamed  map[string]int              // agentMessage text bytes already sent
	output    map[string]*strings.Builder // commandExecution outputDelta text
	final     strings.Builder             // final_answer text this turn

	// Usage. total is per app-server process (a resumed thread starts again
	// from 0 — run3-resume), so each turn's usage is the difference from
	// the total at the previous turn end.
	totalSeen tokenBreakdown
	totalNow  tokenBreakdown
	lastCall  tokenBreakdown
	window    int
	usageSeen bool

	// lastFinal and lastErr are the previous completed turn's final answer
	// and failure, for OneShot.
	lastFinal string
	lastErr   *turnError

	sentIDs []string // clientUserMessageId per accepted Send (SentClientIDs)

	asks map[string]*pendingAsk
	// forwards counts asker goroutines still running (forward), so a
	// capture replay can let each ask be answered before codex's next line.
	forwards sync.WaitGroup
}

func newSession(p *Provider, c *conn, opts provider.StartOptions) *session {
	return &session{
		p:       p,
		c:       c,
		events:  make(chan provider.Event, 256),
		closing: make(chan struct{}),
		trust:   effectiveTrust(opts.Trust),
		asker:   opts.Permissions,
		effort:  p.cfg.Effort,
		settle:  opts.SettleHints,
		asks:    map[string]*pendingAsk{},
	}
}

// effectiveTrust is the level this adapter runs a requested trust at.
// auto_review is not declared (Matrix: codex's approvals reviewer is not
// proven to escalate to the user), so it runs as limited — the
// provider.EffectiveTrust rule, applied here too in case a caller hands it
// over anyway. An unknown value is limited as well, never full.
func effectiveTrust(t provider.Trust) provider.Trust {
	switch t {
	case "", provider.TrustFull:
		return provider.TrustFull
	case provider.TrustReadOnly:
		return provider.TrustReadOnly
	default:
		return provider.TrustLimited
	}
}

// trustPolicy maps a trust level onto codex's two knobs (thread/start
// sandbox + approvalPolicy):
//
//   - full: danger-full-access, never — codex's own --yolo pair; nothing
//     asks, nothing is sandboxed (claude-code's bypassPermissions).
//   - limited: workspace-write, untrusted — the OS sandbox confines writes
//     to the workspace, and codex asks before every edit and every command
//     outside its known-safe read-only list (captured: `touch a.txt` and an
//     apply_patch add both asked; `ls` never does). Those asks go to the
//     user; with no asker they are declined.
//   - read_only: read-only, untrusted — the OS sandbox (Seatbelt /
//     Landlock) refuses every write whatever anyone answers, and under a
//     read-only sandbox codex asks before every command, `ls` included
//     (captured, readonly-untrusted.jsonl); mads declines each ask itself,
//     never forwarding it. So nothing runs. (read-only + never would run
//     commands inside the sandbox without asking — reads, but still
//     command execution, which read_only denies.)
func trustPolicy(t provider.Trust) (sandbox, approval string) {
	switch t {
	case provider.TrustFull:
		return "danger-full-access", "never"
	case provider.TrustReadOnly:
		return "read-only", "untrusted"
	default:
		return "workspace-write", "untrusted"
	}
}

func (s *session) Ref() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ref
}

func (s *session) Mode() provider.Mode { return provider.ModeACP }

func (s *session) Events() <-chan provider.Event { return s.events }

func newClientID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "mads-" + hex.EncodeToString(b[:])
}

// Send starts a turn (turn/start) — or, while one is running, joins it
// (turn/steer): the message lands inside the running turn as a userMessage
// item and that turn still ends once, the way a claude-code message sent
// mid-turn is absorbed by the turn in flight (claudecode session.Send).
// Captured: a steer mid-count produced one more userMessage item in the
// same turn id and a single turn/completed (testdata/captures/
// steer-compact.jsonl).
func (s *session) Send(ctx context.Context, text string) error {
	return s.send(ctx, text, newClientID())
}

// SendKeyed implements provider.KeyedSender: Send, with id as the message's
// clientUserMessageId on turn/start — or on turn/steer, which takes one too
// (schema v2 TurnSteerParams) — so codex stores id as the user item's
// clientId and the transcript keys the row "user:"+id.
func (s *session) SendKeyed(ctx context.Context, text, id string) error {
	if id == "" {
		id = newClientID()
	}
	return s.send(ctx, text, id)
}

func (s *session) send(ctx context.Context, text, cid string) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("codex session %s is closed", s.ref)
	}
	open, compacting, ref := s.turnOpen, s.compacting, s.ref
	s.mu.Unlock()
	input := []map[string]any{{"type": "text", "text": text}}
	// One id per message, kept: codex persists it on the userMessage item
	// (clientId) live and in thread/items/list, which is what keys the
	// stored user row "user:"+cid (userKey).

	if open {
		if compacting {
			// A compaction turn takes no input.
			return fmt.Errorf("codex is compacting: %w", provider.ErrTurnInFlight)
		}
		turnID := s.awaitTurnID(2 * time.Second)
		if turnID == "" {
			return fmt.Errorf("turn started but its id never arrived: %w", provider.ErrTurnInFlight)
		}
		_, err := s.c.request(ctx, "turn/steer", map[string]any{
			"threadId": ref, "expectedTurnId": turnID, "input": input, "clientUserMessageId": cid,
		})
		if err == nil {
			s.recordSent(cid)
			return nil
		}
		s.mu.Lock()
		still := s.turnOpen && s.turnID == turnID
		s.mu.Unlock()
		if still {
			return fmt.Errorf("turn/steer: %w", err)
		}
		// The turn ended between our look and codex's: start a new one.
	}

	s.mu.Lock()
	s.turnOpen = true
	s.interrupted = false
	s.mu.Unlock()
	params := map[string]any{"threadId": ref, "input": input, "clientUserMessageId": cid}
	if s.effort != "" {
		params["effort"] = s.effort
	}
	if _, err := s.c.request(ctx, "turn/start", params); err != nil {
		s.mu.Lock()
		if s.turnID == "" {
			s.turnOpen = false
		}
		s.mu.Unlock()
		return fmt.Errorf("turn/start: %w", err)
	}
	s.recordSent(cid)
	return nil
}

// recordSent keeps the clientUserMessageId of a message codex accepted.
func (s *session) recordSent(cid string) {
	s.mu.Lock()
	s.sentIDs = append(s.sentIDs, cid)
	s.mu.Unlock()
}

// SentClientIDs returns the clientUserMessageId of every message this
// session sent (turn/start or turn/steer, Send or SendKeyed), in order —
// each one the id in its stored user row's key ("user:"+id).
func (s *session) SentClientIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sentIDs...)
}

// awaitTurnID returns the open turn's id, waiting up to d for turn/started
// when a turn/start has been answered but its notification not yet read.
// "" when no turn is open or the id never came.
func (s *session) awaitTurnID(d time.Duration) string {
	deadline := time.Now().Add(d)
	for {
		s.mu.Lock()
		open, id := s.turnOpen, s.turnID
		s.mu.Unlock()
		if !open || id != "" || time.Now().After(deadline) {
			return id
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Interrupt stops the running turn (turn/interrupt). codex ends it with
// turn/completed status "interrupted" — the turn end carries Interrupted —
// and the thread serves the next Send. Whatever text the turn had streamed
// is never stored by codex (survey finding 6). Asks still open are
// answered with their kind's abort (cancel / abort / an empty grant).
func (s *session) Interrupt(ctx context.Context) error {
	s.sendMu.Lock() // a turn/start in flight lands first
	s.mu.Lock()
	open, ref := s.turnOpen, s.ref
	s.mu.Unlock()
	s.sendMu.Unlock()
	if !open {
		return nil
	}
	turnID := s.awaitTurnID(2 * time.Second)
	// Only now, with the turn named and still running, is it this call's to
	// mark: a flag set before giving up would mislabel whichever turn
	// ended next.
	s.mu.Lock()
	switch {
	case !s.turnOpen:
		s.mu.Unlock()
		return nil // it ended meanwhile
	case turnID == "" || s.turnID != turnID:
		s.mu.Unlock()
		return errors.New("turn/interrupt: the running turn's id never arrived")
	}
	s.interrupted = true
	s.mu.Unlock()
	s.withdrawAsks(true)
	if _, err := s.c.request(ctx, "turn/interrupt", map[string]any{"threadId": ref, "turnId": turnID}); err != nil {
		s.mu.Lock()
		still := s.turnOpen && s.turnID == turnID
		if still {
			s.interrupted = false // codex never took it: the turn runs on
		}
		s.mu.Unlock()
		if !still {
			return nil // it finished before codex saw the interrupt
		}
		return fmt.Errorf("turn/interrupt: %w", err)
	}
	return nil
}

// Compact implements provider.Compactor with thread/compact/start, which
// runs codex's own compaction as a turn of its own: turn/started, a
// contextCompaction item, turn/completed (captured, steer-compact.jsonl).
// codex takes no instructions for it, so a focused compaction is refused
// rather than run unfocused.
func (s *session) Compact(ctx context.Context, instructions string) error {
	if strings.TrimSpace(instructions) != "" {
		return fmt.Errorf("compaction instructions: %w", provider.ErrUnsupported)
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("codex session %s is closed", s.ref)
	}
	if s.turnOpen {
		s.mu.Unlock()
		return provider.ErrTurnInFlight
	}
	s.turnOpen, s.compacting, s.interrupted = true, true, false
	ref := s.ref
	s.mu.Unlock()
	if _, err := s.c.request(ctx, "thread/compact/start", map[string]any{"threadId": ref}); err != nil {
		s.mu.Lock()
		if s.turnID == "" {
			s.turnOpen, s.compacting = false, false
		}
		s.mu.Unlock()
		return fmt.Errorf("thread/compact/start: %w", err)
	}
	return nil
}

// Close ends the session: open asks are withdrawn, stdin EOF asks
// app-server to exit, a laggard is killed with its group, and every tool
// process codex spawned in a group of its own is reaped (reap.go). Ref
// stays valid for a later resume — the rollout is codex's, on disk.
func (s *session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ref := s.ref
	close(s.closing)
	s.mu.Unlock()
	s.withdrawAsks(false)
	s.c.shutdown(closeGrace)
	s.p.forget(ref, s)
	return nil
}

// onExit runs on the read goroutine once the process is gone. A death
// mads did not cause is reported with codex's stderr tail before the
// stream closes (the close itself is the process-died signal).
func (s *session) onExit() {
	s.withdrawAsks(false)
	s.mu.Lock()
	ref, closed := s.ref, s.closed
	s.mu.Unlock()
	if !closed {
		msg := "codex app-server exited"
		if tail := s.c.stderr.String(); tail != "" {
			msg += ": " + tail
		}
		s.emit(provider.Event{Kind: provider.EventError, Text: msg})
	}
	s.p.forget(ref, s)
	close(s.events)
}

// emit delivers one event from the read goroutine, on acprpc's policy: a
// text chunk is dropped rather than block the reader when the consumer is
// 256 events behind (the stored transcript still has the whole reply);
// every other event waits for room — but never past Close, so a consumer
// that stopped reading cannot wedge the read loop and with it Close.
func (s *session) emit(ev provider.Event) {
	if ev.Kind == provider.EventText {
		select {
		case s.events <- ev:
		default:
		}
		return
	}
	select {
	case s.events <- ev:
	case <-s.closing:
	}
}

// --- notifications ---------------------------------------------------------

type itemParams struct {
	ThreadID string     `json:"threadId"`
	TurnID   string     `json:"turnId"`
	Item     threadItem `json:"item"`
}

type deltaParams struct {
	ThreadID string `json:"threadId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type turnParams struct {
	ThreadID string `json:"threadId"`
	Turn     struct {
		ID     string     `json:"id"`
		Status string     `json:"status"`
		Error  *turnError `json:"error"`
	} `json:"turn"`
}

type tokenUsageParams struct {
	ThreadID   string `json:"threadId"`
	TokenUsage struct {
		Total              tokenBreakdown `json:"total"`
		Last               tokenBreakdown `json:"last"`
		ModelContextWindow int            `json:"modelContextWindow"`
	} `json:"tokenUsage"`
}

// threadOf pulls the threadId out of a notification's params; "" when it
// has none (account/*, remoteControl/*).
func threadOf(params json.RawMessage) string {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(params, &p)
	return p.ThreadID
}

// handleNotification maps one app-server notification onto events. Only
// this session's own thread counts: a multi-agent run streams its
// sub-agents' threads on the same connection, and none of that is the
// main agent's turn.
func (s *session) handleNotification(method string, params json.RawMessage) {
	if method == "serverRequest/resolved" {
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(params, &p) == nil && len(p.RequestID) > 0 {
			s.withdrawAsk(string(p.RequestID))
		}
		return
	}
	tid := threadOf(params)
	s.mu.Lock()
	ref := s.ref
	s.mu.Unlock()
	if tid == "" || ref == "" || tid != ref {
		return
	}
	switch method {
	case "turn/started":
		var p turnParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		s.mu.Lock()
		s.turnOpen = true
		s.turnID = p.Turn.ID
		s.mu.Unlock()
	case "turn/completed":
		var p turnParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		s.finishTurn(p.Turn.Status, p.Turn.Error)
	case "item/started":
		var p itemParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		s.itemStarted(&p.Item)
	case "item/completed":
		var p itemParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		s.itemCompleted(&p.Item)
	case "item/agentMessage/delta":
		var p deltaParams
		if json.Unmarshal(params, &p) != nil || p.Delta == "" {
			return
		}
		s.text(p.ItemID, p.Delta)
	case "item/commandExecution/outputDelta":
		var p deltaParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		s.mu.Lock()
		if s.output == nil {
			s.output = map[string]*strings.Builder{}
		}
		b := s.output[p.ItemID]
		if b == nil {
			b = &strings.Builder{}
			s.output[p.ItemID] = b
		}
		if b.Len() < maxStreamedOutput {
			b.WriteString(p.Delta)
		}
		s.mu.Unlock()
	case "thread/tokenUsage/updated":
		var p tokenUsageParams
		if json.Unmarshal(params, &p) != nil {
			return
		}
		s.mu.Lock()
		s.totalNow = p.TokenUsage.Total
		s.lastCall = p.TokenUsage.Last
		if p.TokenUsage.ModelContextWindow > 0 {
			s.window = p.TokenUsage.ModelContextWindow
		}
		s.usageSeen = true
		s.mu.Unlock()
	case "error":
		// A turn's failure arrives again on its turn/completed (status
		// "failed", error set), which is where it is reported. Only an error
		// outside any turn surfaces here.
		var p struct {
			Error     turnError `json:"error"`
			WillRetry bool      `json:"willRetry"`
		}
		if json.Unmarshal(params, &p) != nil || p.WillRetry {
			return
		}
		s.mu.Lock()
		open := s.turnOpen
		s.mu.Unlock()
		if !open && p.Error.Message != "" {
			s.emit(provider.Event{Kind: provider.EventError, Text: p.Error.Message})
		}
	}
}

// text emits one agentMessage delta, keyed by the item. Each agentMessage
// is an item of its own, live as stored: two in a row (commentary, then the
// final answer) are two keyed runs with nothing added between them — the
// UI's join rule (docs/CHAT-STREAM-DESIGN.md §6) decides how adjacent text
// items read, the same way live and stored.
func (s *session) text(itemID, delta string) {
	s.mu.Lock()
	if s.streamed == nil {
		s.streamed = map[string]int{}
	}
	s.streamed[itemID] += len(delta)
	s.mu.Unlock()
	s.emit(provider.Event{Kind: provider.EventText, Key: itemID, Text: delta})
}

func (s *session) itemStarted(it *threadItem) {
	switch {
	case it.Type == "contextCompaction":
		s.mu.Lock()
		s.compactOpen, s.compactSeen = true, true
		s.mu.Unlock()
		s.emit(provider.Event{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "start"}})
	case it.Type == "webSearch":
		// Announced at completion: item/started carries an empty query
		// (run1), and a tool row with no title says nothing.
		s.rememberItem(it)
	case it.isTool():
		s.rememberItem(it)
		if it.Type == "commandExecution" {
			// The command's process exists a moment after codex announces
			// it; look then, so a crash of app-server mid-command still
			// leaves the reaper knowing what to kill.
			s.c.reaper.snapshotSoon(300 * time.Millisecond)
		}
		s.emitToolUse(it)
	}
}

func (s *session) rememberItem(it *threadItem) {
	cp := *it
	s.mu.Lock()
	if s.items == nil {
		s.items = map[string]*threadItem{}
	}
	s.items[it.ID] = &cp
	s.mu.Unlock()
}

func (s *session) emitToolUse(it *threadItem) {
	s.mu.Lock()
	if s.usedTools == nil {
		s.usedTools = map[string]bool{}
	}
	if s.usedTools[it.ID] {
		s.mu.Unlock()
		return
	}
	s.usedTools[it.ID] = true
	s.mu.Unlock()
	tool := it.toolUse()
	if it.Status == "inProgress" {
		tool.Status = provider.ToolStatusInProgress
	}
	s.emit(provider.Event{Kind: provider.EventToolUse, Key: it.ID, Tool: tool})
}

// itemCompleted finishes an item's live events and, for a session that
// asked for settle hints, follows them with an EventSettle keyed to the
// item: codex writes an item to its store as it completes it (survey: 0–27
// ms behind item/completed), and never before.
func (s *session) itemCompleted(it *threadItem) {
	s.finishItem(it)
	if s.settle {
		s.emit(provider.Event{Kind: provider.EventSettle, Key: itemKey(it)})
	}
}

func (s *session) finishItem(it *threadItem) {
	switch {
	case it.Type == "agentMessage":
		s.mu.Lock()
		sent := s.streamed[it.ID]
		if it.Phase == "final_answer" || it.Phase == "" {
			if s.final.Len() > 0 {
				s.final.WriteString("\n\n")
			}
			s.final.WriteString(it.Text)
		}
		s.mu.Unlock()
		// Whatever the deltas didn't carry (all of it, when none came).
		if sent < len(it.Text) {
			s.text(it.ID, it.Text[sent:])
		}
	case it.Type == "reasoning":
		if text := it.reasoningText(); text != "" {
			// Whole, at completion: the summary deltas are not forwarded, so
			// one reasoning item is one event and can't interleave.
			s.emit(provider.Event{Kind: provider.EventThinking, Key: it.ID, Text: text})
		}
	case it.Type == "contextCompaction":
		s.mu.Lock()
		was := s.compactOpen
		s.compactOpen = false
		s.mu.Unlock()
		if was {
			s.emit(provider.Event{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "done", Success: true}})
		}
	case it.isTool():
		s.emitToolUse(it) // webSearch's only announcement; a no-op for the rest
		s.mu.Lock()
		streamed := ""
		if b := s.output[it.ID]; b != nil {
			streamed = b.String()
		}
		delete(s.output, it.ID)
		delete(s.items, it.ID)
		s.mu.Unlock()
		text, isError, status := it.toolResult(streamed)
		tool := it.toolUse()
		s.emit(provider.Event{
			Kind:    provider.EventToolResult,
			Key:     it.ID,
			Tool:    &provider.ToolUse{ID: it.ID, Name: tool.Name, Kind: tool.Kind, Status: status},
			Text:    text,
			IsError: isError,
		})
	}
}

// finishTurn closes a turn on turn/completed: the compaction outcome when
// it was one, the failure when it failed, then EventTurnEnd with the turn's
// usage. Every turn/completed for this thread ends a turn, whether or not
// mads started it — the turn-termination contract.
func (s *session) finishTurn(status string, terr *turnError) {
	s.mu.Lock()
	interrupted := s.interrupted || status == "interrupted"
	compacting, compactOpen, compactSeen := s.compacting, s.compactOpen, s.compactSeen
	var usage *provider.Usage
	if s.usageSeen {
		d, ok := s.totalNow.minus(s.totalSeen)
		if !ok {
			d = s.totalNow
		}
		usage = &provider.Usage{
			InputTokens:              d.InputTokens - d.CachedInputTokens,
			OutputTokens:             d.OutputTokens,
			CacheReadInputTokens:     d.CachedInputTokens,
			CacheCreationInputTokens: d.CacheWriteInputTokens,
			Model:                    s.model,
			// The final call's whole context — what the next turn starts
			// from. After a compaction it is the compacted size (last
			// totalTokens 4900, inputTokens 0 — steer-compact.jsonl).
			ContextTokens: s.lastCall.TotalTokens,
			ContextWindow: s.window,
		}
		if usage.InputTokens < 0 {
			usage.InputTokens = 0
		}
		s.totalSeen = s.totalNow
	}
	s.lastFinal = s.final.String()
	s.lastErr = nil
	if status == "failed" {
		s.lastErr = terr
		if s.lastErr == nil {
			s.lastErr = &turnError{Message: "turn failed"}
		}
	}
	s.turnOpen, s.turnID, s.interrupted = false, "", false
	s.compacting, s.compactOpen, s.compactSeen = false, false, false
	s.items, s.usedTools, s.streamed, s.output = nil, nil, nil, nil
	settle := s.settle
	s.final.Reset()
	s.mu.Unlock()

	if compacting || compactOpen {
		done := &provider.CompactStatus{Phase: "done", Success: status == "completed" && !interrupted}
		switch {
		case interrupted:
			done.Error = "compaction interrupted"
		case terr != nil && terr.Message != "":
			done.Error = terr.Message
		case !done.Success:
			done.Error = "compaction " + status
		}
		if compacting && !compactSeen {
			s.emit(provider.Event{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "start"}})
		}
		if compactOpen || (compacting && !compactSeen) {
			s.emit(provider.Event{Kind: provider.EventCompact, Compact: done})
		}
	}
	if status == "failed" {
		msg := "turn failed"
		if terr != nil && terr.Message != "" {
			msg = terr.Message
		}
		s.emit(provider.Event{Kind: provider.EventError, Text: msg})
	}
	if settle {
		// Every item the turn completed is stored by now.
		s.emit(provider.Event{Kind: provider.EventSettle})
	}
	s.emit(provider.Event{Kind: provider.EventTurnEnd, Interrupted: interrupted, Usage: usage})
}

// --- server requests (approvals) ---------------------------------------------

type approvalParams struct {
	ThreadID    string          `json:"threadId"`
	ItemID      string          `json:"itemId"`
	CallID      string          `json:"callId"` // legacy v1 requests
	Command     json.RawMessage `json:"command"`
	Cwd         string          `json:"cwd"`
	Reason      string          `json:"reason"`
	Permissions json.RawMessage `json:"permissions"`
}

// handleRequest answers a server→client request. Approvals are decided by
// trust — mads asks the user only when codex itself asked
// (provider.PermissionAsker): full allows, read_only declines, limited
// forwards to the asker or, with none, declines so the turn never stalls.
// Every other request kind is answered at once with a refusal; nothing is
// left unanswered.
func (s *session) handleRequest(id json.RawMessage, method string, params json.RawMessage) {
	var p approvalParams
	_ = json.Unmarshal(params, &p)
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		s.decide(id, s.askFor(method, p), itemApproval)
	case "item/permissions/requestApproval":
		// The agent wants more than its sandbox gives it (network, a path
		// outside the workspace) for this turn.
		ask := provider.PermissionAsk{ToolUseID: p.ItemID, Tool: "request_permissions", Input: p.Permissions, Reason: p.Reason}
		s.decide(id, ask, permissionsApproval(p.Permissions))
	case "execCommandApproval", "applyPatchApproval":
		// The v1 forms of the same two asks; app-server 0.154 sends the
		// item/* ones, these only stay answered correctly.
		tool := "shell"
		if method == "applyPatchApproval" {
			tool = "apply_patch"
		}
		ask := provider.PermissionAsk{ToolUseID: p.CallID, Tool: tool, Input: params, Reason: p.Reason}
		s.decide(id, ask, legacyApproval)
	case "mcpServer/elicitation/request":
		s.c.respond(id, map[string]string{"action": "decline"})
	case "item/tool/requestUserInput":
		s.c.respond(id, map[string]any{"answers": map[string]any{}})
	default:
		// item/tool/call (mads registers no dynamic tools),
		// account/chatgptAuthTokens/refresh (codex owns its login),
		// attestation/generate, and anything newer.
		s.c.respondError(id, errMethodNotFound, "mads does not handle "+method)
	}
}

// askFor builds the PermissionAsk for an item approval from the item codex
// announced just before asking (item/started precedes requestApproval —
// limited-decline.jsonl), falling back to the request's own fields.
func (s *session) askFor(method string, p approvalParams) provider.PermissionAsk {
	ask := provider.PermissionAsk{ToolUseID: p.ItemID, Reason: p.Reason}
	s.mu.Lock()
	it := s.items[p.ItemID]
	s.mu.Unlock()
	if it != nil {
		tu := it.toolUse()
		ask.Tool, ask.Input = tu.Name, tu.Input
		return ask
	}
	if method == "item/fileChange/requestApproval" {
		ask.Tool = "apply_patch"
		return ask
	}
	var cmd string
	_ = json.Unmarshal(p.Command, &cmd)
	ask.Tool = "shell"
	ask.Input = mustJSON(map[string]string{"command": cmd, "cwd": p.Cwd})
	return ask
}

func (s *session) decide(id json.RawMessage, ask provider.PermissionAsk, r approvalReplies) {
	s.mu.Lock()
	trust, asker := s.trust, s.asker
	s.mu.Unlock()
	switch {
	case trust == provider.TrustFull:
		s.c.respond(id, r.reply(true))
	case trust == provider.TrustReadOnly || asker == nil:
		s.c.respond(id, r.reply(false))
	default:
		s.forward(id, ask, asker, r)
	}
}

// forward hands one ask to the user on its own goroutine — never the read
// loop, which must keep delivering notifications (and a later
// serverRequest/resolved) while the user thinks.
func (s *session) forward(id json.RawMessage, ask provider.PermissionAsk, asker provider.PermissionAsker, r approvalReplies) {
	key := string(id)
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return
	}
	s.asks[key] = &pendingAsk{id: id, reply: r.reply, abort: r.abort, cancel: cancel}
	s.mu.Unlock()
	s.forwards.Add(1)
	go func() {
		defer s.forwards.Done()
		decision := asker(ctx, ask)
		s.mu.Lock()
		pa, ok := s.asks[key]
		delete(s.asks, key)
		s.mu.Unlock()
		cancel()
		if !ok {
			return // withdrawn while the user decided
		}
		s.c.respond(pa.id, pa.reply(decision.Allow))
	}()
}

// withdrawAsk drops one forwarded ask codex no longer waits for
// (serverRequest/resolved), cancelling the asker without a reply.
func (s *session) withdrawAsk(key string) {
	s.mu.Lock()
	pa, ok := s.asks[key]
	delete(s.asks, key)
	s.mu.Unlock()
	if ok {
		pa.cancel()
	}
}

// withdrawAsks withdraws every forwarded ask. With cancelReply set
// (Interrupt) each still gets its kind's abort answer (pendingAsk.abort); a
// dying or closing session replies to nothing.
func (s *session) withdrawAsks(cancelReply bool) {
	s.mu.Lock()
	asks := make([]*pendingAsk, 0, len(s.asks))
	for k, pa := range s.asks {
		asks = append(asks, pa)
		delete(s.asks, k)
	}
	s.mu.Unlock()
	for _, pa := range asks {
		pa.cancel()
		if cancelReply {
			s.c.respond(pa.id, pa.abort)
		}
	}
}
