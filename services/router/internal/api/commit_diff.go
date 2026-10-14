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

// fileDiff is the Router's response shape for one file's before/after
// content within a single commit — matches ui/src/router/types.ts's
// FileDiff (path/original/modified; `language` is guessed client-side).
type fileDiff struct {
	Path     string `json:"path"`
	Original string `json:"original"`
	Modified string `json:"modified"`
}

// handleSessionCommitDiff resolves a single file's diff for one commit via
// git, for the Files tab's Committed-file click-to-diff journey
// (ui/src/files/HANDOFF.md). Unlike commitFiles's silent-degrade-to-nil
// best-effort pattern (fine for list enrichment — the UI just omits the file
// list), this always returns an honest error status — never a 500, never a
// silent empty 200 — since a click that resolves to nothing is the bug this
// endpoint exists to fix.
func (s *server) handleSessionCommitDiff(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	hash := r.PathValue("hash")
	path := r.URL.Query().Get("path")
	if path == "" {
		writeErrMsg(w, http.StatusBadRequest, "", errors.New("path is required"))
		return
	}
	if !commitHashRe.MatchString(hash) {
		writeErrMsg(w, http.StatusBadRequest, "", errors.New("invalid commit hash"))
		return
	}

	commits, err := s.data.Commits(r.Context(), sessionID)
	if err != nil {
		writeError(w, err)
		return
	}
	var commit *contracts.Commit
	for i := range commits {
		if commits[i].Hash == hash {
			commit = &commits[i]
			break
		}
	}
	if commit == nil {
		writeErrMsg(w, http.StatusNotFound, "", errors.New("commit not found for session"))
		return
	}
	if commit.Repo == "" {
		writeErrMsg(w, http.StatusBadGateway, "", errors.New("no repo recorded for commit"))
		return
	}
	if !commitReachable(r.Context(), commit.Repo, hash) {
		writeErrMsg(w, http.StatusBadGateway, "", errors.New("commit unreachable in repo"))
		return
	}

	rel, ok := commitRelPath(r.Context(), commit.Repo, path)
	if !ok {
		writeErrMsg(w, http.StatusBadRequest, "", errors.New("path is outside the commit's repo"))
		return
	}

	// hash^:<path> failing is the expected, benign shape for an added file
	// (no parent had that path) — only the modified-side fetch is an error.
	original, _ := gitShowFile(r.Context(), commit.Repo, hash+"^", rel)
	modified, err := gitShowFile(r.Context(), commit.Repo, hash, rel)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "", errors.New("file not found in commit"))
		return
	}

	writeJSON(w, http.StatusOK, fileDiff{Path: path, Original: original, Modified: modified})
}

// commitRelPath turns the requested path into the repo-relative form `git
// show <rev>:<path>` takes. A relative path is taken as already
// repo-relative (the commit list's form). An absolute one — the Changes
// tab's form — is made relative to the work tree that holds it: the
// recorded repo, or a linked worktree of it (a worktree session's files),
// which shares the object store and so resolves the same hash. ok=false
// when the path is under neither.
func commitRelPath(ctx context.Context, repo, path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(path), true
	}
	path = shadow.RealPath(path)
	for _, root := range []string{shadow.RealPath(repo), workTreeOf(ctx, path)} {
		if root == "" {
			continue
		}
		if rel, err := filepath.Rel(root, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel), true
		}
	}
	return "", false
}

// workTreeOf is the git work tree containing path, found from the nearest
// directory that still exists (a file deleted since may have taken its
// folder with it). "" outside every repo.
func workTreeOf(ctx context.Context, path string) string {
	dir := filepath.Dir(path)
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return shadow.RealPath(strings.TrimSpace(string(out)))
}

func commitReachable(ctx context.Context, repo, hash string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "cat-file", "-e", hash+"^{commit}")
	return cmd.Run() == nil
}

func gitShowFile(ctx context.Context, repo, rev, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "show", rev+":"+path)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
