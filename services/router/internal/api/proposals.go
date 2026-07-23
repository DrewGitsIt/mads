package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// This file is a pure pass-through over dataclient's proposal methods — same
// shape as tasks.go. The Data Service already owns validation/persistence
// (services/data/internal/api/proposals.go); Router just proxies the wire
// shape. No POST /proposals or GET /proposals/{id} proxy: the UI only ever
// lists a session's proposals and approves/dismisses them — proposals are
// authored by the synthesis job (services/router/internal/job), which talks
// to the Data Service directly.

func (s *server) handleListProposals(w http.ResponseWriter, r *http.Request) {
	proposals, err := s.data.Proposals(r.Context(), r.URL.Query().Get("session_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if proposals == nil {
		proposals = []contracts.Proposal{}
	}
	writeJSON(w, http.StatusOK, proposals)
}

func (s *server) handleApproveProposal(w http.ResponseWriter, r *http.Request) {
	p, err := s.data.ApproveProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) handleDismissProposal(w http.ResponseWriter, r *http.Request) {
	p, err := s.data.DismissProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
