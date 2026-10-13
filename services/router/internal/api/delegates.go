package api

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/delegates"
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
	writeJSON(w, http.StatusOK, s.delegateTranscriptFor(r.Context(), sess, r.PathValue("harnessId")))
}

// delegateTranscriptFor reads one delegate's transcript from the harness's
// store; shared by the endpoint above and the conversation export.
func (s *server) delegateTranscriptFor(ctx context.Context, sess contracts.Session, harnessID string) contracts.DelegateTranscript {
	out := contracts.DelegateTranscript{Messages: []contracts.TranscriptMessage{}}
	p, err := s.providers.Get(sess.Provider)
	if err != nil {
		return out
	}
	inspector, ok := p.(provider.DelegateInspector)
	if !ok {
		return out
	}
	for _, ref := range delegateRefs(sess) {
		msgs, found, err := inspector.DelegateTranscript(ctx, ref, harnessID)
		if err != nil {
			log.Printf("api: delegate transcript: session %s delegate %s: %v", sess.ID, harnessID, err)
			continue
		}
		if found {
			return contracts.DelegateTranscript{Available: true, Messages: normalizeTurns(msgs)}
		}
	}
	return out
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

// RecordImportedDelegates gives an imported session the delegate records a
// live one would have: every subagent its harness's history holds for it
// (provider.DelegateInspector.DelegateHistory), under both of its refs (see
// delegateRefs), as delegates.FromHistory maps it. Only for a session with no
// records yet: once it has any, its runner keeps them, and history must not
// overwrite what it has since learned (a re-run, a capability check).
// Returns how many it recorded.
func RecordImportedDelegates(ctx context.Context, data *dataclient.Client, providers *provider.Registry, sess contracts.Session) (int, error) {
	if sess.ImportedFrom == "" {
		return 0, nil
	}
	p, err := providers.Get(sess.Provider)
	if err != nil {
		return 0, nil // a provider no longer installed: nothing to read with
	}
	inspector, ok := p.(provider.DelegateInspector)
	if !ok {
		return 0, nil
	}
	existing, err := data.SessionDelegates(ctx, sess.ID)
	if err != nil || len(existing) > 0 {
		return 0, err
	}
	defaults := delegates.Defaults(p.Matrix())
	recorded, seen := 0, map[string]bool{}
	for _, ref := range delegateRefs(sess) {
		history, err := inspector.DelegateHistory(ctx, ref)
		if err != nil {
			return recorded, err
		}
		for _, r := range history {
			if seen[r.HarnessID] {
				continue
			}
			seen[r.HarnessID] = true
			if _, err := data.PutDelegate(ctx, delegates.FromHistory(sess.ID, r, defaults)); err != nil {
				return recorded, err
			}
			recorded++
		}
	}
	return recorded, nil
}

// BackfillImportedDelegates runs RecordImportedDelegates over every imported
// session — the ones imported before delegates were recorded at import. Each
// one with records already is skipped, so a later run costs a list and a
// folder glob per import. Best effort: a failure is logged, never fatal.
func BackfillImportedDelegates(ctx context.Context, data *dataclient.Client, providers *provider.Registry) {
	sessions, err := data.Sessions(ctx, "")
	if err != nil {
		log.Printf("api: backfill imported delegates: list sessions: %v", err)
		return
	}
	total := 0
	for _, sess := range sessions {
		if ctx.Err() != nil {
			return
		}
		n, err := RecordImportedDelegates(ctx, data, providers, sess)
		if err != nil {
			log.Printf("api: backfill imported delegates: session %s: %v", sess.ID, err)
		}
		total += n
	}
	if total > 0 {
		log.Printf("api: recorded %d delegates from imported sessions' history", total)
	}
}
