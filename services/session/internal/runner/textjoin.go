package runner

import (
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// textJoiner keeps the legacy text stream reading as it did before keyed
// items (docs/CHAT-STREAM-DESIGN.md §6, the join rule; C4 replaces this with
// the keyed conversation model). An adapter now emits two adjacent text
// items as separate keyed events, each with exactly the text the agent
// stores (codex's commentary then its final answer); consumers that render
// one running string would read "now.The workspace". When a text event
// directly follows text with a different, non-empty Key, and the earlier
// text didn't end with a newline, the joiner opens the new item with a
// blank line. Anything else between them (a tool, thinking, a turn end)
// breaks adjacency; a settle hint carries no content and doesn't. Unkeyed
// text is never joined, so an adapter without keys streams exactly as it
// always has.
type textJoiner struct {
	lastKey string // the key of the text event before this one, "" if none
	endsNL  bool   // that text ended with a newline
}

func (j *textJoiner) apply(ev provider.Event) provider.Event {
	if ev.ParentToolUseID != "" {
		return ev // a subagent's sidechain: routed apart, not this stream's text
	}
	switch ev.Kind {
	case provider.EventText:
		if ev.Text == "" {
			return ev // no content: neither joins nor moves the boundary
		}
		if ev.Key != "" && j.lastKey != "" && ev.Key != j.lastKey && !j.endsNL {
			ev.Text = "\n\n" + ev.Text
		}
		j.endsNL = strings.HasSuffix(ev.Text, "\n")
		j.lastKey = ev.Key
	case provider.EventSettle:
	default:
		j.lastKey = ""
	}
	return ev
}
