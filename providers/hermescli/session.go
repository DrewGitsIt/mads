package hermescli

import (
	"context"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// session wraps the engine's ACP session to correct three hermes wire facts
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
//  3. Permission asks carry synthetic tool ids (edit-approval-N,
//     perm-check-N) that are never announced as tool calls, yet hermes
//     sends a tool_call_update for them (survey run9 lines 13-15). The
//     engine turns that into an EventToolResult for a call nobody saw; the
//     wrapper drops results whose id was never announced. The real call's
//     own result follows under its live id (run9 line 16).
//
// It deliberately does not implement the runner's live Transcript()
// strategy: history comes from hermes's own store (TranscriptReader), never
// from the engine's in-memory buffer (design doc "Transcript").
type session struct {
	inner  provider.Session
	events chan provider.Event
}

func wrapSession(inner provider.Session) *session {
	s := &session{inner: inner, events: make(chan provider.Event, 256)}
	go s.pump()
	return s
}

func (s *session) pump() {
	defer close(s.events)
	n := normalizer{announced: map[string]bool{}}
	for ev := range s.inner.Events() {
		if out, ok := n.apply(ev); ok {
			s.events <- out
		}
	}
}

// normalizer is the per-session state for the three corrections; only the
// pump goroutine touches it.
type normalizer struct {
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

func (s *session) Ref() string                                 { return s.inner.Ref() }
func (s *session) Mode() provider.Mode                         { return s.inner.Mode() }
func (s *session) Events() <-chan provider.Event               { return s.events }
func (s *session) Send(ctx context.Context, text string) error { return s.inner.Send(ctx, text) }
func (s *session) Interrupt(ctx context.Context) error         { return s.inner.Interrupt(ctx) }
func (s *session) Close() error                                { return s.inner.Close() }
