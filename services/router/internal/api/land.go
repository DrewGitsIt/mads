package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

// The Land confirmation (docs/COMMIT-LAND-PUSH-DESIGN.md D12):
//
//	GET  /sessions/{id}/land?repo=&base=   what a Land would squash, and into what
//	POST /sessions/{id}/land/draft         a Fast Model draft of the landing message
//
// Both read only, so the Router answers them itself, child or not.

func (s *server) handleSessionLandPreview(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	p, err := s.worktreeOps().LandPreview(r.Context(), sess.ID, q.Get("repo"), q.Get("base"), s.pushGates(r.Context(), sess))
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "land preview: ", err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

const landDraftSystem = `You write the message for one commit that lands a whole feature branch on its base. Reply with the message only: a subject line of at most 72 characters in the imperative mood ("Add", "Fix", not "Added") saying what the branch does as a whole, then, only if it needs explaining, a blank line and a short body wrapped at 72 columns saying why. No code fences, no quotes, no trailers, no list of the commits, no preamble.`

func (s *server) handleSessionLandDraft(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req contracts.LandDraftRequest
	if !decode(w, r, &req) {
		return
	}
	if _, err := s.data.Session(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	diff, truncated, subjects, err := s.worktreeOps().LandDraftInput(r.Context(), id, req.Repo, req.Base, sessionwt.DraftDiffLimit)
	if err != nil {
		writeCommitRefusal(w, err)
		return
	}
	fm := s.jobs.FastModel()
	if fm == nil {
		writeCommitRefusal(w, &sessionwt.CommitRefusal{Body: contracts.CommitError{
			Code: contracts.CommitNoFastModel, Error: "No Fast Model is configured to write landing messages.",
		}})
		return
	}
	prompt := "Commits on the branch, oldest first:\n- " + strings.Join(subjects, "\n- ") + "\n\n"
	if truncated {
		prompt += fmt.Sprintf("The diff below was cut at %d KB; describe the change from what is shown and the commits.\n\n", sessionwt.DraftDiffLimit>>10)
	}
	prompt += "Diff against the base:\n" + diff
	text, _, err := fm.DoText(r.Context(), fastmodel.Job{System: landDraftSystem, Prompt: prompt})
	if errors.Is(err, fastmodel.ErrBackendUnconfigured) {
		writeCommitRefusal(w, &sessionwt.CommitRefusal{Body: contracts.CommitError{
			Code: contracts.CommitNoFastModel, Error: "No Fast Model is configured to write landing messages.",
		}})
		return
	}
	msg := cleanCommitDraft(text)
	if err != nil || msg == "" {
		detail := "the Fast Model returned an empty message"
		if err != nil {
			detail = err.Error()
		}
		writeCommitRefusal(w, &sessionwt.CommitRefusal{Body: contracts.CommitError{
			Code: contracts.CommitDraftFailed, Error: "The Fast Model couldn't draft a message.", Detail: detail,
		}})
		return
	}
	writeJSON(w, http.StatusOK, contracts.CommitDraft{Message: msg, Truncated: truncated})
}
