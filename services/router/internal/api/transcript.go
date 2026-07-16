package api

import (
	"context"
	"log"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// transcriptFor reads sess's durable transcript via its bound provider,
// keyed by the persisted ProviderRef (contracts/session.go's documented
// "transcript pointer"). Never errors outward — every failure mode here
// (no conversation yet, provider deregistered, provider doesn't implement
// TranscriptReader, the read itself fails) degrades to an empty transcript,
// matching this endpoint's "always valid JSON, never a blank pane" contract.
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

func (s *server) handleSessionTranscript(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.transcriptFor(r.Context(), sess))
}
