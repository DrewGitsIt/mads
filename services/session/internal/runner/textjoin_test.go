package runner

import (
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

func text(key, t string) provider.Event {
	return provider.Event{Kind: provider.EventText, Key: key, Text: t}
}

// The legacy stream joins adjacent keyed text items with a blank line, the
// way codex's adapter did before it emitted them separately.
func TestTextJoiner(t *testing.T) {
	tool := provider.Event{Kind: provider.EventToolUse, Key: "t1", Tool: &provider.ToolUse{Name: "ls"}}
	settle := provider.Event{Kind: provider.EventSettle, Key: "a"}
	cases := []struct {
		name string
		in   []provider.Event
		want string
	}{
		{"adjacent items", []provider.Event{text("a", "I'll check."), text("a", " Now."), text("b", "Done"), text("b", ".")}, "I'll check. Now.\n\nDone."},
		{"earlier item ends with a newline", []provider.Event{text("a", "## Plan\n"), text("b", "- one")}, "## Plan\n- one"},
		{"a tool between", []provider.Event{text("a", "Looking."), tool, text("b", "One file.")}, "Looking.One file."},
		{"a settle hint doesn't separate", []provider.Event{text("a", "first."), settle, text("b", "second")}, "first.\n\nsecond"},
		{"thinking between", []provider.Event{text("a", "x"), {Kind: provider.EventThinking, Key: "r", Text: "hm"}, text("b", "y")}, "xy"},
		{"unkeyed text is never joined", []provider.Event{text("", "x"), text("", "y")}, "xy"},
		{"a turn end resets", []provider.Event{text("a", "x"), {Kind: provider.EventTurnEnd}, text("b", "y")}, "xy"},
		{"a subagent's text is not this stream's", []provider.Event{text("a", "x"), {Kind: provider.EventText, Key: "sub", Text: "s", ParentToolUseID: "toolu_1"}, text("a", "y")}, "xsy"},
		{"an empty chunk doesn't move the boundary", []provider.Event{text("a", "x"), text("b", ""), text("b", "y")}, "x\n\ny"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var j textJoiner
			var got string
			for _, ev := range c.in {
				if ev = j.apply(ev); ev.Kind == provider.EventText {
					got += ev.Text
				}
			}
			if got != c.want {
				t.Errorf("stream text %q, want %q", got, c.want)
			}
		})
	}
}

// End to end through the runner: what a subscriber of the live stream reads.
func TestLiveStreamJoinsAdjacentTextItems(t *testing.T) {
	rig := startPeer(t)
	stream, unsub := rig.r.Subscribe()
	defer unsub()
	rig.sess.events <- text("msg_a", "I'm checking the workspace now.")
	rig.sess.events <- text("msg_b", "The workspace is empty.")
	rig.sess.events <- provider.Event{Kind: provider.EventTurnEnd}
	var got string
	for ev := range stream {
		if ev.Kind == provider.EventText {
			got += ev.Text
		}
		if ev.Kind == provider.EventTurnEnd {
			break
		}
	}
	if want := "I'm checking the workspace now.\n\nThe workspace is empty."; got != want {
		t.Errorf("live stream %q, want %q", got, want)
	}
}
