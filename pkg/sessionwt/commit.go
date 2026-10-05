package sessionwt

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// CommitRefusal is a Commit (or draft) that didn't happen for a reason the
// dialog shows in place, keeping the selection: a structured outcome, not
// a failure of the service. Body is the wire shape.
type CommitRefusal struct {
	Body contracts.CommitError
}

func (r *CommitRefusal) Error() string {
	return "commit refused (" + r.Body.Code + "): " + r.Body.Error
}

// HTTPStatus is the status a refusal is served with: 400 for a request
// that can't be right (bad paths, no message), 409 for one the session's
// state rules out right now, 422 when git itself said no, 502 when the
// Fast Model failed to draft.
func (r *CommitRefusal) HTTPStatus() int {
	switch r.Body.Code {
	case contracts.CommitInvalidPaths, contracts.CommitMessageRequired:
		return http.StatusBadRequest
	case contracts.CommitGitFailed:
		return http.StatusUnprocessableEntity
	case contracts.CommitDraftFailed:
		return http.StatusBadGateway
	default: // nothing_to_commit, no_folder, no_fast_model
		return http.StatusConflict
	}
}

// UndoStatus is the status an undo result is served with: 200 when it
// undid, 409 when it refused.
func UndoStatus(res contracts.UndoCommitResult) int {
	if res.Undone {
		return http.StatusOK
	}
	return http.StatusConflict
}

func refuseCommit(code, msg string) *CommitRefusal {
	return &CommitRefusal{Body: contracts.CommitError{Code: code, Error: msg}}
}

// trailer is the line every Commit and Land commit carries, for people
// reading the log (D5) and for Undo to recognize a Commit-made commit.
func trailer(sessionID string) string { return "Session: " + sessionID }

// SelectPaths checks a Commit request's paths against the session's change
// list (D5: paths are validated on the server, not trusted). Each must be
// relative to the session's folder, stay inside it with symlinks followed,
// and be a file this session changed — rows a pipeline task produced live
// in task worktrees and never qualify. Empty paths selects every session
// file. ok is false when any path was rejected; results then say which and
// why, and nothing may be committed.
func SelectPaths(t Target, rows []contracts.FileChange, paths []string) (valid []string, results []contracts.CommitPath, ok bool) {
	session := SessionFiles(t.Folder, rows)
	if len(paths) == 0 {
		return session, nil, true
	}
	isSession := map[string]bool{}
	for _, p := range session {
		isSession[p] = true
	}
	var taskRows []contracts.FileChange
	for _, row := range rows {
		if row.TaskID != "" {
			row.TaskID = ""
			taskRows = append(taskRows, row)
		}
	}
	isTask := map[string]bool{}
	for _, p := range SessionFiles(t.Folder, taskRows) {
		isTask[p] = true
	}

	root := resolve(t.Folder)
	ok = true
	seen := map[string]bool{}
	for _, p := range paths {
		rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
		reason := ""
		switch {
		case p == "" || filepath.IsAbs(p) || rel == "." || rel == ".." || strings.HasPrefix(rel, "../"):
			reason = "outside_folder"
		case !inside(root, resolve(filepath.Join(t.Folder, filepath.FromSlash(rel)))):
			reason = "outside_folder" // a symlink on the way out of the folder
		case isSession[rel]:
		case isTask[rel]:
			reason = "pipeline_task_file"
		default:
			reason = "not_session_file"
		}
		if reason != "" {
			ok = false
			results = append(results, contracts.CommitPath{Path: p, Status: contracts.CommitPathRejected, Reason: reason})
			continue
		}
		if !seen[rel] {
			seen[rel] = true
			valid = append(valid, rel)
		}
	}
	return valid, results, ok
}

// inside reports whether p is root or below it; both already resolved.
func inside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// CommitFiles is D5's Commit, git side: it stages exactly the selected
// session files (deletions included) and commits only them — anything else
// the user had staged stays staged and out of the commit — with the user's
// own identity and the message followed by a Session: trailer. A selected
// file git no longer sees as changed (a pipeline merge committed it, D8)
// is reported "already committed" rather than failing the rest.
//
// Refusals come back as *CommitRefusal: invalid paths, nothing left to
// commit, no message, or git itself saying no (Detail carries its output
// verbatim — a pre-commit hook, a missing user.name, a signing failure).
// The caller holds the session lock, and records the commit.
func CommitFiles(ctx context.Context, t Target, rows []contracts.FileChange, req contracts.CommitRequest) (contracts.CommitResult, error) {
	valid, results, ok := SelectPaths(t, rows, req.Paths)
	if !ok {
		r := refuseCommit(contracts.CommitInvalidPaths, "Some of the selected files can't be committed from this session.")
		r.Body.Paths, r.Body.Selection = results, req.Paths
		return contracts.CommitResult{}, r
	}
	changed, err := changes(ctx, t.Folder, valid)
	if err != nil {
		return contracts.CommitResult{}, err
	}
	isChanged := map[string]bool{}
	for _, f := range changed {
		isChanged[f.Path] = true
	}
	var commit []string
	for _, p := range valid {
		if isChanged[p] {
			commit = append(commit, p)
			results = append(results, contracts.CommitPath{Path: p, Status: contracts.CommitPathCommitted})
		} else {
			results = append(results, contracts.CommitPath{Path: p, Status: contracts.CommitPathAlreadyCommitted})
		}
	}
	selection := req.Paths
	if len(selection) == 0 {
		selection = commit
	}
	if len(commit) == 0 {
		r := refuseCommit(contracts.CommitNothingToCommit, "Nothing to commit: the selected files have no changes git can see.")
		r.Body.Paths, r.Body.Selection = results, req.Paths
		return contracts.CommitResult{}, r
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		r := refuseCommit(contracts.CommitMessageRequired, "A commit message is required.")
		r.Body.Selection = selection
		return contracts.CommitResult{}, r
	}

	gitRefusal := func(out string, err error) error {
		r := refuseCommit(contracts.CommitGitFailed, "git refused the commit.")
		r.Body.Detail, r.Body.Paths, r.Body.Selection = gitOutput(out, err), results, selection
		return r
	}
	// `git add -A` refuses a path that is in neither the index nor the
	// tree, which is exactly a deletion already staged, so deletions stage
	// through rm --cached instead.
	var present, gone []string
	for _, p := range commit {
		if _, err := os.Lstat(filepath.Join(t.Folder, filepath.FromSlash(p))); errors.Is(err, fs.ErrNotExist) {
			gone = append(gone, p)
		} else {
			present = append(present, p)
		}
	}
	if len(gone) > 0 {
		if out, err := userGit(ctx, t.Folder, append([]string{"--literal-pathspecs", "rm", "--cached", "--ignore-unmatch", "-q", "--"}, gone...)...); err != nil {
			return contracts.CommitResult{}, gitRefusal(out, err)
		}
	}
	if len(present) > 0 {
		if out, err := userGit(ctx, t.Folder, append([]string{"--literal-pathspecs", "add", "-A", "--"}, present...)...); err != nil {
			return contracts.CommitResult{}, gitRefusal(out, err)
		}
	}
	args := append([]string{"--literal-pathspecs", "commit", "--only", "-m", msg, "-m", trailer(t.SessionID), "--"}, commit...)
	if out, err := userGit(ctx, t.Folder, args...); err != nil {
		return contracts.CommitResult{}, gitRefusal(out, err)
	}
	hash, err := Git(ctx, t.Folder, "rev-parse", "HEAD")
	if err != nil {
		return contracts.CommitResult{}, err
	}
	body, err := Git(ctx, t.Folder, "log", "-1", "--format=%B")
	if err != nil {
		return contracts.CommitResult{}, err
	}
	return contracts.CommitResult{
		Hash: strings.TrimSpace(hash), Message: strings.TrimSpace(body), Undoable: true, Paths: results,
	}, nil
}

// Subject is a commit message's first line.
func Subject(message string) string {
	s, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return strings.TrimSpace(s)
}

// UndoCommit takes back a Commit-made commit with `git reset --soft
// HEAD~1`, leaving its changes staged — only while hash is still the tip
// of the session's folder, carries this session's trailer, has one parent,
// and (isolated) hasn't landed. Anything else is a refusal with a reason,
// not an error. Caller holds the session lock.
func UndoCommit(ctx context.Context, t Target, hash string) (contracts.UndoCommitResult, error) {
	res := contracts.UndoCommitResult{Hash: hash}
	refuse := func(reason, detail string) (contracts.UndoCommitResult, error) {
		res.Reason, res.Detail = reason, detail
		return res, nil
	}
	if !exists(t.Folder) {
		return refuse("no_folder", "The session's folder is missing.")
	}
	out, err := Git(ctx, t.Folder, "log", "-1", "--format=%H%x00%P%x00%B")
	if err != nil {
		return contracts.UndoCommitResult{}, err
	}
	parts := strings.SplitN(out, "\x00", 3)
	if len(parts) < 3 {
		return contracts.UndoCommitResult{}, fmt.Errorf("sessionwt: unexpected log output %q", out)
	}
	full := ""
	if hash != "" {
		if h, err := Git(ctx, t.Folder, "rev-parse", "-q", "--verify", hash+"^{commit}"); err == nil {
			full = strings.TrimSpace(h)
		}
	}
	if full == "" || full != parts[0] {
		return refuse("not_tip", "Only the newest commit can be undone, and this one isn't any more.")
	}
	if reason := undoRefusal(ctx, t, parts[0], parts[1], parts[2]); reason != "" {
		return refuse(reason, map[string]string{
			"not_session_commit": "This commit wasn't made by this session's Commit.",
			"merge_commit":       "This is a merge commit; Undo only takes back a single commit.",
			"no_parent":          "This is the repository's first commit; there is nothing to go back to.",
			"already_landed":     "This commit has already landed in " + t.Base + ".",
		}[reason])
	}
	if op := midOperation(ctx, t.Folder); op != "" {
		return refuse("mid_merge", "The session's folder is in the middle of a "+op+".")
	}
	if out, err := Git(ctx, t.Folder, "reset", "-q", "--soft", "HEAD~1"); err != nil {
		return refuse("git_failed", gitOutput(out, err))
	}
	res.Hash, res.Undone = full, true
	return res, nil
}

// undoRefusal is why the commit (hash, its parents, its message) can't be
// undone, or "" if it can.
func undoRefusal(ctx context.Context, t Target, hash, parents, body string) string {
	hasTrailer := false
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == trailer(t.SessionID) {
			hasTrailer = true
		}
	}
	switch n := len(strings.Fields(parents)); {
	case !hasTrailer:
		return "not_session_commit"
	case n == 0:
		return "no_parent"
	case n > 1:
		return "merge_commit"
	}
	if t.Isolated() && t.Base != "" && refExists(ctx, t.Folder, "refs/heads/"+t.Base) {
		if _, err := Git(ctx, t.Folder, "merge-base", "--is-ancestor", hash, "refs/heads/"+t.Base); err == nil {
			return "already_landed"
		}
	}
	return ""
}

// DraftDiffLimit bounds the diff a commit-message draft sees: enough for
// any ordinary change, small enough for a fast model's prompt.
const DraftDiffLimit = 40 << 10

// DraftDiff is the diff of the given session files (relative to the
// folder; SelectPaths has vetted them) for the Fast Model to describe:
// tracked changes against HEAD, then each new untracked file in full. Cut
// at limit bytes, with truncated reporting it. Read-only, no lock.
func DraftDiff(ctx context.Context, folder string, files []string, limit int) (diff string, truncated bool, err error) {
	if len(files) == 0 {
		return "", false, nil
	}
	var b strings.Builder
	args := append([]string{"--no-optional-locks", "--literal-pathspecs", "diff", "--no-color", "--no-ext-diff", "HEAD", "--"}, files...)
	if out, err := Git(ctx, folder, args...); err == nil {
		b.WriteString(out)
	}
	status, err := Git(ctx, folder, append([]string{"--no-optional-locks", "--literal-pathspecs", "status", "--porcelain", "-z", "--untracked-files=all", "--"}, files...)...)
	if err != nil {
		return "", false, err
	}
	for _, e := range parseStatusZ(status) {
		if e.x != '?' || b.Len() >= limit {
			continue
		}
		// --no-index exits 1 when the sides differ, which they always do here.
		cmd := exec.CommandContext(ctx, "git", "diff", "--no-index", "--no-color", "--no-ext-diff", "--", os.DevNull, e.path)
		cmd.Dir = folder
		out, _ := cmd.Output()
		b.Write(out)
	}
	diff = b.String()
	if len(diff) > limit {
		return diff[:limit], true, nil
	}
	return diff, false, nil
}
