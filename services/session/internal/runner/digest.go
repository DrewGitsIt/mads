package runner

import (
	"strings"
	"sync"

	"github.com/drewkidwell/mads/providers/provider"
)

// digestEntry is one unit of a turn's accumulated activity: text chunks
// verbatim, tool uses reduced to {name, path} — raw tool input/output never
// leaves the session process. Mirrors
// services/session/internal/synth's deltaEntry shape, but resets every turn
// end rather than on synth's own debounced multi-turn trigger, so a digest
// push (docs/LINKED-SESSIONS-DESIGN.md decision 4) carries exactly one
// turn's activity.
type digestEntry struct {
	Kind string // "user", "assistant", "tool"
	Text string
	Tool string
	Path string
}

// digestAccumulator collects one turn's activity for the digest push. Its
// own mutex (distinct from Runner.mu) guards against recordUser, called
// from the Send/Inject goroutine, racing with recordEvent/renderAndReset,
// called from the pump goroutine.
type digestAccumulator struct {
	mu  sync.Mutex
	buf []digestEntry
}

func (d *digestAccumulator) recordUser(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf = append(d.buf, digestEntry{Kind: "user", Text: text})
}

func (d *digestAccumulator) recordEvent(ev provider.Event) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch ev.Kind {
	case provider.EventText:
		if ev.Text != "" {
			d.buf = append(d.buf, digestEntry{Kind: "assistant", Text: ev.Text})
		}
	case provider.EventToolUse:
		if ev.Tool != nil {
			d.buf = append(d.buf, digestEntry{Kind: "tool", Tool: ev.Tool.Name, Path: ev.Tool.Path})
		}
	}
}

// renderAndReset drains the buffer into synth's Fast Model prompt shape and
// resets it for the next turn. Empty when nothing accumulated (e.g. a turn
// with no assistant text or tool use) — the caller skips the push entirely.
func (d *digestAccumulator) renderAndReset() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.buf) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, e := range d.buf {
		switch e.Kind {
		case "user":
			sb.WriteString("user: ")
			sb.WriteString(e.Text)
			sb.WriteString("\n")
		case "assistant":
			sb.WriteString("assistant: ")
			sb.WriteString(e.Text)
			sb.WriteString("\n")
		case "tool":
			sb.WriteString("tool: ")
			sb.WriteString(e.Tool)
			if e.Path != "" {
				sb.WriteString(" ")
				sb.WriteString(e.Path)
			}
			sb.WriteString("\n")
		}
	}
	d.buf = nil
	return sb.String()
}
