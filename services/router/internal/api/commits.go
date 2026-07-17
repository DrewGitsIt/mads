package api

import (
	"context"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func (s *server) handleSessionCommits(w http.ResponseWriter, r *http.Request) {
	commits, err := s.data.Commits(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]commitWithFiles, len(commits))
	for i, c := range commits {
		out[i] = commitWithFiles{Commit: c, Files: commitFiles(r.Context(), c)}
	}
	writeJSON(w, http.StatusOK, out)
}

// commitWithFiles is the Router's response shape — contracts.Commit plus a
// best-effort file list this handler derives via git, never persisted by
// the Data Service (spec: derived-at-read, not stored).
type commitWithFiles struct {
	contracts.Commit
	Files []commitFile `json:"files,omitempty"`
}

type commitFile struct {
	Path   string `json:"path"`
	Change string `json:"change"` // added | modified | deleted — matches contracts.FileChange's vocabulary
}

var commitHashRe = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// commitFiles best-effort resolves c's file list via `git show --name-status`
// against c.Repo (the primary checkout the post-commit hook reported, see
// pkg/githook — NOT a per-task worktree, which may already be removed by
// merge-time cleanup; git-common-dir resolution in the hook script makes
// c.Repo point at the surviving primary even for a commit originally made
// in a now-deleted task worktree). Returns nil — never an error — for any
// commit this can't resolve: empty Repo (recorded before this change, or
// the hook's git-common-dir resolution failed), a Repo directory that no
// longer exists, or a hash git can't find. The UI renders the commit card
// without a file list in that case; it must never fail the whole request.
func commitFiles(ctx context.Context, c contracts.Commit) []commitFile {
	if c.Repo == "" || !commitHashRe.MatchString(c.Hash) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", c.Repo, "show", "--no-color", "--name-status", "--format=", c.Hash)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []commitFile
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		files = append(files, commitFile{Path: fields[len(fields)-1], Change: mapGitStatus(fields[0])})
	}
	return files
}

// mapGitStatus collapses git's --name-status letters (A/M/D/R100/C100/T...)
// onto contracts.FileChange's three-way vocabulary, reusing the CommitList/
// ChangeList UI's existing A/M/D chip styling rather than inventing a
// fourth rename/copy visual.
func mapGitStatus(status string) string {
	switch {
	case strings.HasPrefix(status, "A"):
		return "added"
	case strings.HasPrefix(status, "D"):
		return "deleted"
	default:
		return "modified"
	}
}
