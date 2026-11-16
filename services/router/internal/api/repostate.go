package api

import (
	"context"
	"errors"
	"net/http"

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

// handleSessionRepos lists every repo of the session's set — the ones with
// nothing uncommitted too, since they may still have commits to push —
// with each checkout's branch facts, and for an isolated session the base
// in the user's checkout, which a push after Land publishes. A repo whose
// facts can't be read (deleted since) is left out.
func (s *server) handleSessionRepos(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess, err := s.data.Session(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	primary, all, _, err := s.worktreeOps().Repos(ctx, sess.ID)
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "session repos: ", err)
		return
	}
	out := []contracts.SessionRepoState{}
	for i, t := range all {
		wt, err := s.repoWorktree(ctx, t.Folder)
		if err != nil {
			continue
		}
		rs := contracts.SessionRepoState{Root: t.Folder, Primary: primary != nil && i == 0, Isolated: t.Isolated(), Base: t.Base, Worktree: wt}
		if t.Isolated() {
			if base, err := s.repoBranch(ctx, t.Primary, t.Base); err == nil {
				rs.BaseWorktree = &base
			}
		}
		out = append(out, rs)
	}
	writeJSON(w, http.StatusOK, out)
}
