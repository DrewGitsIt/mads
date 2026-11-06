package sessionwt

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// A session's repo set (docs/COMMIT-LAND-PUSH-DESIGN.md D1): its folder's
// own repo, when it has one, plus every repo its own tools changed files
// in. Commit, its draft, Undo and the header counts act per repo; Land
// stays with the folder's repo, the only one a session isolates.

// ErrUnknownRepo is a request naming a repo outside the session's set.
var ErrUnknownRepo = errors.New("sessionwt: not one of the session's repos")

// RepoTargets is the session's repo set: primary (its folder's Target, nil
// when the folder isn't in a repo) first, then each other repo the
// session's own ledger rows fall in, sorted by root. The others work in
// place: Folder and Primary are both the repo's top. Repos nested inside
// the primary's working tree (a subagent's worktree, a vendored repo) are
// left out: their work reaches the folder through its own Land, or isn't
// the session's to commit.
func RepoTargets(ctx context.Context, sess contracts.Session, primary *Target, rows []contracts.FileChange) []Target {
	var out []Target
	if primary != nil {
		out = append(out, *primary)
	}
	tops := map[string]bool{}
	cache := map[string]string{}
	for _, row := range rows {
		if row.TaskID != "" || row.Path == "" || (row.Origin == contracts.OriginObserved && !row.Attributed) {
			continue
		}
		abs := row.Path
		if !filepath.IsAbs(abs) {
			base := row.Repo
			if base == "" && primary != nil {
				base = primary.Folder
			}
			if base == "" {
				continue
			}
			abs = filepath.Join(base, abs)
		}
		top := toplevelOf(ctx, filepath.Dir(abs), cache)
		if top == "" || tops[top] {
			continue
		}
		tops[top] = true
	}
	var others []string
	for top := range tops {
		if primary != nil && (samePath(top, primary.Folder) || nestedIn(primary.Folder, top)) {
			continue
		}
		others = append(others, top)
	}
	sort.Strings(others)
	for _, top := range others {
		out = append(out, Target{SessionID: sess.ID, Name: sess.Name, Folder: top, Primary: top})
	}
	return out
}

// toplevelOf is the top of the working tree holding dir, or of its
// nearest existing ancestor (a deleted file's directory may be gone too);
// "" outside every repo. Cached per directory asked.
func toplevelOf(ctx context.Context, dir string, cache map[string]string) string {
	if top, ok := cache[dir]; ok {
		return top
	}
	top := ""
	for cur := dir; ; {
		if info, err := os.Stat(cur); err == nil && info.IsDir() {
			if t, err := Toplevel(ctx, cur); err == nil {
				top = t
			}
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	cache[dir] = top
	return top
}

// nestedIn reports whether p is strictly inside root.
func nestedIn(root, p string) bool {
	_, ok := relInside(root, p)
	return ok
}

// targets loads the session's primary target (nil when its folder isn't
// a repo) and its whole repo set.
func (o Ops) targets(ctx context.Context, sessionID string) (contracts.Session, *Target, []Target, []contracts.FileChange, error) {
	sess, t, rows, err := o.load(ctx, sessionID)
	var primary *Target
	switch {
	case err == nil:
		primary = &t
	case errors.Is(err, ErrNoFolder):
		if rows, err = o.Store.FileChanges(ctx, sessionID); err != nil {
			return sess, nil, nil, nil, err
		}
	default:
		return sess, nil, nil, nil, err
	}
	return sess, primary, RepoTargets(ctx, sess, primary, rows), rows, nil
}

// pick resolves repo to one of all: "" is the primary, or the only repo
// when the folder has none; otherwise the repo whose top is repo.
// ErrNoFolder when "" names nothing, ErrUnknownRepo for a repo outside
// the set.
func pick(primary *Target, all []Target, repo string) (Target, error) {
	if repo == "" {
		switch {
		case primary != nil:
			return *primary, nil
		case len(all) == 1:
			return all[0], nil
		}
		return Target{}, ErrNoFolder
	}
	for _, t := range all {
		if samePath(t.Folder, repo) {
			return t, nil
		}
	}
	return Target{}, ErrUnknownRepo
}

// loadRepo is load for one repo of the session's set (pick).
func (o Ops) loadRepo(ctx context.Context, sessionID, repo string) (Target, []contracts.FileChange, error) {
	_, primary, all, rows, err := o.targets(ctx, sessionID)
	if err != nil {
		return Target{}, nil, err
	}
	t, err := pick(primary, all, repo)
	return t, rows, err
}

// CountRepos computes the header counts (D4) in every repo of all, and
// their sums. A repo that can't be counted (deleted since) is logged and
// left out rather than failing the rest. repos lists the repos with
// something to count, primary first.
func CountRepos(ctx context.Context, primary *Target, all []Target, rows []contracts.FileChange) (sum Counts, repos []contracts.SessionRepo) {
	for i, t := range all {
		c, err := countTarget(ctx, t, rows)
		if err != nil {
			log.Printf("sessionwt: count %s in %s: %v", t.SessionID, t.Folder, err)
			continue
		}
		sum.Uncommitted += c.Uncommitted
		sum.Unlanded += c.Unlanded
		sum.Outside += c.Outside
		sum.Unpushed += c.Unpushed
		if c == (Counts{}) {
			continue
		}
		repos = append(repos, contracts.SessionRepo{
			Root: t.Folder, Branch: t.Branch, Isolated: t.Isolated(), Primary: primary != nil && i == 0,
			UncommittedCount: c.Uncommitted, UnlandedCount: c.Unlanded, OutsideCount: c.Outside,
			UnpushedCount: c.Unpushed,
		})
	}
	return sum, repos
}

// countTarget is one repo's counts: its folder (if still there) for the
// uncommitted and, isolated, the outside count; the branch for unlanded.
func countTarget(ctx context.Context, t Target, rows []contracts.FileChange) (Counts, error) {
	var c Counts
	if exists(t.Folder) {
		mine := SessionFiles(t.Folder, rows)
		var err error
		if c.Uncommitted, err = CountUncommitted(ctx, t.Folder, mine); err != nil {
			return Counts{}, err
		}
		if t.Isolated() {
			if c.Outside, err = CountOutside(ctx, t.Folder, mine); err != nil {
				return Counts{}, err
			}
		}
	}
	if t.Isolated() {
		var err error
		if c.Unlanded, err = CountUnlanded(ctx, t.Primary, t.Base, t.Branch); err != nil {
			return Counts{}, err
		}
	}
	if exists(t.Folder) || t.Isolated() {
		var err error
		if c.Unpushed, err = unpushedOf(ctx, t); err != nil {
			return Counts{}, err
		}
	}
	return c, nil
}

// CountAll computes the session's header counts across its repo set, read
// off its record and change list. ErrNoFolder when the set is empty: there
// is nothing to count, and nothing to write.
func (o Ops) CountAll(ctx context.Context, sessionID string) (Counts, []contracts.SessionRepo, error) {
	_, primary, all, rows, err := o.targets(ctx, sessionID)
	if err != nil {
		return Counts{}, nil, err
	}
	if len(all) == 0 {
		return Counts{}, nil, ErrNoFolder
	}
	sum, repos := CountRepos(ctx, primary, all, rows)
	return sum, repos, nil
}

// CountsPatch is the scoped record write for sum and repos.
func CountsPatch(sum Counts, repos []contracts.SessionRepo) contracts.SessionWorktreePatch {
	if repos == nil {
		repos = []contracts.SessionRepo{}
	}
	return contracts.SessionWorktreePatch{
		UncommittedCount: &sum.Uncommitted, UnlandedCount: &sum.Unlanded, OutsideCount: &sum.Outside,
		UnpushedCount: &sum.Unpushed, Repos: &repos,
	}
}

// Repos is the session's repo set as Commit sees it: its primary target
// (nil when its folder isn't a repo), every repo, and its change list.
func (o Ops) Repos(ctx context.Context, sessionID string) (primary *Target, all []Target, rows []contracts.FileChange, err error) {
	_, primary, all, rows, err = o.targets(ctx, sessionID)
	return primary, all, rows, err
}

// ChangedFiles is every file git reports changed in dir's working tree,
// untracked included, ignored left out: what a Commit there could take.
// Paths are relative to the top of the tree, in git's order.
func ChangedFiles(ctx context.Context, dir string) ([]contracts.WorktreeFile, error) {
	out, err := Git(ctx, dir, "--no-optional-locks", "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	files := []contracts.WorktreeFile{}
	for _, e := range parseStatusZ(out) {
		files = append(files, contracts.WorktreeFile{Path: e.path, Kind: e.kind()})
	}
	return files, nil
}

// TopOf is the top of the working tree holding path (or its nearest
// existing ancestor); "" outside every repo.
func TopOf(ctx context.Context, path string) string {
	return toplevelOf(ctx, filepath.Dir(path), map[string]string{})
}
