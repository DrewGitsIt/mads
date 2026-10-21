package api

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/inbound"
	"github.com/drewkidwell/mads/pkg/linkwrap"
	"github.com/drewkidwell/mads/pkg/toolstatus"
	"github.com/drewkidwell/mads/pkg/transcriptpage"
	"github.com/drewkidwell/mads/providers/provider"
)

// transcriptFor reads sess's durable transcript via its bound provider,
// keyed by the persisted ProviderRef (contracts/session.go's documented
// "transcript pointer"). Never errors outward — every failure mode here
// (no conversation yet, provider deregistered, provider doesn't implement
// TranscriptReader, the read itself fails) degrades to an empty transcript,
// matching this endpoint's "always valid JSON, never a blank pane" contract.
//
// This is the fallback path only: handleSessionTranscript proxies to the
// running session child (which reads its own live provider instance) first,
// since a live turn's in-memory state only ever exists in the child's
// process, not the Router's own provider.Registry. This path serves sessions
// with no running child, where the durable ProviderRef read here is all
// there is.
//
// keyed is the keyed form (transcriptpage.Keyed: ?v=2 or paging): the
// provider's KeyedTranscriptReader where it has one, so stored thinking
// rows appear by key, exactly as the live child serves them.
func (s *server) transcriptFor(ctx context.Context, sess contracts.Session, keyed bool) []contracts.TranscriptMessage {
	out := []contracts.TranscriptMessage{}
	if sess.ProviderRef == "" {
		return out // pipeline-only session (e.g. "Wave 7"): no interactive conversation ever started
	}
	p, err := s.providers.Get(sess.Provider)
	if err != nil {
		log.Printf("api: transcript: provider %q: %v", sess.Provider, err)
		return out
	}
	reader, ok := p.(provider.TranscriptReader)
	if !ok {
		return out
	}
	var msgs []provider.Message
	if kr, ok := reader.(provider.KeyedTranscriptReader); ok && keyed {
		msgs, err = kr.KeyedTranscript(ctx, sess.ProviderRef)
	} else {
		msgs, err = reader.Transcript(ctx, sess.ProviderRef)
	}
	if err != nil {
		log.Printf("api: transcript: read session %s: %v", sess.ID, err)
		return out
	}
	return normalizeTurns(msgs, s.inboundSources(ctx, sess))
}

// inboundSources resolves the senders of inbound messages (Role "inbound")
// in sess's transcript the same way the live session does (pkg/inbound), so
// a dormant session's history names them too.
func (s *server) inboundSources(ctx context.Context, sess contracts.Session) func(*provider.Inbound, time.Time) *contracts.InboundSource {
	var data inbound.Data
	if s.data != nil {
		data = s.data
	}
	res := inbound.New(data, sess.ID, sess.Provider)
	return func(in *provider.Inbound, at time.Time) *contracts.InboundSource {
		return res.Source(ctx, in, at)
	}
}

// normalizeTurns maps provider-native transcript messages onto the
// contracts wire shape shared by GET /sessions/{id}/transcript and the
// pipeline drill-in's GET .../turns (pipeline_turns.go) — the same mapping,
// two callers. Enrichment (kind/title/locations/server) is carried through
// and statuses are resolved with the same rule engine the live child uses,
// so a dormant session's transcript renders identically to a live refetch.
// Injected messages are labelled from their in-text wrappers by the same
// parse the live child runs (pkg/linkwrap).
//
// Every row carries its provider key and notice flag, as the child's do; a
// split user row's segments get sub-keys (linkwrap.SegmentKey).
//
// src resolves an inbound message's source (inboundSources); nil leaves
// Inbound unset.
func normalizeTurns(msgs []provider.Message, src func(*provider.Inbound, time.Time) *contracts.InboundSource) []contracts.TranscriptMessage {
	result := make([]contracts.TranscriptMessage, len(msgs))
	for i, m := range msgs {
		tm := contracts.TranscriptMessage{Role: m.Role, Text: m.Text, Timestamp: m.Timestamp, IsError: m.IsError, Key: m.Key, Notice: m.Notice}
		if m.Compact != nil {
			c := contracts.CompactMarker(*m.Compact)
			tm.Compact = &c
		}
		if m.Inbound != nil && src != nil {
			tm.Inbound = src(m.Inbound, m.Timestamp)
		}
		if m.Tool != nil {
			tm.Tool = &contracts.ToolUse{
				ID:     m.Tool.ID,
				Name:   m.Tool.Name,
				Input:  m.Tool.Input,
				Path:   m.Tool.Path,
				Kind:   string(m.Tool.Kind),
				Title:  m.Tool.Title,
				Status: string(m.Tool.Status),
				Server: m.Tool.Server,
			}
			for _, loc := range m.Tool.Locations {
				tm.Tool.Locations = append(tm.Tool.Locations, contracts.ToolLocation{Path: loc.Path, Line: loc.Line})
			}
		}
		result[i] = tm
	}
	result = linkwrap.Expand(result)
	toolstatus.StampTranscriptStatuses(result)
	return result
}

// handleSessionTranscript answers GET /sessions/{id}/transcript: proxied,
// query and all, to the session's running child, else read from the stored
// ref here. ?before=<key>&limit=<n> pages either way through the same
// transcriptpage.Respond (contracts.TranscriptPage); with neither, the whole
// transcript as a bare array.
func (s *server) handleSessionTranscript(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if addr, ok := s.sup.SessionAddr(sess.ID); ok {
		s.proxyTo(w, r, addr, "/transcript")
		return
	}
	q := r.URL.Query()
	status, body := transcriptpage.Respond(q, s.transcriptFor(r.Context(), sess, transcriptpage.Keyed(q)))
	writeJSON(w, status, body)
}
