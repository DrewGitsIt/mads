package changes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// The session's recorded commits, as the change set lists them, in a few
// git calls per repo instead of three per commit: one `cat-file
// --batch-check` for which hashes the repo has, one octopus merge-base
// and one bounded rev-list for which of those its HEAD reaches, and one
// `git show` for every anchored commit's files. Each answer is the one the
// per-commit calls (resolveRef, isAncestorOfHEAD, commitFiles) give;
// commitsEach is that version, and what any batch failure falls back to.

// showBatch caps the hashes one `git show` is given on its command line;
// ancestryBatch, one merge-base and rev-list.
const (
	showBatch     = 256
	ancestryBatch = 256
)

// revListCap bounds the rev-list walk reachableFromHEAD runs: a history
// longer than this since the hashes forked is asked one hash at a time,
// rather than walked to the git timeout. A var for tests.
var revListCap = 20000

// fullHashRe is a full object name, SHA-1 or SHA-256.
var fullHashRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// batchFallbacks counts commits() falling back to commitsEach, and
// perHashAncestry reachableFromHEAD asking hash by hash: for tests, so a
// silent fallback can't pass for the batched answer.
var batchFallbacks, perHashAncestry atomic.Int64

// commitMark starts each commit's record in commitFilesBatch's output
// (\x01 can't start a path git prints under -z).
const commitMark = "\x01mads-commit\x01"

// commits lists the session's recorded commits, newest first, with the
// files each touched as absolute paths, and which commits touched each
// path. Worktrees share their object store with the primary checkout, so
// every repo can resolve every hash; paths are anchored in the first repo
// whose checked-out branch contains the commit — the session folder's
// first, so a worktree session's own commits key the worktree's files,
// while a commit that landed in the primary checkout keys the primary's.
// No branch contains it (amended away, say): the first repo that has it.
func (b *builder) commits(ctx context.Context) ([]contracts.ChangeCommit, map[string][]string) {
	if len(b.in.Commits) == 0 {
		return []contracts.ChangeCommit{}, map[string][]string{}
	}
	out, byPath, err := b.commitsBatched(ctx)
	if err != nil {
		batchFallbacks.Add(1)
		return b.commitsEach(ctx)
	}
	return out, byPath
}

// commitRepos is the repos a recorded commit is looked for in, in order:
// the session folder's, every other root the set saw, and the repo the
// commit was recorded in.
func (b *builder) commitRepos() []string {
	var repos []string
	if b.folder != "" {
		if r := repoRootOf(b.folder, b.rootCache); r != "" {
			repos = append(repos, r)
		}
	}
	for _, r := range sortedKeys(b.repoRoots) {
		if len(repos) == 0 || repos[0] != r {
			repos = append(repos, r)
		}
	}
	return repos
}

func (b *builder) commitsBatched(ctx context.Context) ([]contracts.ChangeCommit, map[string][]string, error) {
	recorded := b.in.Commits
	repos := b.commitRepos()
	type pending struct {
		c      contracts.Commit
		try    map[string]bool // the repos it is looked for in
		anchor string
		full   string // its full hash, once a repo resolved it
		done   bool   // anchored where HEAD reaches it
	}
	var list []*pending
	order := append([]string{}, repos...)
	seen := map[string]bool{}
	for _, r := range repos {
		seen[r] = true
	}
	for i := len(recorded) - 1; i >= 0; i-- {
		c := recorded[i]
		if !commitHashRe.MatchString(c.Hash) {
			continue
		}
		p := &pending{c: c, try: map[string]bool{}}
		for _, r := range repos {
			p.try[r] = true
		}
		if c.Repo != "" {
			r := shadow.RealPath(c.Repo)
			p.try[r] = true
			if !seen[r] {
				seen[r] = true
				order = append(order, r)
			}
		}
		list = append(list, p)
	}
	// Repo by repo, in the order each commit tries them (its own repo is
	// always last): what earlier repos settled isn't asked again.
	for _, repo := range order {
		var hashes []string
		for _, p := range list {
			if !p.done && p.try[repo] {
				hashes = append(hashes, p.c.Hash)
			}
		}
		if len(hashes) == 0 {
			continue
		}
		full, err := resolveCommits(ctx, repo, hashes)
		if err != nil {
			if repoGone(repo) {
				continue // it has none of them, as resolveRef says
			}
			return nil, nil, err // a timeout, an odd answer: commitsEach decides
		}
		var have []string
		for _, h := range hashes {
			if f, ok := full[h]; ok {
				have = append(have, f)
			}
		}
		reach := reachableFromHEAD(ctx, repo, have)
		for _, p := range list {
			if p.done || !p.try[repo] {
				continue
			}
			f, ok := full[p.c.Hash]
			if !ok {
				continue
			}
			if p.anchor == "" {
				p.anchor, p.full = repo, f
			}
			if reach[f] {
				p.anchor, p.full, p.done = repo, f, true
			}
		}
	}
	files := map[string]map[string][]string{} // anchor → full hash → rels
	byAnchor := map[string][]string{}
	for _, p := range list {
		if p.anchor != "" {
			byAnchor[p.anchor] = append(byAnchor[p.anchor], p.full)
		}
	}
	for repo, hashes := range byAnchor {
		rels, err := commitFilesBatch(ctx, repo, hashes)
		if err != nil {
			return nil, nil, err
		}
		files[repo] = rels
	}
	out := []contracts.ChangeCommit{}
	byPath := map[string][]string{}
	for _, p := range list {
		cc := contracts.ChangeCommit{Hash: p.c.Hash, Message: firstLine(p.c.Message), Timestamp: p.c.Timestamp, Paths: []string{}}
		if repo := p.anchor; repo != "" {
			for _, rel := range files[repo][p.full] {
				path := filepath.Join(repo, filepath.FromSlash(rel))
				cc.Paths = append(cc.Paths, path)
				byPath[path] = append(byPath[path], p.c.Hash)
			}
		}
		out = append(out, cc)
	}
	return out, byPath, nil
}

// commitsEach is commits with three git calls per commit and repo
// (resolveRef, isAncestorOfHEAD, commitFiles): the batched version's
// fallback, and its reference in tests.
func (b *builder) commitsEach(ctx context.Context) ([]contracts.ChangeCommit, map[string][]string) {
	out := []contracts.ChangeCommit{}
	byPath := map[string][]string{}
	recorded := b.in.Commits
	if len(recorded) == 0 {
		return out, byPath
	}
	repos := b.commitRepos()
	for i := len(recorded) - 1; i >= 0; i-- {
		c := recorded[i]
		if !commitHashRe.MatchString(c.Hash) {
			continue
		}
		cc := contracts.ChangeCommit{Hash: c.Hash, Message: firstLine(c.Message), Timestamp: c.Timestamp, Paths: []string{}}
		tryRepos := repos
		if c.Repo != "" {
			tryRepos = append(append([]string{}, repos...), shadow.RealPath(c.Repo))
		}
		anchor := ""
		for _, repo := range tryRepos {
			if _, ok := resolveRef(ctx, repo, c.Hash); !ok {
				continue
			}
			if anchor == "" {
				anchor = repo
			}
			if isAncestorOfHEAD(ctx, repo, c.Hash) {
				anchor = repo
				break
			}
		}
		if repo := anchor; repo != "" {
			rels, err := commitFiles(ctx, repo, c.Hash)
			if err != nil {
				break
			}
			for _, rel := range rels {
				p := filepath.Join(repo, filepath.FromSlash(rel))
				cc.Paths = append(cc.Paths, p)
				byPath[p] = append(byPath[p], c.Hash)
			}
		}
		out = append(out, cc)
	}
	return out, byPath
}

// repoGone: no git checkout at repo any more (deleted, or never one), so
// every git call there fails and resolveRef says it has no commit.
func repoGone(repo string) bool {
	_, err := os.Lstat(filepath.Join(repo, ".git"))
	return err != nil
}

// resolveCommits maps each of hashes that names a commit in repo to its
// full hash (resolveRef for many, in one `git cat-file --batch-check`).
func resolveCommits(ctx context.Context, repo string, hashes []string) (map[string]string, error) {
	var in strings.Builder
	for _, h := range hashes {
		in.WriteString(h + "^{commit}\n")
	}
	out, err := gitCmd{
		args:  []string{"--no-optional-locks", "-C", repo, "cat-file", "--batch-check=%(objectname) %(objecttype)"},
		stdin: strings.NewReader(in.String()),
	}.run(ctx)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(hashes) {
		return nil, errors.New("cat-file --batch-check: one line per hash expected")
	}
	res := map[string]string{}
	for i, line := range lines {
		if f, ok := strings.CutSuffix(line, " commit"); ok && fullHashRe.MatchString(f) {
			res[hashes[i]] = f
		}
	}
	return res, nil
}

// reachableFromHEAD is which of fulls (full commit hashes repo has) its
// HEAD reaches (isAncestorOfHEAD for many), ancestryBatch at a time. The
// octopus merge-base of HEAD and a batch is an ancestor of each of them,
// so one of them is reachable from a base only by being that base: the
// reachable ones are the bases among them and the ones `rev-list HEAD
// --not <bases>` lists — a walk no longer than the history since the
// oldest of them forked, and capped at revListCap. No common base
// (unrelated histories, an unborn HEAD), a failed or capped walk: one
// merge-base per hash.
func reachableFromHEAD(ctx context.Context, repo string, fulls []string) map[string]bool {
	res := map[string]bool{}
	seen := map[string]bool{}
	var want []string
	for _, f := range fulls {
		if !seen[f] {
			seen[f] = true
			want = append(want, f)
		}
	}
	for start := 0; start < len(want); start += ancestryBatch {
		for f := range reachableBatch(ctx, repo, want[start:min(start+ancestryBatch, len(want))]) {
			res[f] = true
		}
	}
	return res
}

func reachableBatch(ctx context.Context, repo string, want []string) map[string]bool {
	eachOne := func() map[string]bool {
		perHashAncestry.Add(1)
		res := map[string]bool{}
		for _, f := range want {
			if isAncestorOfHEAD(ctx, repo, f) {
				res[f] = true
			}
		}
		return res
	}
	uniq := map[string]bool{}
	for _, f := range want {
		uniq[f] = true
	}
	out, err := repoGit(ctx, repo, append([]string{"merge-base", "--octopus", "--all", "HEAD"}, want...)...)
	bases := strings.Fields(string(out))
	if err != nil || len(bases) == 0 {
		return eachOne()
	}
	res := map[string]bool{}
	args := []string{"rev-list", "--max-count=" + strconv.Itoa(revListCap), "HEAD", "--not"}
	for _, base := range bases {
		args = append(args, base)
		if uniq[base] {
			res[base] = true
		}
	}
	if out, err = repoGit(ctx, repo, args...); err != nil {
		return eachOne()
	}
	listed := strings.Fields(string(out))
	if len(listed) >= revListCap {
		return eachOne() // maybe cut short: what it didn't list may be reachable
	}
	for _, h := range listed {
		if uniq[h] {
			res[h] = true
		}
	}
	return res
}

// commitFilesBatch lists each of fulls' files (repo-relative, git's
// order), by full hash: commitFiles for many, in one `git show` per
// showBatch commits. Every hash must be a full one naming a commit in repo.
func commitFilesBatch(ctx context.Context, repo string, fulls []string) (map[string][]string, error) {
	var want []string
	res := map[string][]string{}
	for _, f := range fulls {
		if _, dup := res[f]; !dup {
			res[f] = nil
			want = append(want, f)
		}
	}
	printed := map[string]bool{}
	for start := 0; start < len(want); start += showBatch {
		chunk := want[start:min(start+showBatch, len(want))]
		args := append([]string{"show", "--no-color", "--name-only", "-z", "--no-renames", "--format=" + commitMark + "%H"}, chunk...)
		out, err := repoGit(ctx, repo, args...)
		if err != nil {
			return nil, err
		}
		cur := ""
		for _, tok := range strings.Split(string(out), "\x00") {
			if h, ok := strings.CutPrefix(strings.TrimLeft(tok, "\n"), commitMark); ok {
				cur = h
				printed[cur] = true
				continue
			}
			if rel := strings.TrimSpace(tok); rel != "" && cur != "" {
				res[cur] = append(res[cur], rel)
			}
		}
	}
	for _, f := range want {
		if !printed[f] {
			return nil, errors.New("git show printed nothing for " + f)
		}
	}
	return res, nil
}
