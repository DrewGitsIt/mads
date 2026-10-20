package codexcli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// OneShot runs one prompt on an app-server thread and closes it — the same
// path an interactive session takes, rather than `codex exec --json`. exec
// is one process per turn too, but it streams no deltas (whole items only,
// ids item_0…, run4-exec), so OnEvent would get the reply in one piece, and
// it would need a second parser for a second dialect. Riding the thread
// keeps one wire layer, streams genuinely, resumes by the same thread id
// (Resume → thread/resume), and reports the ref before the reply.
//
// The thread runs at full trust (danger-full-access, never) — what the
// ACP engine's one-shot does too: there is no one to ask. Text is the
// turn's final answer; commentary the model wrote along the way (phase
// "commentary") streams through OnEvent but is not the result.
func (p *Provider) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	sess, err := p.Start(ctx, provider.StartOptions{
		Mode:         provider.ModeACP,
		Model:        req.Model,
		Cwd:          req.Cwd,
		SystemPrompt: req.System,
		Resume:       req.Resume,
		Trust:        provider.TrustFull,
	})
	if err != nil {
		return nil, err
	}
	s := sess.(*session)
	defer func() { _ = s.Close() }()
	ref := s.Ref()
	if req.OnEvent != nil {
		req.OnEvent(provider.Event{Kind: provider.EventSessionRef, Ref: ref})
	}
	if err := s.Send(ctx, req.Prompt); err != nil {
		return nil, err
	}

	var text strings.Builder
	var usage provider.Usage
	var errText string
	for done := false; !done; {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				return nil, errors.New("codex app-server exited before the turn ended")
			}
			switch ev.Kind {
			case provider.EventText:
				text.WriteString(ev.Text)
			case provider.EventError:
				errText = ev.Text
			case provider.EventTurnEnd:
				if ev.Usage != nil {
					usage = *ev.Usage
				}
				done = true
			}
			if req.OnEvent != nil && ev.Kind != provider.EventTurnEnd {
				req.OnEvent(ev)
			}
		case <-ctx.Done():
			// Straight to the deferred Close: an Interrupt could wait out
			// awaitTurnID and an unanswered turn/interrupt, and the thread is
			// discarded either way. Close's own wait is bounded (closeGrace).
			return nil, ctx.Err()
		}
	}

	s.mu.Lock()
	final, terr := s.lastFinal, s.lastErr
	s.mu.Unlock()
	if terr != nil {
		if terr.rateLimited() {
			return nil, &provider.RateLimitError{Message: terr.Message}
		}
		return nil, fmt.Errorf("codex turn failed: %s", terr.Message)
	}
	if errText != "" && final == "" && text.Len() == 0 {
		return nil, fmt.Errorf("codex: %s", errText)
	}
	out := final
	if out == "" {
		out = text.String()
	}
	return &provider.OneShotResult{Text: out, Ref: ref, Usage: usage}, nil
}
