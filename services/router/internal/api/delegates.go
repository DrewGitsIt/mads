package api

import (
	"log"
	"net/http"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// handleListDelegates serves the delegate records (docs/DELEGATES-DESIGN.md),
// filtered by ?state= — the session rail's cold read is state=running, one
// call for every session rather than one per card — or by ?ended_after=, the
// running ones plus those finished since (the rail under a fade window).
func (s *server) handleListDelegates(w http.ResponseWriter, r *http.Request) {
	if raw := r.URL.Query().Get("ended_after"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			var v contracts.ValidationError
			v.Add("ended_after", "must be an RFC 3339 time")
			writeError(w, &v)
			return
		}
		list, err := s.data.DelegatesEndedAfter(r.Context(), t)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
		return
	}
	list, err := s.data.Delegates(r.Context(), contracts.DelegateState(r.URL.Query().Get("state")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleListSessionDelegates serves one session's delegates, oldest first.
func (s *server) handleListSessionDelegates(w http.ResponseWriter, r *http.Request) {
	list, err := s.data.SessionDelegates(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleDelegateTranscript serves one delegate's own transcript, read from
// the harness's store through the session's provider (DelegateInspector).
//
// Read here, not proxied to a live child as GET /sessions/{id}/transcript
// is: the harness writes the delegate's file as it runs, so the file on disk
// is as fresh as anything the child holds, and a dormant or imported session
// is served the same way. Every failure degrades to available=false — the
// view falls back to the event log rather than going blank.
func (s *server) handleDelegateTranscript(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	out := contracts.DelegateTranscript{Messages: []contracts.TranscriptMessage{}}
	p, err := s.providers.Get(sess.Provider)
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	inspector, ok := p.(provider.DelegateInspector)
	if !ok {
		writeJSON(w, http.StatusOK, out)
		return
	}
	harnessID := r.PathValue("harnessId")
	for _, ref := range delegateRefs(sess) {
		msgs, found, err := inspector.DelegateTranscript(r.Context(), ref, harnessID)
		if err != nil {
			log.Printf("api: delegate transcript: session %s delegate %s: %v", sess.ID, harnessID, err)
			continue
		}
		if found {
			out = contracts.DelegateTranscript{Available: true, Messages: normalizeTurns(msgs)}
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// delegateRefs is where a session's delegates can be filed: under its
// current provider ref, and — for an import that has since been continued —
// under the conversation it was imported from. Continuing an import forks it
// (Session.ForkOnResume), and the harness leaves the original's delegates in
// the original's folder.
func delegateRefs(sess contracts.Session) []string {
	var refs []string
	if sess.ProviderRef != "" {
		refs = append(refs, sess.ProviderRef)
	}
	if sess.ImportedFrom != "" && sess.ImportedFrom != sess.ProviderRef {
		refs = append(refs, sess.ImportedFrom)
	}
	return refs
}
