package api

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
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

	// hash^:<path> failing is the expected, benign shape for an added file
	// (no parent had that path) — only the modified-side fetch is an error.
	original, _ := gitShowFile(r.Context(), commit.Repo, hash+"^", path)
	modified, err := gitShowFile(r.Context(), commit.Repo, hash, path)
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "", errors.New("file not found in commit"))
		return
	}

	writeJSON(w, http.StatusOK, fileDiff{Path: path, Original: original, Modified: modified})
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
