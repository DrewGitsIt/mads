package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

// The session's Commit and Land actions (docs/SESSION-WORKTREES-DESIGN.md
// D3, D5, D6):
//
//	GET  /sessions/{id}/worktree      status: files, counts, Land's blocker
//	POST /sessions/{id}/commit        commit session files
//	POST /sessions/{id}/commit/undo   take the last Commit back
//	POST /sessions/{id}/commit/draft  Fast Model commit message
//	POST /sessions/{id}/land          land the session branch into its base
//
// With a live child, status, commit, undo and land go to it: only it knows
// whether the agent is mid-turn, and it holds the user's messages back
// while a Land runs. With none, the Router runs the same sessionwt.Ops
// itself, holding the session's wake lock so nothing can start the child
// (and an agent turn) underneath it. Either way the bodies are the
// contracts.* shapes; the active-pipeline gate is the Router's to add,
// since only its supervisor knows. Drafting needs no live state and always
// runs here, where the Fast Model is.

// worktreeOps is sessionwt.Ops over the Router's own Data Service client:
// the inactive-session path.
func (s *server) worktreeOps() sessionwt.Ops { return sessionwt.Ops{Store: s.data} }

// worktreeRoute runs a worktree route against the live child when there is
// one, else directly under the session's wake lock, re-checking for a
// child once it holds the lock.
func (s *server) worktreeRoute(w http.ResponseWriter, r *http.Request, live func(addr string), direct func()) {
	id := r.PathValue("id")
	if _, err := s.data.Session(r.Context(), id); err != nil {
		writeError(w, err) // 404 before anything else
		return
	}
	if addr, ok := s.sup.SessionAddr(id); ok {
		live(addr)
		return
	}
	mu, _ := s.wakeLocks.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	if addr, ok := s.sup.SessionAddr(id); ok {
		mu.(*sync.Mutex).Unlock()
		live(addr)
		return
	}
	defer mu.(*sync.Mutex).Unlock()
	direct()
}

// childPath is a child route with the Router's pipeline gate attached.
func (s *server) childPath(id, path string) string {
	if s.sup.HasPipeline(id) {
		return path + "?pipeline=1"
	}
	return path
}

func (s *server) handleSessionWorktree(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.worktreeRoute(w, r, func(addr string) {
		s.forwardToChild(w, r, addr, http.MethodGet, s.childPath(id, "/worktree"), nil)
	}, func() {
		st, err := s.worktreeOps().Status(r.Context(), id, sessionwt.Gates{Pipeline: s.sup.HasPipeline(id)})
		if err != nil {
			writeErrMsg(w, http.StatusInternalServerError, "worktree status: ", err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
}

// handleSessionCommit commits session files. An empty message is drafted
// by the Fast Model first when Config.AutoCommitMessages is on (the
// header's one-click path, which also leaves paths empty: every session
// file), else refused; drafted comes back true on the result.
func (s *server) handleSessionCommit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req contracts.CommitRequest
	if !decode(w, r, &req) {
		return
	}
	drafted := false
	if strings.TrimSpace(req.Message) == "" {
		cfg, err := s.data.Config(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if !cfg.AutoCommitMessages {
			writeCommitRefusal(w, &sessionwt.CommitRefusal{Body: contracts.CommitError{
				Code: contracts.CommitMessageRequired, Error: "A commit message is required.", Selection: req.Paths,
			}})
			return
		}
		msg, _, err := s.draftCommitMessage(r.Context(), id, req.Paths)
		if err != nil {
			writeCommitRefusal(w, err)
			return
		}
		req.Message, drafted = msg, true
	}
	s.worktreeRoute(w, r, func(addr string) {
		status, raw, err := callChild(r.Context(), addr, http.MethodPost, "/commit", req)
		if err != nil {
			writeErrMsg(w, http.StatusBadGateway, "commit: ", err)
			return
		}
		if status == http.StatusOK && drafted {
			var res contracts.CommitResult
			if json.Unmarshal(raw, &res) == nil {
				res.Drafted = true
				writeJSON(w, status, res)
				return
			}
		}
		writeRaw(w, status, raw)
	}, func() {
		res, err := s.worktreeOps().Commit(context.WithoutCancel(r.Context()), id, req)
		if err != nil {
			writeCommitRefusal(w, err)
			return
		}
		res.Drafted = drafted
		writeJSON(w, http.StatusOK, res)
	})
}

func (s *server) handleSessionUndoCommit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req contracts.UndoCommitRequest
	if !decode(w, r, &req) {
		return
	}
	s.worktreeRoute(w, r, func(addr string) {
		s.forwardToChild(w, r, addr, http.MethodPost, "/commit/undo", req)
	}, func() {
		res, err := s.worktreeOps().Undo(context.WithoutCancel(r.Context()), id, req.Hash)
		if err != nil {
			writeErrMsg(w, http.StatusInternalServerError, "undo commit: ", err)
			return
		}
		writeJSON(w, sessionwt.UndoStatus(res), res)
	})
}

// handleSessionLand answers 200 for landed, conflicted and refused alike —
// each is a card — and an error status only for something unexpected. It
// runs to the end even if the request goes away.
func (s *server) handleSessionLand(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.worktreeRoute(w, r, func(addr string) {
		s.forwardToChild(w, r, addr, http.MethodPost, s.childPath(id, "/land"), struct{}{})
	}, func() {
		res, err := s.worktreeOps().Land(context.WithoutCancel(r.Context()), id, sessionwt.Gates{Pipeline: s.sup.HasPipeline(id)})
		if err != nil {
			writeErrMsg(w, http.StatusInternalServerError, "land: ", err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

// handleSessionCommitDraft has the Fast Model draft a commit message for
// the selected session files (all of them when none are given).
func (s *server) handleSessionCommitDraft(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req contracts.CommitDraftRequest
	if !decode(w, r, &req) {
		return
	}
	if _, err := s.data.Session(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	msg, truncated, err := s.draftCommitMessage(r.Context(), id, req.Paths)
	if err != nil {
		writeCommitRefusal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, contracts.CommitDraft{Message: msg, Truncated: truncated})
}

const commitDraftSystem = `You write git commit messages. Reply with the message only: a subject line of at most 72 characters in the imperative mood ("Add", "Fix", not "Added"), then, only if the change needs explaining, a blank line and a short body wrapped at 72 columns saying why. No code fences, no quotes, no trailers, no preamble.`

// draftCommitMessage asks the Fast Model for a commit message describing
// the diff of the given session files, validated exactly as Commit would
// validate them. The diff is capped (sessionwt.DraftDiffLimit) and the
// prompt says when it was cut. Failures are *sessionwt.CommitRefusal:
// no_fast_model when none is configured, draft_failed when it answered
// unusably.
func (s *server) draftCommitMessage(ctx context.Context, id string, paths []string) (msg string, truncated bool, err error) {
	folder, files, err := s.worktreeOps().SelectForDraft(ctx, id, paths)
	if err != nil {
		return "", false, err
	}
	diff, truncated, err := sessionwt.DraftDiff(ctx, folder, files, sessionwt.DraftDiffLimit)
	if err != nil {
		return "", false, err
	}
	fm := s.jobs.FastModel()
	if fm == nil {
		return "", false, &sessionwt.CommitRefusal{Body: contracts.CommitError{
			Code: contracts.CommitNoFastModel, Error: "No Fast Model is configured to write commit messages.",
		}}
	}
	prompt := "Files: " + strings.Join(files, ", ") + "\n\n"
	if truncated {
		prompt += fmt.Sprintf("The diff below was cut at %d KB; describe the change from what is shown and the file list.\n\n", sessionwt.DraftDiffLimit>>10)
	}
	prompt += "Diff:\n" + diff
	text, _, err := fm.DoText(ctx, fastmodel.Job{System: commitDraftSystem, Prompt: prompt})
	if errors.Is(err, fastmodel.ErrBackendUnconfigured) {
		return "", false, &sessionwt.CommitRefusal{Body: contracts.CommitError{
			Code: contracts.CommitNoFastModel, Error: "No Fast Model is configured to write commit messages.",
		}}
	}
	if msg = cleanCommitDraft(text); err != nil || msg == "" {
		detail := "the Fast Model returned an empty message"
		if err != nil {
			detail = err.Error()
		}
		return "", false, &sessionwt.CommitRefusal{Body: contracts.CommitError{
			Code: contracts.CommitDraftFailed, Error: "The Fast Model couldn't draft a message.", Detail: detail,
		}}
	}
	return msg, truncated, nil
}

// cleanCommitDraft strips what models wrap messages in anyway: code fences
// and surrounding quotes.
func cleanCommitDraft(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```")
		if nl := strings.IndexByte(text, '\n'); nl >= 0 && !strings.Contains(text[:nl], " ") {
			text = text[nl+1:] // a language tag
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	return strings.Trim(strings.TrimSpace(text), "\"'`")
}

func writeCommitRefusal(w http.ResponseWriter, err error) {
	var refusal *sessionwt.CommitRefusal
	if errors.As(err, &refusal) {
		writeJSON(w, refusal.HTTPStatus(), refusal.Body)
		return
	}
	writeErrMsg(w, http.StatusInternalServerError, "commit: ", err)
}

// forwardToChild sends body (nil for none) to the child and writes its
// answer back unchanged.
func (s *server) forwardToChild(w http.ResponseWriter, r *http.Request, addr, method, path string, body any) {
	status, raw, err := callChild(r.Context(), addr, method, path, body)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "session child: ", err)
		return
	}
	writeRaw(w, status, raw)
}

// callChild makes one JSON request to a session child. Mutations are sent
// without the caller's cancellation: the child finishes a Commit or Land
// whatever happens to the request, and its events say how it went.
func callChild(ctx context.Context, addr, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(raw)
		ctx = context.WithoutCancel(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+addr+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

func writeRaw(w http.ResponseWriter, status int, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
