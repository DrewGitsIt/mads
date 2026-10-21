package hermescli

import (
	"context"
	"strings"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// session wraps the engine's ACP session to correct four hermes wire facts
// on the way through, without new engine knobs (the engine's knob set stays
// closed; each fact is a hermes quirk, not an ACP variation):
//
//  1. Usage is cumulative. hermes fills the prompt result's usage from the
//     agent's per-session counters (agent/turn_finalizer.py:33-36,708:
//     result[key] = agent.session_<key>), which start at zero in each
//     hermes-acp process (survey run2: the first turn after session/load
//     reports 0; runc1 lines 18/40/87: 9535 → 28758 → 38455 input tokens
//     over three turns). provider.Usage on EventTurnEnd is per turn, so the
//     wrapper reports the difference from the previous turn end. Context
//     occupancy (usage_update) is already absolute and passes through.
//  2. hermes prepends "\n\n" to the first non-blank text delta after a tool
//     iteration (agent/stream_delivery.py:305-310); state.db stores the
//     text without it (survey run1: 58 live chars vs 56 stored). The
//     wrapper strips it, so live text and the transcript agree.
//  3. A tool call's name: the engine names a kind-based call by its title,
//     and hermes titles are "<tool>: <preview>" (tools.py build_tool_title);
//     the wrapper cuts the name back to the tool's, the one hermes stores.
//  4. Permission asks carry synthetic tool ids (edit-approval-N,
//     perm-check-N) that are never announced as tool calls, yet hermes
//     sends a tool_call_update for them (survey run9 lines 13-15). The
//     engine turns that into an EventToolResult for a call nobody saw; the
//     wrapper drops results whose id was never announced. The real call's
//     own result follows under its live id (run9 line 16).
//
// Two more, for conversation keys (keys.go): the wrapper records what each
// turn streamed in the provider's ledger for this ref, which the readers
// match stored rows against; and it replaces a tool result's live text —
// hermes's rendering for the ACP stream — with what hermes stores for it,
// waiting up to half a second for the row (it lands 3-23 ms after the frame).
//
// It deliberately does not implement the runner's live Transcript()
// strategy: history comes from hermes's own store (TranscriptReader), never
// from the engine's in-memory buffer (design doc "Transcript").
type session struct {
	inner  provider.Session
	events chan provider.Event
	p      *Provider
	ref    string
	led    *ledger
	// keyed: the session was started with StartOptions.Keyed, the
	// keyed consumer's opt-in. Only then are a tool's name and result
	// swapped for what hermes stores (corrections 3 and the result swap);
	// a consumer that didn't opt in sees the stream exactly as before.
	keyed bool
}

func wrapSession(inner provider.Session, p *Provider, keyed bool) *session {
	s := &session{inner: inner, events: make(chan provider.Event, 256), p: p, ref: inner.Ref(), keyed: keyed}
	if p != nil {
		s.led = p.live.add(s.ref)
	}
	go s.pump()
	return s
}

// toolResultWait bounds the wait for a tool's stored result.
const toolResultWait = 500 * time.Millisecond

func (s *session) pump() {
	defer close(s.events)
	n := normalizer{announced: map[string]bool{}, storedNames: s.keyed}
	for ev := range s.inner.Events() {
		out, ok := n.apply(ev)
		if !ok {
			continue
		}
		if s.led != nil {
			switch out.Kind {
			case provider.EventText:
				if out.Text != "" {
					s.led.text(out.Key)
				}
			case provider.EventToolUse:
				s.led.tool(out.Key)
			case provider.EventToolResult:
				if !s.keyed {
					break
				}
				if text, found := s.p.storedToolResult(context.Background(), s.ref, out.Key, toolResultWait); found {
					out.Text = text
				}
			case provider.EventTurnEnd:
				s.led.end()
			}
		}
		s.events <- out
	}
}

// normalizer is the per-session state for the wire corrections; only the
// pump goroutine touches it.
type normalizer struct {
	// storedNames cuts a tool's title-name back to hermes's stored name
	// (keyed sessions only; see session.keyed).
	storedNames     bool
	announced       map[string]bool
	afterTool       bool
	prevIn, prevOut int
}

func (n *normalizer) apply(ev provider.Event) (provider.Event, bool) {
	switch ev.Kind {
	case provider.EventToolUse:
		if ev.Tool != nil && ev.Tool.ID != "" {
			n.announced[ev.Tool.ID] = true
		}
		if ev.Tool != nil && n.storedNames {
			// The engine names a kind-based call by its title; hermes titles
			// are "<tool>: <preview>", or the bare tool name when there's no
			// preview (acp_adapter/tools.py build_tool_title). The name is
			// what hermes stores (tool_calls[].function.name).
			t := *ev.Tool
			if name, _, ok := strings.Cut(t.Title, ": "); ok && t.Name == t.Title {
				t.Name = name
			}
			ev.Tool = &t
		}
		n.afterTool = true
	case provider.EventToolResult:
		if ev.Tool != nil && ev.Tool.ID != "" && !n.announced[ev.Tool.ID] {
			return ev, false
		}
		n.afterTool = true
	case provider.EventText:
		// hermes only adds the break to a delta with visible text and keeps
		// waiting past blank ones — mirror that exactly.
		if n.afterTool && strings.TrimSpace(ev.Text) != "" {
			ev.Text = strings.TrimPrefix(ev.Text, "\n\n")
			n.afterTool = false
		}
	case provider.EventTurnEnd:
		n.afterTool = false
		if ev.Usage != nil {
			u := *ev.Usage
			in, out := u.InputTokens, u.OutputTokens
			// A total below the previous one means the counters restarted;
			// the reported figure is then this turn's alone.
			if in >= n.prevIn && out >= n.prevOut {
				u.InputTokens, u.OutputTokens = in-n.prevIn, out-n.prevOut
			}
			n.prevIn, n.prevOut = in, out
			ev.Usage = &u
		}
	}
	return ev, true
}

func (s *session) Ref() string                         { return s.inner.Ref() }
func (s *session) Mode() provider.Mode                 { return s.inner.Mode() }
func (s *session) Events() <-chan provider.Event       { return s.events }
func (s *session) Interrupt(ctx context.Context) error { return s.inner.Interrupt(ctx) }

// Send records the turn in the ledger first: its user row will be the
// store's next one, so the count is taken before hermes can write it.
func (s *session) Send(ctx context.Context, text string) error {
	if s.led == nil {
		return s.inner.Send(ctx, text)
	}
	user, err := s.p.storedUserRows(ctx, s.ref)
	if err != nil {
		user = -1 // unknown: this turn's rows keep their own keys
	}
	s.led.begin(user, text)
	if err := s.inner.Send(ctx, text); err != nil {
		s.led.abort()
		return err
	}
	return nil
}

// Close ends the child; its rows go back to their own keys.
func (s *session) Close() error {
	err := s.inner.Close()
	if s.led != nil {
		s.p.live.remove(s.ref, s.led)
	}
	return err
}
