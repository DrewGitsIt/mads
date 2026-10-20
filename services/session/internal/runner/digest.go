package runner

import (
	"sync"

	"github.com/drewkidwell/mads/pkg/turndigest"
	"github.com/drewkidwell/mads/providers/provider"
)

// digestAccumulator collects one turn's activity for the digest push, as
// turndigest.Entry values (the shared shape — text chunks verbatim, tool
// uses reduced to {name, path}). A digest carries no raw tool input/output;
// a leader that wants it reads the transcript through get_session_turns
// (2026-10-19), which this accumulator doesn't feed. It mirrors
// services/session/internal/synth's deltaEntry accumulation, but resets
// every turn end rather than on synth's own debounced multi-turn trigger,
// so a digest push
// (docs/LINKED-SESSIONS-DESIGN.md decision 4) carries exactly one turn's
// activity. Its own mutex (distinct from Runner.mu) guards against
// recordUser, called from the Send/Inject goroutine, racing with
// recordEvent/renderAndReset, called from the pump goroutine.
type digestAccumulator struct {
	mu  sync.Mutex
	buf []turndigest.Entry

	// sawUser/allUserDigestAttributed track whether every recordUser call
	// this turn was a job.GenerateDigest push (Attribution.DigestAttributed)
	// rather than a real user/leader message — renderAndReset reports this
	// as digestOnly so digestwatch can skip re-triggering on a monitor's own
	// forced turn (self-amplification guard).
	sawUser                 bool
	allUserDigestAttributed bool
}

func (d *digestAccumulator) recordUser(text string, digestAttributed bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf = append(d.buf, turndigest.Entry{Kind: turndigest.KindUser, Text: text})
	if !d.sawUser {
		d.allUserDigestAttributed = true
	}
	d.sawUser = true
	if !digestAttributed {
		d.allUserDigestAttributed = false
	}
}

// recordInbound records a message that reached the agent from outside mads
// (inbound.go) as a user-kind entry naming its sender — what prompted the
// turn when it woke the agent. It counts as a real message for digestOnly,
// like an ordinary user turn's.
func (d *digestAccumulator) recordInbound(from, text string) {
	d.recordUser("(message from "+from+") "+text, false)
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
// digestOnly reports whether every recordUser call this turn was a
// job.GenerateDigest push, with no real user/leader message mixed in.
func (d *digestAccumulator) renderAndReset() (out string, digestOnly bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.buf) == 0 {
		return "", false
	}
	out = turndigest.Render(d.buf)
	digestOnly = d.sawUser && d.allUserDigestAttributed
	d.buf = nil
	d.sawUser = false
	d.allUserDigestAttributed = false
	return out, digestOnly
}
