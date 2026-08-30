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
)

// handleSessionWorkingDiff resolves one working (uncommitted) file's
// before/after content for the Files tab's working-change click-to-diff —
// the counterpart of handleSessionCommitDiff for files the session touched
// but hasn't committed. "Original" is the file at HEAD in its containing
// repo (empty for an added or non-git file), "modified" is the file on disk
// right now (empty for a deleted file).
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
	original := headContent(r.Context(), change.Path)

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

// headContent best-effort resolves the file's content at HEAD of whatever
// git repo contains it (the session's checkout or worktree — the ledger
// records absolute paths). Every failure — not in a repo, not in HEAD
// (added file), no HEAD at all — is the benign empty-original shape, same
// as commit_diff's added-file case.
func headContent(ctx context.Context, path string) string {
	dir := filepath.Dir(path)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	//nolint:gosec // path is gated by the session's file-change ledger
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	repo := strings.TrimSpace(string(out))
	rel, err := filepath.Rel(repo, path)
	if err != nil {
		return ""
	}
	original, err := gitShowFile(ctx, repo, "HEAD", filepath.ToSlash(rel))
	if err != nil {
		return ""
	}
	return original
}
