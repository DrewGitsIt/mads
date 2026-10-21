package runner

import (
	"context"
	"log"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/linkwrap"
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
	return r.transcript(ctx, false)
}

// KeyedTranscript is Transcript as a keyed (v2) consumer reads it
// (GET /transcript?v=2, or paged): for a provider with stable keys, always
// the agent's own store — its KeyedTranscriptReader where it has one, so
// stored thinking comes as keyed Role "thinking" rows — never a session's
// live buffer, which (acprpc's, goose) holds in-flight items the v2 stream
// carries itself, mostly without keys (docs/CHAT-STREAM-DESIGN.md §3,
// "ACP"). Elsewhere it is Transcript.
func (r *Runner) KeyedTranscript(ctx context.Context) []contracts.TranscriptMessage {
	return r.transcript(ctx, true)
}

func (r *Runner) transcript(ctx context.Context, keyed bool) []contracts.TranscriptMessage {
	out := []contracts.TranscriptMessage{}
	ref, providerName, sess := r.transcriptRef()
	if ref == "" {
		return out // Start hasn't finished yet
	}
	_, storeKeyed := r.chatReader()

	// Preferred while a live session is bound and non-empty: its own buffer is
	// the only record of an in-flight turn (an ACP agent has nothing on disk).
	// Fall through only when the live buffer is unavailable (sess nil) or empty.
	if sess != nil && (!keyed || !storeKeyed) {
		if lt, ok := sess.(transcripter); ok {
			if msgs := lt.Transcript(); len(msgs) > 0 {
				return r.renderRows(ctx, msgs, false)
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
	// Only the keyed form settles the tail: thinking rows are in it.
	fromStore := keyed && storeKeyed
	msgs, err := readStore(ctx, reader, ref, fromStore)
	if err != nil {
		log.Printf("runner: transcript: read %s: %v", r.sessionID, err)
		return out
	}
	return r.renderRows(ctx, msgs, fromStore)
}

// renderRows renders a transcript read, which is also a settle point
// (docs/CHAT-STREAM-DESIGN.md §3): fromStore says msgs are the whole store
// as settling reads it, so they settle the v2 tail before the rows are
// served — no row served here shares a key with a frame still in the tail.
// Rows from elsewhere (a live buffer) only prompt a read of the store.
func (r *Runner) renderRows(ctx context.Context, msgs []provider.Message, fromStore bool) []contracts.TranscriptMessage {
	if fromStore {
		r.settleWith(msgs)
	} else {
		r.labeler.assign(msgs, true)
		r.scheduleSettle()
	}
	opts := r.renderOptions(ctx)
	opts.userKeys = r.labeler.snapshot()
	return renderTranscript(msgs, opts)
}

// renderOptions is renderTranscript's per-session context.
func (r *Runner) renderOptions(ctx context.Context) renderOptions {
	return renderOptions{inbound: func(in *provider.Inbound, at time.Time) *contracts.InboundSource {
		return r.inboundSource(ctx, in, at)
	}}
}

// renderOptions carries what renderTranscript needs from the session beyond
// the provider's messages. A struct, so a new per-session input (user-row
// labels, docs/CHAT-STREAM-DESIGN.md §1) is a field, not a signature change.
type renderOptions struct {
	// inbound resolves an inbound message's (Role "inbound") source; nil
	// leaves it unresolved.
	inbound func(*provider.Inbound, time.Time) *contracts.InboundSource
	// userKeys labels stored user rows that carry the agent's own key with
	// the key of the user_message that sent them (userLabeler): native key
	// → "user:<id>". Applied before linkwrap.Expand, so a split row's
	// segments get sub-keys of the label.
	userKeys map[string]string
}

// renderTranscript maps normalized provider messages to wire TranscriptMessages,
// labels injected user messages from their in-text wrappers (pkg/linkwrap —
// the same parse the Router's durable transcript runs, so a message reads the
// same live or asleep; a message with no wrapper is the human's), then runs
// the same T5
// tool-status rule engine as the live stream: resolve each recorded call's
// status from its recorded result; calls with no result stay honestly open (a
// refetch has no turn-end signal to distinguish "still running" from
// "interrupted" — see conversation.go).
//
// Every row carries its provider key and notice flag (§4 of
// docs/CHAT-STREAM-DESIGN.md); a split user row's segments get sub-keys
// (linkwrap.SegmentKey). Paging happens on the result, in the handler.
func renderTranscript(msgs []provider.Message, opts renderOptions) []contracts.TranscriptMessage {
	out := make([]contracts.TranscriptMessage, len(msgs))
	for i, m := range msgs {
		tm := contracts.TranscriptMessage{Role: m.Role, Text: m.Text, Timestamp: m.Timestamp, IsError: m.IsError, Key: m.Key, Notice: m.Notice}
		if l, ok := opts.userKeys[m.Key]; ok && m.Role == "user" {
			tm.Key = l
		}
		if m.Compact != nil {
			c := contracts.CompactMarker(*m.Compact)
			tm.Compact = &c
		}
		tm.Tool = contractsToolUse(m.Tool) // full enriched mapping — kind/title/status/locations/server
		if m.Inbound != nil && opts.inbound != nil {
			tm.Inbound = opts.inbound(m.Inbound, m.Timestamp)
		}
		out[i] = tm
	}
	out = linkwrap.Expand(out)
	// One call site for the stamp, both source branches — keeps the live-buffer
	// path and the disk path status-consistent.
	toolstatus.StampTranscriptStatuses(out)
	return out
}
