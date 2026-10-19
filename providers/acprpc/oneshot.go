package acprpc

import (
	"context"
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// OneShot is the engine-level emulation over ACP (design decision,
// docs/DECLARATIVE-PROVIDERS-DESIGN.md "OneShot is engine-emulated"):
// spawn → initialize → session/new → full-trust mode → optional system
// prompt → one streamed prompt → session/close → exit. The ref is the
// sessionId, known synchronously at session/new — so OneShotResult.Ref
// and the EventSessionRef announcement are solved for every ACP agent
// with no per-harness one-shot argv or session-list ref fallback. Usage
// comes from the prompt result / usage_update snapshot on the turn end.
//
// req.Resume swaps session/new for session/load of that ref (Start's resume
// path): the replayed history is buffered, never streamed to OnEvent, and
// the ref reported is the resumed one. An agent that doesn't advertise
// loadSession fails with an error wrapping provider.ErrUnsupported.
func (e *Engine) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	sess, err := e.Start(ctx, provider.StartOptions{
		Mode:         provider.ModeACP,
		Model:        req.Model,
		Cwd:          req.Cwd,
		Resume:       req.Resume,
		SystemPrompt: req.System,
		Trust:        provider.TrustFull,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()

	emit := func(ev provider.Event) {
		if req.OnEvent != nil {
			req.OnEvent(ev)
		}
	}
	// The ref is known synchronously — announce it before any turn output
	// so callers get exact transcript attribution up front.
	emit(provider.Event{Kind: provider.EventSessionRef, Ref: sess.Ref()})

	if err := sess.Send(ctx, req.Prompt); err != nil {
		return nil, err
	}

	var text strings.Builder
	var usage provider.Usage
	var errText string
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return nil, fmt.Errorf("%s acp: agent exited before the turn ended", e.cfg.Bin)
			}
			emit(ev)
			switch ev.Kind {
			case provider.EventText:
				text.WriteString(ev.Text)
			case provider.EventError:
				errText = ev.Text
			case provider.EventTurnEnd:
				// An errored turn fails the invocation even when partial text
				// streamed first — claudecode's one-shot does the same (its
				// is_error result line returns the error, not the text).
				if errText != "" {
					return nil, fmt.Errorf("%s acp: %s", e.cfg.Bin, errText)
				}
				if ev.Usage != nil {
					usage = *ev.Usage
				}
				return &provider.OneShotResult{Text: text.String(), Ref: sess.Ref(), Usage: usage}, nil
			}
		case <-ctx.Done():
			// Best-effort stop before the deferred Close: cancel the turn so
			// the agent can wind down cleanly instead of being killed mid-run.
			_ = sess.Interrupt(context.Background())
			return nil, ctx.Err()
		}
	}
}
