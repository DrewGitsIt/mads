package runner

import (
	"context"
	"log"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// Transcript reads this session's live conversation straight from its bound
// provider instance (spec §15) — the counterpart to the Router's
// transcriptFor, which can only see durable state. This is what makes a live
// session's transcript readable at all: the Router proxies here rather than
// reading through its own, separate provider.Registry. Never errors outward,
// matching transcriptFor's "always valid JSON, never a blank pane" contract.
func (r *Runner) Transcript(ctx context.Context) []contracts.TranscriptMessage {
	out := []contracts.TranscriptMessage{}
	r.mu.Lock()
	sess := r.sess
	providerName := r.providerName
	r.mu.Unlock()
	if sess == nil {
		return out // Start hasn't finished yet
	}
	p, err := r.providers.Get(providerName)
	if err != nil {
		log.Printf("runner: transcript: provider %q: %v", providerName, err)
		return out
	}
	reader, ok := p.(provider.TranscriptReader)
	if !ok {
		return out
	}
	msgs, err := reader.Transcript(ctx, sess.Ref())
	if err != nil {
		log.Printf("runner: transcript: read %s: %v", r.sessionID, err)
		return out
	}
	result := make([]contracts.TranscriptMessage, len(msgs))
	for i, m := range msgs {
		tm := contracts.TranscriptMessage{Role: m.Role, Text: m.Text}
		if m.Tool != nil {
			tm.Tool = &contracts.ToolUse{Name: m.Tool.Name, Input: m.Tool.Input, Path: m.Tool.Path}
		}
		result[i] = tm
	}
	return result
}
