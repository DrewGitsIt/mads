package sessionwt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// ErrNoFolder is returned by TargetFor for a session with no git folder to
// commit in: no directory was asked for, or it isn't a git repo.
var ErrNoFolder = errors.New("sessionwt: the session has no git folder")

// Target is where a session's Commit and Land act, read off its record.
type Target struct {
	SessionID string
	// Name is the session's name, for the Land merge commit's subject.
	Name string
	// Folder is the top of the working tree the session works in: its
	// worktree when isolated, else the checkout its Dir is in. Paths in the
	// Commit dialog are relative to it.
	Folder string
	// Primary is the user's own checkout — where Land merges, and the repo
	// an isolated session's worktree is registered against. Equal to
	// Folder for a session working in place.
	Primary string
	// Branch and Base are set only for an isolated session.
	Branch string
	Base   string
	// Root is where the session's worktree lives ($MADS_HOME/
	// session-worktrees), for Ensure.
	Root string
}

// Isolated reports whether the session has its own worktree and branch,
// which is what makes Land apply.
func (t Target) Isolated() bool { return t.Branch != "" }

// TargetFor resolves a session record to its Target. An isolated session
// is one whose record names a worktree (D1 wrote it before its provider
// ever started); its folder may be missing on disk, which Ensure repairs.
// Anything else works in place in Dir, if Dir is in a git repo.
func TargetFor(ctx context.Context, sess contracts.Session) (Target, error) {
	t := Target{SessionID: sess.ID, Name: sess.Name}
	if sess.WorktreeDir != "" && sess.WorktreeBranch != "" {
		t.Folder, t.Branch, t.Base = sess.WorktreeDir, sess.WorktreeBranch, sess.WorktreeBase
		t.Root = filepath.Dir(sess.WorktreeDir)
		if !implicit(sess.Dir) {
			if top, err := Toplevel(ctx, sess.Dir); err == nil {
				t.Primary = top
			}
		}
		if t.Primary == "" && exists(sess.WorktreeDir) {
			if main, err := mainWorktree(ctx, sess.WorktreeDir); err == nil {
				t.Primary = main
			}
		}
		if t.Primary == "" {
			return Target{}, ErrNoFolder
		}
		return t, nil
	}
	if implicit(sess.Dir) {
		return Target{}, ErrNoFolder
	}
	top, err := Toplevel(ctx, sess.Dir)
	if err != nil {
		return Target{}, ErrNoFolder
	}
	t.Folder, t.Primary = top, top
	return t, nil
}

func implicit(dir string) bool { return dir == "" || dir == "." || dir == "./" }

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// RepoOf is the repository a commit made in dir is recorded against: the
// parent of its shared .git directory, the same value the post-commit hook
// reports (pkg/githook), so commit records from either source agree.
func RepoOf(ctx context.Context, dir string) (string, error) {
	common, err := commonDir(ctx, dir)
	if err != nil {
		return "", err
	}
	return filepath.Dir(common), nil
}

// SameRepo reports whether directories a and b are in the same git
// repository — the primary checkout or any of its worktrees.
func SameRepo(ctx context.Context, a, b string) bool {
	ca, err := commonDir(ctx, a)
	if err != nil {
		return false
	}
	cb, err := commonDir(ctx, b)
	return err == nil && samePath(ca, cb)
}

// Gates are what only the caller knows about a session at the moment it
// asks: whether its agent is mid-turn (the live runner) and whether a
// pipeline run is active for it (the Router's supervisor).
type Gates struct {
	Busy     bool
	Pipeline bool
}

// Status reads everything the header control, the Commit dialog and the
// delete confirmation show. Read-only and lock-free, like Count; a lost
// folder reads as having no uncommitted files (Commit and Land ensure it
// back).
func Status(ctx context.Context, t Target, rows []contracts.FileChange, g Gates) (contracts.WorktreeStatus, error) {
	st := contracts.WorktreeStatus{
		Isolated: t.Isolated(), Folder: t.Folder, Branch: t.Branch, Base: t.Base,
		Files: []contracts.WorktreeFile{},
	}
	present := exists(t.Folder)
	mine := SessionFiles(t.Folder, rows)
	if present {
		files, err := changes(ctx, t.Folder, mine)
		if err != nil {
			return contracts.WorktreeStatus{}, err
		}
		st.Files = files
		st.UncommittedCount = len(files)
		st.LastCommit = tip(ctx, t)
	}
	if t.Isolated() {
		n, err := CountUnlanded(ctx, t.Primary, t.Base, t.Branch)
		if err != nil {
			return contracts.WorktreeStatus{}, err
		}
		st.UnlandedCount = n
	}
	refusal, err := LandGate(ctx, t, g, st.UnlandedCount, present, mine)
	if err != nil {
		return contracts.WorktreeStatus{}, err
	}
	st.LandBlocked = refusal
	return st, nil
}

// changes reports which of files (relative to the top of dir's working
// tree) git sees as changed, and how: the Commit dialog's rows. Same
// status call as CountUncommitted, which counts these.
func changes(ctx context.Context, dir string, files []string) ([]contracts.WorktreeFile, error) {
	out := []contracts.WorktreeFile{}
	if len(files) == 0 {
		return out, nil
	}
	seen := map[string]bool{}
	for start := 0; start < len(files); start += countChunk {
		chunk := files[start:min(start+countChunk, len(files))]
		args := append([]string{
			"--no-optional-locks", "--literal-pathspecs",
			"status", "--porcelain", "-z", "--untracked-files=all", "--",
		}, chunk...)
		raw, err := Git(ctx, dir, args...)
		if err != nil {
			return nil, err
		}
		for _, e := range parseStatusZ(raw) {
			if !seen[e.path] {
				seen[e.path] = true
				out = append(out, contracts.WorktreeFile{Path: e.path, Kind: e.kind()})
			}
		}
	}
	return out, nil
}

type statusEntry struct {
	x, y byte
	path string
}

// kind maps a porcelain XY pair onto the ledger's three-way vocabulary.
func (e statusEntry) kind() string {
	switch {
	case e.x == '?' || e.x == 'A':
		return "added"
	case e.x == 'D' || e.y == 'D':
		return "deleted"
	default:
		return "modified"
	}
}

// parseStatusZ parses `git status --porcelain -z`. A rename or copy entry
// carries its source path as an extra NUL-terminated field, which is
// skipped: the destination is the file that changed.
func parseStatusZ(out string) []statusEntry {
	var entries []statusEntry
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		entries = append(entries, statusEntry{x: f[0], y: f[1], path: f[3:]})
		if f[0] == 'R' || f[0] == 'C' {
			i++
		}
	}
	return entries
}

// tip describes the commit checked out in the session's folder, and
// whether Undo would take it back.
func tip(ctx context.Context, t Target) *contracts.WorktreeCommit {
	out, err := Git(ctx, t.Folder, "log", "-1", "--format=%H%x00%P%x00%s%x00%B")
	if err != nil {
		return nil
	}
	parts := strings.SplitN(out, "\x00", 4)
	if len(parts) < 4 {
		return nil
	}
	c := &contracts.WorktreeCommit{Hash: parts[0], Subject: parts[2]}
	c.Undoable = undoRefusal(ctx, t, parts[0], parts[1], parts[3]) == ""
	return c
}

// LandGate is D6's "when Land is allowed", in the order the user would
// want to hear about it. nil means Land may run. unlanded is the session's
// unlanded count; folderPresent false skips the checks that read the
// folder (Land ensures it back before gating). sessionFiles are the
// session's own files (SessionFiles): a dirty refusal names the others as
// Outside, since Commit can't take those. Read-only, so Status can show the
// reason before anyone clicks.
func LandGate(ctx context.Context, t Target, g Gates, unlanded int, folderPresent bool, sessionFiles []string) (*contracts.LandRefusal, error) {
	refuse := func(reason, detail string, files ...string) (*contracts.LandRefusal, error) {
		return &contracts.LandRefusal{Reason: reason, Detail: detail, Files: files}, nil
	}
	switch {
	case !t.Isolated():
		return refuse(contracts.LandNotIsolated, "This session works directly in your checkout, so there is nothing to land. Commit is all it needs.")
	case g.Busy:
		return refuse(contracts.LandBusy, "The agent is working. Land when it finishes its turn.")
	case g.Pipeline:
		return refuse(contracts.LandPipeline, "A pipeline run is active for this session. Land when it finishes.")
	}
	if folderPresent {
		if op := midOperation(ctx, t.Folder); op != "" {
			return refuse(contracts.LandMidMerge, "The session's folder is in the middle of a "+op+". Ask the agent to finish or abort it first.")
		}
		if head, err := CurrentBranch(ctx, t.Folder); err != nil || head != t.Branch {
			on := head
			if on == "" {
				on = "a detached HEAD"
			}
			return refuse(contracts.LandOffBranch, "The session's folder has "+on+" checked out, not its branch "+t.Branch+". Ask the agent to switch back first.")
		}
		out, err := Git(ctx, t.Folder, "--no-optional-locks", "status", "--porcelain", "-z", "--untracked-files=all")
		if err != nil {
			return nil, err
		}
		if entries := parseStatusZ(out); len(entries) > 0 {
			files := make([]string, len(entries))
			for i, e := range entries {
				files[i] = e.path
			}
			r, _ := refuse(contracts.LandDirty, "The session's folder has uncommitted changes. Commit them (or ask the agent to tidy up) before landing.", files...)
			r.Outside = outside(entries, sessionFiles)
			return r, nil
		}
	}
	if unlanded == 0 {
		return refuse(contracts.LandNothingToLand, "Every commit on this session's branch is already in "+t.Base+".")
	}
	branch, err := CurrentBranch(ctx, t.Primary)
	switch {
	case errors.Is(err, ErrDetachedHEAD):
		return refuse(contracts.LandBaseNotCheckedOut, "Your checkout at "+t.Primary+" is on a detached HEAD. Check out "+t.Base+" there to land.")
	case err != nil:
		return nil, err
	case branch != t.Base:
		return refuse(contracts.LandBaseNotCheckedOut, "Your checkout at "+t.Primary+" is on "+branch+", not "+t.Base+". Check out "+t.Base+" there to land.")
	}
	if op := midOperation(ctx, t.Primary); op != "" {
		return refuse(contracts.LandPrimaryMidMerge, "Your checkout at "+t.Primary+" is in the middle of a "+op+". Finish or abort it, then land.")
	}
	return nil, nil
}

// MidOperation names the merge, rebase, cherry-pick or revert dir is in
// the middle of, or "" when it is in none.
func MidOperation(ctx context.Context, dir string) string { return midOperation(ctx, dir) }

// midOperation is MidOperation.
func midOperation(ctx context.Context, dir string) string {
	for _, op := range []struct{ ref, name string }{
		{"MERGE_HEAD", "merge"}, {"REBASE_HEAD", "rebase"},
		{"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"},
	} {
		if _, err := Git(ctx, dir, "rev-parse", "-q", "--verify", op.ref); err == nil {
			return op.name
		}
	}
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		p, err := Git(ctx, dir, "rev-parse", "--git-path", d)
		if err != nil {
			continue
		}
		p = strings.TrimSpace(p)
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if exists(p) {
			return "rebase"
		}
	}
	return ""
}
