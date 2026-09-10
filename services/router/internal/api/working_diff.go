package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// handleSessionWorkingDiff resolves one working file's before/after content
// for the Files tab's working-change click-to-diff — the counterpart of
// handleSessionCommitDiff for files the session touched outside a selected
// commit. "Original" is the file at the session's base — the parent of the
// session's earliest recorded commit in the containing repo, falling back
// to HEAD when the session has no commits there (empty for an added or
// non-git file); "modified" is the file on disk right now (empty for a
// deleted file). Diffing against plain HEAD would erase the session's own
// work the moment the agent commits it (HEAD catches up to disk), which is
// the common shape for a mads session.
//
// The requested path must appear in the session's recorded file-change
// ledger (scoped to task_id when given): that's both the honest 404 for a
// stale click and the security gate — path is caller-controlled, and
// without the ledger check this endpoint would read any file on disk.
func (s *server) handleSessionWorkingDiff(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	path := r.URL.Query().Get("path")
	taskID := r.URL.Query().Get("task_id")
	if path == "" {
		writeErrMsg(w, http.StatusBadRequest, "", errors.New("path is required"))
		return
	}

	changes, err := s.data.FileChanges(r.Context(), sessionID)
	if err != nil {
		writeError(w, err)
		return
	}
	var change *contracts.FileChange
	for i := range changes {
		if changes[i].Path != path {
			continue
		}
		if taskID != "" && changes[i].TaskID != taskID {
			continue
		}
		change = &changes[i]
		break
	}
	if change == nil {
		writeErrMsg(w, http.StatusNotFound, "", errors.New("file not changed in this session"))
		return
	}

	modified, err := readWorkingFile(change.Path)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "", errors.New("file unreadable on disk"))
		return
	}
	original := s.workingOriginal(r.Context(), sessionID, change.Path)

	writeJSON(w, http.StatusOK, fileDiff{Path: path, Original: original, Modified: modified})
}

// readWorkingFile reads the file's current on-disk content. A missing file
// is the expected shape for a deleted change — empty modified side, not an
// error; anything else unreadable (EISDIR, permissions) is.
func readWorkingFile(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is gated by the session's file-change ledger
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// workingOriginal best-effort resolves the file's pre-session content.
// First choice is the session's shadow baseline commit (docs/
// SHADOW-GIT-DIFFS-ADR.md) — the root's literal state when the session
// first touched it, which works even for sessions that never commit and
// for non-git roots. Absent a usable shadow (older session, baseline still
// running, shadowing disabled), fall back to whatever git repo contains
// the path: the session-base rev when the session has recorded commits in
// that repo (the pre-shadow heuristic — parent of the earliest recorded
// commit), HEAD otherwise. Every failure — not in a repo, not present at
// the base rev (file the session added, base of a root commit), no HEAD at
// all — is the benign empty-original shape, same as commit_diff's
// added-file case.
func (s *server) workingOriginal(ctx context.Context, sessionID, path string) string {
	dir := filepath.Dir(path)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	//nolint:gosec // path is gated by the session's file-change ledger
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	repo := ""
	if err == nil {
		repo = strings.TrimSpace(string(out))
	}

	// Shadow roots mirror the runner's discovery rule: the containing git
	// repo, or the file's own directory when it isn't in one.
	if sess, err := s.data.Session(ctx, sessionID); err == nil && sess.ShadowDir != "" {
		root := repo
		if root == "" {
			root = dir
		}
		if rel, err := filepath.Rel(root, path); err == nil {
			if original, ok := shadow.BaselineOriginal(ctx, sess.ShadowDir, root, filepath.ToSlash(rel)); ok {
				return original
			}
		}
	}

	if repo == "" {
		return ""
	}
	rel, err := filepath.Rel(repo, path)
	if err != nil {
		return ""
	}
	rev := "HEAD"
	if base := s.sessionBaseRev(ctx, sessionID, repo); base != "" {
		rev = base
	}
	original, err := gitShowFile(ctx, repo, rev, filepath.ToSlash(rel))
	if err != nil {
		return ""
	}
	return original
}

// sessionBaseRev finds the parent of the session's earliest recorded commit
// that exists in repo — the repo's state before the session started work.
// Commit.Repo isn't trusted for the repo match (it can be empty or a
// differently-normalized path); `git cat-file -e` on the hash is. Returns ""
// (caller falls back to HEAD) when the session has no commits in this repo;
// a `<root-commit>^` result is fine — gitShowFile fails on it and the caller
// lands on the empty-original shape, which is correct for a repo the
// session itself created.
func (s *server) sessionBaseRev(ctx context.Context, sessionID, repo string) string {
	commits, err := s.data.Commits(ctx, sessionID)
	if err != nil {
		return ""
	}
	// Data Service returns commits in record order, which is chronological.
	for _, c := range commits {
		if c.Hash == "" {
			continue
		}
		//nolint:gosec // hash comes from the session's recorded commits, repo from rev-parse
		cmd := exec.CommandContext(ctx, "git", "-C", repo, "cat-file", "-e", c.Hash+"^{commit}")
		if cmd.Run() == nil {
			return c.Hash + "^"
		}
	}
	return ""
}
