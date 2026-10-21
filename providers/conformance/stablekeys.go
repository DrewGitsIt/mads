package conformance

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// The stable_keys probe (docs/CHAT-STREAM-DESIGN.md §1). Three scripted turns
// on one session started with SettleHints: a plain reply (Script.Echo), a
// reply with a tool call between two text parts (Script.ToolTurn), and a
// turn interrupted while text streams (Script.Slow). For every item both
// streamed and stored, the key and the text must agree; the store must list
// a turn's items in the order they streamed; chained TranscriptSince reads
// must add up to Transcript.
//
// Items on only one side are allowed only where the agent genuinely keeps
// them on one side: the interrupted turn's in-flight item, notices
// (Message.Notice), and live thinking (transcript rows carry no thinking).
// Anything else one-sided in the first two turns fails: that is a dropped or
// renamed item, which is exactly what the probe exists to catch.

// Roles the probe compares. Thinking is live-only by the Message model.
const (
	roleText       = "agent"
	roleTool       = "tool"
	roleToolResult = "tool_result"
	roleThinking   = "thinking"
	roleUser       = "user"
)

type liveItem struct {
	key, role, text, tool string
}

func (i liveItem) id() string { return i.role + "\x00" + i.key }

type keyedTurn struct {
	name      string
	prompt    string
	interrupt bool
	userID    string
	items     []liveItem
	inflight  string // the in-flight item's id at the interrupt
	settles   int
}

func (c *checker) storeWait() time.Duration {
	if c.s.StoreWait > 0 {
		return c.s.StoreWait
	}
	return 10 * time.Second
}

// CheckStableKeys runs only the stable_keys probe — the entry point for an
// adapter's real-binary smoke (MADS_*_SMOKE), where the full suite would be
// slow and costly. The Result is what Check would report for the row.
func CheckStableKeys(ctx context.Context, p provider.Provider, s Script) Result {
	m := p.Matrix()
	if err := m.Validate(); err != nil {
		return Result{Behavior: MatrixResult, Err: err}
	}
	c := checker{p: p, m: m, s: s, ctx: ctx}
	note, err := c.stableKeys(m[provider.BehaviorACP].Works())
	return Result{Behavior: provider.BehaviorStableKeys, Declared: m[provider.BehaviorStableKeys], Err: err, Note: note}
}

// stableKeys returns the probe's verdict and a note on what it covered.
func (c *checker) stableKeys(structured bool) (string, error) {
	var errs []string
	if c.unaskedSettle {
		errs = append(errs, "an EventSettle reached a session started without StartOptions.SettleHints")
	}
	fail := func(note string) (string, error) {
		if len(errs) == 0 {
			return note, nil
		}
		return note, errors.New(strings.Join(errs, "; "))
	}
	if !structured {
		return fail("not probed: keys are a structured-transport contract")
	}
	if !c.m[provider.BehaviorStableKeys].Works() {
		if err := c.noKeys(); err != nil {
			errs = append(errs, err.Error())
		}
		return fail("")
	}
	reader, ok := c.p.(provider.TranscriptReader)
	if !ok || !c.m[provider.BehaviorTranscript].Works() {
		errs = append(errs, "declared working but stable keys need a working TranscriptReader")
		return fail("")
	}
	if c.s.ToolTurn == nil {
		errs = append(errs, "declared working but the Script has no ToolTurn (text, tool, text) to probe with")
		return fail("")
	}

	sess, err := c.p.Start(c.ctx, provider.StartOptions{Mode: provider.ModeACP, SettleHints: true})
	if err != nil {
		errs = append(errs, fmt.Sprintf("Start(acp, SettleHints): %v", err))
		return fail("")
	}
	defer func() { _ = sess.Close() }()
	_, keyed := sess.(provider.KeyedSender)
	since, incremental := c.p.(provider.IncrementalTranscriptReader)

	turns := []*keyedTurn{
		{name: "plain", prompt: c.s.Echo("mk-keys-plain")},
		{name: "text-tool-text", prompt: c.s.ToolTurn()},
		{name: "interrupt", prompt: c.s.Slow(), interrupt: true},
	}
	var (
		stored  []provider.Message
		cursor  provider.TranscriptCursor
		chained []provider.Message
	)
	for i, t := range turns {
		if keyed {
			t.userID = newUUID()
		}
		if err := c.runKeyedTurn(sess, t); err != nil {
			errs = append(errs, fmt.Sprintf("turn %q: %v", t.name, err))
			return fail("")
		}
		if t.settles == 0 {
			errs = append(errs, fmt.Sprintf("turn %q: no EventSettle with SettleHints on", t.name))
		}
		stored = c.awaitStored(reader, sess.Ref(), turns[:i+1])
		if incremental {
			rows, next, err := since.TranscriptSince(c.ctx, sess.Ref(), cursor)
			if err != nil {
				errs = append(errs, fmt.Sprintf("TranscriptSince after turn %q: %v", t.name, err))
				incremental = false
			} else {
				chained, cursor = append(chained, rows...), next
			}
		}
	}

	errs = append(errs, compareKeyed(turns, stored, keyed)...)
	if incremental {
		final, err := reader.Transcript(c.ctx, sess.Ref())
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("Transcript: %v", err))
		default:
			if diff := sameRows(chained, final); diff != "" {
				errs = append(errs, "chained TranscriptSince != Transcript: "+diff)
			}
		}
	}
	note := fmt.Sprintf("3 turns; user ids round-trip: %v; incremental reader: %v", keyed, incremental)
	if !keyed {
		note += " (user rows keyed natively; the session child matches them on settle)"
	}
	return fail(note)
}

// noKeys holds an unsupported declaration to its word: a plain turn carries
// no keys, live or stored.
func (c *checker) noKeys() error {
	sess, err := c.p.Start(c.ctx, provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		return fmt.Errorf("Start(acp): %w", err)
	}
	defer func() { _ = sess.Close() }()
	t := &keyedTurn{name: "plain", prompt: c.s.Echo("mk-keys-none")}
	var keyedEvents int
	if err := sess.Send(c.ctx, t.prompt); err != nil {
		return fmt.Errorf("Send: %w", err)
	}
	deadline := time.After(c.s.timeout())
	for done := false; !done; {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return errors.New("event stream closed before turn end")
			}
			if ev.Key != "" {
				keyedEvents++
			}
			done = ev.Kind == provider.EventTurnEnd
		case <-deadline:
			return fmt.Errorf("no turn end within %s", c.s.timeout())
		}
	}
	if keyedEvents > 0 {
		return fmt.Errorf("declared unsupported but %d live events carry a Key", keyedEvents)
	}
	if reader, ok := c.p.(provider.TranscriptReader); ok {
		msgs, _ := reader.Transcript(c.ctx, sess.Ref())
		for _, m := range msgs {
			if m.Key != "" {
				return fmt.Errorf("declared unsupported but stored row %q carries Key %q", m.Role, m.Key)
			}
		}
	}
	return nil
}

// runKeyedTurn sends t's prompt and records its keyed items through turn end,
// enforcing the per-event half of the contract: every item event has a key,
// and a text or thinking key never reappears once another item has started.
func (c *checker) runKeyedTurn(sess provider.Session, t *keyedTurn) error {
	var err error
	if t.userID != "" {
		err = sess.(provider.KeyedSender).SendKeyed(c.ctx, t.prompt, t.userID)
	} else {
		err = sess.Send(c.ctx, t.prompt)
	}
	if err != nil {
		return fmt.Errorf("Send: %w", err)
	}
	index := map[string]int{} // item id -> position in t.items
	last := ""                // id of the item the latest event belonged to
	interrupted := false
	add := func(it liveItem) {
		if pos, ok := index[it.id()]; ok {
			t.items[pos].text += it.text
			return
		}
		index[it.id()] = len(t.items)
		t.items = append(t.items, it)
	}
	deadline := time.After(c.s.timeout())
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return errors.New("event stream closed before turn end")
			}
			if ev.ParentToolUseID != "" {
				continue // a subagent's sidechain is not this conversation
			}
			switch ev.Kind {
			case provider.EventText, provider.EventThinking:
				if ev.Kind == provider.EventThinking && ev.Text == "" {
					continue // a token-count tick, not content
				}
				role := roleText
				if ev.Kind == provider.EventThinking {
					role = roleThinking
				}
				it := liveItem{key: ev.Key, role: role, text: ev.Text}
				if ev.Key == "" {
					return fmt.Errorf("%s event without a Key", ev.Kind)
				}
				if _, seen := index[it.id()]; seen && last != it.id() {
					return fmt.Errorf("%s key %q reappears after item %q started: the chunks of one item must be contiguous", role, ev.Key, last)
				}
				add(it)
				last = it.id()
				if t.interrupt && !interrupted {
					interrupted, t.inflight = true, it.id()
					if err := sess.Interrupt(c.ctx); err != nil {
						return fmt.Errorf("Interrupt: %w", err)
					}
				}
			case provider.EventToolUse:
				if ev.Key == "" {
					return errors.New("tool_use event without a Key")
				}
				it := liveItem{key: ev.Key, role: roleTool}
				if ev.Tool != nil {
					it.tool = ev.Tool.Name
				}
				add(it)
				last = it.id()
			case provider.EventToolResult:
				if ev.Key == "" {
					return errors.New("tool_result event without a Key")
				}
				add(liveItem{key: ev.Key, role: roleToolResult, text: ev.Text})
				last = roleToolResult + "\x00" + ev.Key
			case provider.EventSettle:
				t.settles++
			case provider.EventTurnEnd:
				if t.interrupt && !interrupted {
					return errors.New("the slow turn ended before any text streamed, so nothing was interrupted")
				}
				return nil
			case provider.EventError:
				if t.interrupt && interrupted {
					// How some agents close an interrupted turn (claude's
					// error_during_execution result): the interrupt
					// working, not a failure of the turn.
					continue
				}
				return fmt.Errorf("provider error mid-turn: %s", ev.Text)
			}
		case <-deadline:
			return fmt.Errorf("no turn end within %s", c.s.timeout())
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
	}
}

// awaitStored polls the store until every item the turns streamed (bar the
// exempt ones) is there, or the store wait runs out; the comparison reports
// whatever is still missing.
func (c *checker) awaitStored(reader provider.TranscriptReader, ref string, turns []*keyedTurn) []provider.Message {
	want := map[string]bool{}
	for _, t := range turns {
		for _, it := range t.items {
			if it.role != roleThinking && it.id() != t.inflight {
				want[it.id()] = true
			}
		}
	}
	var msgs []provider.Message
	for deadline := time.Now().Add(c.storeWait()); ; time.Sleep(100 * time.Millisecond) {
		msgs, _ = reader.Transcript(c.ctx, ref)
		have := map[string]bool{}
		users := 0
		for _, m := range msgs {
			have[m.Role+"\x00"+m.Key] = true
			if m.Role == roleUser && !m.Notice {
				users++
			}
		}
		complete := users >= len(turns)
		for id := range want {
			complete = complete && have[id]
		}
		if complete || time.Now().After(deadline) || c.ctx.Err() != nil {
			return msgs
		}
	}
}

// compareKeyed checks the stored rows against what the turns streamed.
func compareKeyed(turns []*keyedTurn, stored []provider.Message, keyed bool) []string {
	var errs []string
	seen := map[string]bool{}
	for _, m := range stored {
		if !comparedRole(m.Role) {
			continue
		}
		if m.Key == "" {
			errs = append(errs, fmt.Sprintf("stored %s row without a Key (%.40q)", m.Role, m.Text))
			continue
		}
		if id := m.Role + "\x00" + m.Key; seen[id] {
			errs = append(errs, fmt.Sprintf("stored %s key %q appears twice", m.Role, m.Key))
		} else {
			seen[id] = true
		}
	}

	// Split the store into turns at each user row.
	var parts [][]provider.Message
	var users []provider.Message
	for _, m := range stored {
		switch {
		case m.Role == roleUser && !m.Notice:
			users = append(users, m)
			parts = append(parts, nil)
		case len(parts) > 0 && comparedRole(m.Role):
			parts[len(parts)-1] = append(parts[len(parts)-1], m)
		}
	}
	if len(users) != len(turns) {
		return append(errs, fmt.Sprintf("store has %d user rows for %d probe turns", len(users), len(turns)))
	}

	for i, t := range turns {
		if t.userID != "" && users[i].Key != "user:"+t.userID {
			errs = append(errs, fmt.Sprintf("turn %q: stored user row key %q, want %q (the id given to SendKeyed)", t.name, users[i].Key, "user:"+t.userID))
		}
		rows := map[string]provider.Message{}
		var storedOrder []string
		for _, m := range parts[i] {
			id := m.Role + "\x00" + m.Key
			rows[id] = m
			if !m.Notice {
				storedOrder = append(storedOrder, id)
			}
		}
		var liveOrder []string
		for _, it := range t.items {
			if it.role == roleThinking {
				continue
			}
			m, ok := rows[it.id()]
			switch {
			case !ok && it.id() == t.inflight:
				// The interrupted item: the agent may never store it.
			case !ok:
				errs = append(errs, fmt.Sprintf("turn %q: streamed %s %q never stored", t.name, it.role, it.key))
			default:
				liveOrder = append(liveOrder, it.id())
				if msg := textMismatch(it, m, it.id() == t.inflight); msg != "" {
					errs = append(errs, fmt.Sprintf("turn %q: %s %q %s", t.name, it.role, it.key, msg))
				}
			}
		}
		liveIDs := map[string]bool{}
		for _, it := range t.items {
			liveIDs[it.id()] = true
		}
		var shared []string
		for _, id := range storedOrder {
			if liveIDs[id] {
				shared = append(shared, id)
				continue
			}
			role, key, _ := strings.Cut(id, "\x00")
			errs = append(errs, fmt.Sprintf("turn %q: stored %s %q was never streamed", t.name, role, key))
		}
		if strings.Join(shared, "|") != strings.Join(liveOrder, "|") {
			errs = append(errs, fmt.Sprintf("turn %q: store order %v differs from stream order %v", t.name, readable(shared), readable(liveOrder)))
		}
	}
	return errs
}

func comparedRole(role string) bool {
	switch role {
	case roleUser, roleText, roleTool, roleToolResult:
		return true
	}
	return false
}

// textMismatch compares an item's streamed and stored content. The
// interrupted item may legitimately be cut at a different point on each side,
// so it only has to agree on a common prefix.
func textMismatch(it liveItem, m provider.Message, inflight bool) string {
	if it.role == roleTool {
		if m.Tool != nil && it.tool != "" && m.Tool.Name != it.tool {
			return fmt.Sprintf("tool name streamed %q, stored %q", it.tool, m.Tool.Name)
		}
		return ""
	}
	if it.text == m.Text {
		return ""
	}
	if inflight && (strings.HasPrefix(it.text, m.Text) || strings.HasPrefix(m.Text, it.text)) {
		return ""
	}
	return fmt.Sprintf("text streamed %.60q, stored %.60q", it.text, m.Text)
}

func sameRows(a, b []provider.Message) string {
	ids := func(ms []provider.Message) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Role+":"+m.Key+":"+m.Text)
		}
		return out
	}
	x, y := ids(a), ids(b)
	if strings.Join(x, "\n") == strings.Join(y, "\n") {
		return ""
	}
	return fmt.Sprintf("%d rows chained vs %d in Transcript", len(x), len(y))
}

func readable(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strings.Replace(id, "\x00", ":", 1)
	}
	return out
}

// newUUID is a random RFC 4122 v4 UUID, the id shape claude's stdin uuid
// takes.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
