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
//
// Falls back to lastRef when sess is nil (mid-SwitchTransport, which nils
// sess for the switch's full duration) so the transcript keeps serving
// history through a switch instead of going blank — providerName never
// changes across a switch, and TranscriptReader only needs the ref string,
// not a live session object.
func (r *Runner) Transcript(ctx context.Context) []contracts.TranscriptMessage {
	out := []contracts.TranscriptMessage{}
	r.mu.Lock()
	ref := r.lastRef
	if r.sess != nil {
		ref = r.sess.Ref()
	}
	providerName := r.providerName
	attrs := make(map[int]contracts.Attribution, len(r.turnAttributions))
	for k, v := range r.turnAttributions {
		attrs[k] = v
	}
	r.mu.Unlock()
	if ref == "" {
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
	msgs, err := reader.Transcript(ctx, ref)
	if err != nil {
		log.Printf("runner: transcript: read %s: %v", r.sessionID, err)
		return out
	}
	result := make([]contracts.TranscriptMessage, len(msgs))
	var userIdx int
	for i, m := range msgs {
		tm := contracts.TranscriptMessage{Role: m.Role, Text: m.Text, Timestamp: m.Timestamp, IsError: m.IsError}
		if m.Tool != nil {
			tm.Tool = &contracts.ToolUse{ID: m.Tool.ID, Name: m.Tool.Name, Input: m.Tool.Input, Path: m.Tool.Path}
		}
		if m.Role == "user" {
			userIdx++
			if a, ok := attrs[userIdx]; ok {
				tm.Attribution = &a
			}
		}
		result[i] = tm
	}
	return result
}
