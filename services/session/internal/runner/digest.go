package runner

import (
	"sync"

	"github.com/drewkidwell/mads/pkg/turndigest"
	"github.com/drewkidwell/mads/providers/provider"
)

// digestAccumulator collects one turn's activity for the digest push, as
// turndigest.Entry values (the shared shape — text chunks verbatim, tool
// uses reduced to {name, path}; raw tool input/output never leaves the
// session process). It mirrors services/session/internal/synth's deltaEntry
// accumulation, but resets every turn end rather than on synth's own
// debounced multi-turn trigger, so a digest push
// (docs/LINKED-SESSIONS-DESIGN.md decision 4) carries exactly one turn's
// activity. Its own mutex (distinct from Runner.mu) guards against
// recordUser, called from the Send/Inject goroutine, racing with
// recordEvent/renderAndReset, called from the pump goroutine.
type digestAccumulator struct {
	mu  sync.Mutex
	buf []turndigest.Entry
}

func (d *digestAccumulator) recordUser(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf = append(d.buf, turndigest.Entry{Kind: turndigest.KindUser, Text: text})
}

func (d *digestAccumulator) recordEvent(ev provider.Event) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch ev.Kind {
	case provider.EventText:
		if ev.Text != "" {
			d.buf = append(d.buf, turndigest.Entry{Kind: turndigest.KindAssistant, Text: ev.Text})
		}
	case provider.EventToolUse:
		if ev.Tool != nil {
			d.buf = append(d.buf, turndigest.Entry{Kind: turndigest.KindTool, Tool: ev.Tool.Name, Path: ev.Tool.Path})
		}
	}
}

// renderAndReset drains the buffer into turndigest's canonical line format
// (the Fast Model prompt shape, same bytes as the old inline rendering) and
// resets it for the next turn. Empty when nothing accumulated (e.g. a turn
// with no assistant text or tool use) — the caller skips the push entirely.
func (d *digestAccumulator) renderAndReset() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.buf) == 0 {
		return ""
	}
	out := turndigest.Render(d.buf)
	d.buf = nil
	return out
}
