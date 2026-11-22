package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/repostate"
)

// Repo state (docs/COMMIT-LAND-PUSH-DESIGN.md D10): facts about the
// branches sessions work on, kept once per repo and current to the last
// settled change of its refs; repo.changed says when to read them again.
//
//	GET /repos/state?path=      one checkout's branch facts
//	GET /sessions/{id}/repos    the session's repos, each with its facts

// repoWorktree is the branch facts of the checkout holding path, from the
// service's cache, or computed now without one.
func (s *server) repoWorktree(ctx context.Context, path string) (contracts.RepoWorktree, error) {
	if s.repoState != nil {
		return s.repoState.Worktree(ctx, path)
	}
	return repostate.Compute(ctx, path)
}

// repoBranch is repoWorktree for a branch the checkout needn't have
// checked out.
func (s *server) repoBranch(ctx context.Context, path, branch string) (contracts.RepoWorktree, error) {
	if s.repoState != nil {
		return s.repoState.Branch(ctx, path, branch)
	}
	return repostate.ComputeBranch(ctx, path, branch)
}

var errPathRequired = errors.New("path is required")

func (s *server) handleRepoState(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeErrMsg(w, http.StatusBadRequest, "", errPathRequired)
		return
	}
	st, err := s.repoWorktree(r.Context(), path)
	if err != nil {
		writeErrMsg(w, http.StatusNotFound, "repo state: ", err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleSessionRepos serves GET /sessions/{id}/repos: the session's repos
// with their branch facts (computeSessionRepos), as the gitstate service
// last computed them (docs/GIT-STATE-DESIGN.md). The body is the bare
// array it always was; X-Mads-Updating (1 while a recompute is queued or
// running) and X-Mads-Computed-At (RFC 3339, absent before a first
// compute) say how current it is. Cold: [] with X-Mads-Updating: 1, which
// readers take as "loading", not "no repos".
func (s *server) handleSessionRepos(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	a := s.gitState.Repos(sess.ID)
	if a.Err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "session repos: ", a.Err)
		return
	}
	state := a.State
	if state == nil {
		state = []contracts.SessionRepoState{}
	}
	if a.Updating || !a.Computed {
		w.Header().Set("X-Mads-Updating", "1")
	} else {
		w.Header().Set("X-Mads-Updating", "0")
	}
	if a.Computed {
		w.Header().Set("X-Mads-Computed-At", a.ComputedAt.UTC().Format(time.RFC3339Nano))
	}
	writeJSON(w, http.StatusOK, state)
}
