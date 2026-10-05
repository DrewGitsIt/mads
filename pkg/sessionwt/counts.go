package sessionwt

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Counts is the header state of D4: how many of the session's files git
// still reports as changed, and how many commits the session branch has that
// its base doesn't.
type Counts struct {
	Uncommitted int `json:"uncommitted_count"`
	Unlanded    int `json:"unlanded_count"`
}

// CountTarget says where to count. For an isolated session Dir is its
// worktree and Branch/Base are set; for one working in place Dir is the
// folder it runs in and Branch is empty, so Unlanded is always 0.
type CountTarget struct {
	Dir string
	// Files are the session's files (SessionFiles), relative to the top of
	// Dir's working tree.
	Files  []string
	Branch string
	Base   string
}

// Count computes both counts. It only reads, takes no lock, and never
// refreshes the index (--no-optional-locks), so it can't contend with the
// agent's own git commands. Still a `git status`: callers run it at the
// listed moments (start, turn end, after Commit/Land/merge), never per render.
func Count(ctx context.Context, t CountTarget) (Counts, error) {
	var c Counts
	var err error
	if c.Uncommitted, err = CountUncommitted(ctx, t.Dir, t.Files); err != nil {
		return Counts{}, err
	}
	if c.Unlanded, err = CountUnlanded(ctx, t.Dir, t.Base, t.Branch); err != nil {
		return Counts{}, err
	}
	return c, nil
}

// countChunk bounds one git invocation's pathspec list well under any argv
// limit; a session's file list is usually far shorter.
const countChunk = 200

// CountUncommitted counts the files, of those given (relative to the top of
// dir's working tree), that git reports as changed: modified or deleted in
// the tree, staged, or new and untracked. A file the agent touched and then
// put back doesn't count — that is why this asks git rather than the ledger
// (D4).
//
// D4 sketches `--untracked-files=no`, but that hides exactly the files the
// agent created, which are uncommitted session work too. Limiting status to
// the session's own paths keeps the untracked scan to those paths, so asking
// for them costs nothing extra; ignored files stay out, since Commit couldn't
// stage them anyway.
func CountUncommitted(ctx context.Context, dir string, files []string) (int, error) {
	if len(files) == 0 {
		return 0, nil
	}
	top, err := Toplevel(ctx, dir)
	if err != nil {
		return 0, err
	}
	changed, err := changes(ctx, top, files)
	if err != nil {
		return 0, err
	}
	return len(changed), nil
}

// CountUnlanded counts commits on branch that base doesn't have: `git
// rev-list --count <base>..<branch>`. No branch (a session working in place)
// is 0. An empty or deleted base counts against the primary's HEAD, the same
// fallback Ensure uses for a new branch's start point.
func CountUnlanded(ctx context.Context, dir, base, branch string) (int, error) {
	if branch == "" {
		return 0, nil
	}
	from := "HEAD"
	if base != "" && refExists(ctx, dir, "refs/heads/"+base) {
		from = "refs/heads/" + base
	}
	if from == "HEAD" {
		// dir is the session's worktree, whose HEAD is the session branch
		// itself; the primary's HEAD lives in the main worktree.
		if main, err := mainWorktree(ctx, dir); err == nil {
			dir = main
		}
	}
	out, err := Git(ctx, dir, "rev-list", "--count", from+"..refs/heads/"+branch)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// mainWorktree is the repo's main working tree — the first entry git lists.
func mainWorktree(ctx context.Context, dir string) (string, error) {
	out, err := Git(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(out, "\n")
	return strings.TrimPrefix(first, "worktree "), nil
}

// SessionFiles turns the session's change ledger into paths relative to
// root (its worktree, or the folder it works in): the set Count looks at,
// and later the Commit dialog's candidates. Rows a pipeline task produced
// (TaskID set) are left out — they live in task worktrees (D5) — as are
// changes merely observed in a shared checkout, which the §5 attribution
// rule says aren't the session's, and any path that resolves outside root.
// Sorted and deduplicated.
func SessionFiles(root string, rows []contracts.FileChange) []string {
	seen := map[string]bool{}
	var out []string
	for _, row := range rows {
		if row.TaskID != "" || row.Path == "" || (row.Origin == contracts.OriginObserved && !row.Attributed) {
			continue
		}
		abs := row.Path
		if !filepath.IsAbs(abs) {
			base := row.Repo
			if base == "" {
				base = root
			}
			abs = filepath.Join(base, abs)
		}
		rel, ok := relInside(root, abs)
		if !ok || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	slices.Sort(out)
	return out
}

// relInside is p relative to root, if p is inside it — trying the paths as
// given first, then with symlinks resolved, since the ledger and git can
// spell the same directory differently.
func relInside(root, p string) (string, bool) {
	try := func(r, q string) (string, bool) {
		rel, err := filepath.Rel(r, q)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		return filepath.ToSlash(rel), true
	}
	if rel, ok := try(filepath.Clean(root), filepath.Clean(p)); ok {
		return rel, true
	}
	return try(resolve(root), resolve(p))
}
