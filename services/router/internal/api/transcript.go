package api

import (
	"context"
	"log"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/toolstatus"
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
func (s *server) transcriptFor(ctx context.Context, sess contracts.Session) []contracts.TranscriptMessage {
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
	msgs, err := reader.Transcript(ctx, sess.ProviderRef)
	if err != nil {
		log.Printf("api: transcript: read session %s: %v", sess.ID, err)
		return out
	}
	return normalizeTurns(msgs)
}

// normalizeTurns maps provider-native transcript messages onto the
// contracts wire shape shared by GET /sessions/{id}/transcript and the
// pipeline drill-in's GET .../turns (pipeline_turns.go) — the same mapping,
// two callers. Enrichment (kind/title/locations/server) is carried through
// and statuses are resolved with the same rule engine the live child uses,
// so a dormant session's transcript renders identically to a live refetch.
func normalizeTurns(msgs []provider.Message) []contracts.TranscriptMessage {
	result := make([]contracts.TranscriptMessage, len(msgs))
	for i, m := range msgs {
		tm := contracts.TranscriptMessage{Role: m.Role, Text: m.Text, Timestamp: m.Timestamp, IsError: m.IsError}
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
	toolstatus.StampTranscriptStatuses(result)
	return result
}

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
	writeJSON(w, http.StatusOK, s.transcriptFor(r.Context(), sess))
}
