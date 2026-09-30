package runner

import (
	"context"
	"log"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/toolstatus"
	"github.com/drewkidwell/mads/providers/provider"
)

// transcripter is the optional strategy a live provider.Session may implement
// to hand back its own running conversation record. The ACP engine's
// streamSession does: it buffers every streamed fragment in-session, because an
// ACP agent like goose has no on-disk transcript file to read from (unlike
// claude-code's .jsonl). The buffer is the only durable record for an in-flight
// turn, so a live transcripter is preferred over a provider-level disk read.
type transcripter interface {
	Transcript() []provider.Message
}

// Transcript reads this session's live conversation straight from its bound
// provider (spec §15) — the counterpart to the Router's transcriptFor, which
// can only see durable state. This is what makes a live session's transcript
// readable at all: the Router proxies here rather than reading through its own,
// separate provider.Registry. Never errors outward, matching transcriptFor's
// "always valid JSON, never a blank pane" contract.
//
// Source preference, in order:
//  1. The live session's own buffered transcript (transcripter), when bound
//     non-nil and non-empty — the authoritative, real-time record of an
//     in-flight turn for an ACP agent (goose ACP assistant-reply gap).
//  2. The provider's TranscriptReader (a disk read), as a fallback for
//     transports that persist per-turn (claude-code .jsonl) — and as the
//     only path available mid-SwitchTransport, when sess is nil for the
//     switch's full duration. There providerName never changes and the
//     TranscriptReader only needs the ref string, not a live session object.
func (r *Runner) Transcript(ctx context.Context) []contracts.TranscriptMessage {
	out := []contracts.TranscriptMessage{}
	r.mu.Lock()
	ref := r.lastRef
	providerName := r.providerName
	sess := r.sess
	attrs := make(map[int]contracts.Attribution, len(r.turnAttributions))
	for k, v := range r.turnAttributions {
		attrs[k] = v
	}
	if sess != nil {
		ref = sess.Ref()
	}
	r.mu.Unlock()
	if ref == "" {
		return out // Start hasn't finished yet
	}

	// Preferred while a live session is bound and non-empty: its own buffer is
	// the only record of an in-flight turn (an ACP agent has nothing on disk).
	// Fall through only when the live buffer is unavailable (sess nil) or empty.
	if sess != nil {
		if lt, ok := sess.(transcripter); ok {
			if msgs := lt.Transcript(); len(msgs) > 0 {
				return renderTranscript(msgs, attrs)
			}
		}
	}

	// Fallback: the provider's disk-backed transcript reader.
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
	return renderTranscript(msgs, attrs)
}

// renderTranscript maps normalized provider messages to wire TranscriptMessages,
// attaching per-turn Attribution to each user message, then runs the same T5
// tool-status rule engine as the live stream: resolve each recorded call's
// status from its recorded result; calls with no result stay honestly open (a
// refetch has no turn-end signal to distinguish "still running" from
// "interrupted" — see conversation.go).
func renderTranscript(msgs []provider.Message, attrs map[int]contracts.Attribution) []contracts.TranscriptMessage {
	out := make([]contracts.TranscriptMessage, len(msgs))
	var userIdx int
	for i, m := range msgs {
		tm := contracts.TranscriptMessage{Role: m.Role, Text: m.Text, Timestamp: m.Timestamp, IsError: m.IsError}
		if m.Compact != nil {
			c := contracts.CompactMarker(*m.Compact)
			tm.Compact = &c
		}
		tm.Tool = contractsToolUse(m.Tool) // full enriched mapping — kind/title/status/locations/server
		if m.Role == "user" {
			userIdx++
			if a, ok := attrs[userIdx]; ok {
				tm.Attribution = &a
			}
		}
		out[i] = tm
	}
	// One call site for the stamp, both source branches — keeps the live-buffer
	// path and the disk path status-consistent.
	toolstatus.StampTranscriptStatuses(out)
	return out
}
