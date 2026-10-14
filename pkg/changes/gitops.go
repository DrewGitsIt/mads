package changes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/drewkidwell/mads/services/session/shadow"
)

const (
	// gitTimeout bounds every git call except the temp-index refresh.
	gitTimeout = 15 * time.Second
	// addTimeout bounds `add -A` into the temp index. The copied index is
	// warm (the shadow store's last turn commit), so this is a stat walk
	// plus hashing whatever changed since — normally well under a second.
	addTimeout = 60 * time.Second
)

// gitEnv is the environment every git call runs with: the caller's, minus
// anything that would redirect git away from the dirs named on the command
// line (a Router started from inside a git hook would otherwise inherit
// GIT_DIR and read the wrong repo).
func gitEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "GIT_DIR="),
			strings.HasPrefix(kv, "GIT_WORK_TREE="),
			strings.HasPrefix(kv, "GIT_INDEX_FILE="),
			strings.HasPrefix(kv, "GIT_OBJECT_DIRECTORY="),
			strings.HasPrefix(kv, "GIT_COMMON_DIR="):
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// gitCmd is one git invocation.
type gitCmd struct {
	args    []string
	env     []string // extra KEY=VALUE pairs
	stdin   io.Reader
	timeout time.Duration
	// okExit lists non-zero exit codes that are answers, not failures
	// (diff --no-index exits 1 when the files differ; check-ignore exits 1
	// when nothing is ignored).
	okExit []int
}

// run executes c and returns stdout.
func (c gitCmd) run(ctx context.Context) ([]byte, error) {
	timeout := c.timeout
	if timeout == 0 {
		timeout = gitTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", c.args...) //nolint:gosec // args are internally constructed
	cmd.Env = gitEnv(c.env...)
	cmd.Stdin = c.stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			for _, code := range c.okExit {
				if ee.ExitCode() == code {
					return stdout.Bytes(), nil
				}
			}
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", firstArgs(c.args), ctx.Err())
		}
		return nil, fmt.Errorf("git %s: %w: %s", firstArgs(c.args), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// firstArgs names a git call in an error without dumping a long pathspec.
func firstArgs(args []string) string {
	if len(args) > 4 {
		args = args[:4]
	}
	return strings.Join(args, " ")
}

// repoGit runs a read-only git command in the user's repo. Every call
// carries --no-optional-locks: a `git status` or `git diff` would otherwise
// opportunistically rewrite the user's index, contending with the agent's
// own git commands for index.lock.
func repoGit(ctx context.Context, repo string, args ...string) ([]byte, error) {
	return gitCmd{args: append([]string{"--no-optional-locks", "-C", repo}, args...)}.run(ctx)
}

// numstatEntry is one file of a numstat/name-status diff, root-relative.
type numstatEntry struct {
	Rel     string
	Change  string // added | modified | deleted
	Added   int
	Deleted int
	Binary  bool
}

// parseNumstatZ parses `git diff --numstat -z --no-renames` output:
// "<added>\t<deleted>\t<path>\0" per file, "-\t-\t" for a binary file.
func parseNumstatZ(out []byte) map[string]numstatEntry {
	res := map[string]numstatEntry{}
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		e := numstatEntry{Rel: parts[2]}
		if parts[0] == "-" && parts[1] == "-" {
			e.Binary = true
		} else {
			e.Added, _ = strconv.Atoi(parts[0])
			e.Deleted, _ = strconv.Atoi(parts[1])
		}
		res[e.Rel] = e
	}
	return res
}

// parseNameStatusZ parses `git diff --name-status -z --no-renames` output:
// "<letter>\0<path>\0" pairs.
func parseNameStatusZ(out []byte) map[string]string {
	res := map[string]string{}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == "" {
			continue
		}
		res[fields[i+1]] = mapStatus(fields[i])
	}
	return res
}

// mapStatus collapses git's status letters onto added/modified/deleted.
func mapStatus(letter string) string {
	switch {
	case strings.HasPrefix(letter, "A"):
		return "added"
	case strings.HasPrefix(letter, "D"):
		return "deleted"
	default:
		return "modified"
	}
}

// mergeDiff joins numstat and name-status output into entries sorted by
// path.
func mergeDiff(numstat, nameStatus []byte) []numstatEntry {
	stats := parseNumstatZ(numstat)
	letters := parseNameStatusZ(nameStatus)
	out := make([]numstatEntry, 0, len(letters))
	for rel, change := range letters {
		e, ok := stats[rel]
		if !ok {
			e = numstatEntry{Rel: rel}
		}
		e.Change = change
		out = append(out, e)
	}
	return out
}

// shadowDiff lists what differs on disk in root from the shadow store's
// baseline commit, without touching the shadow store's own index, refs or
// commits: the store's index is copied to a temp file, `add -A` refreshes
// that copy against the work tree (blobs it hashes land in the shadow
// object store, which the next turn commit would write anyway), and the
// diff is taken --cached against the baseline. The shadow dir's
// info/exclude (the mads exclude list) and the root's own .gitignore files
// apply exactly as they do to the store's own commits.
func shadowDiff(ctx context.Context, gitDir, root string) ([]numstatEntry, error) {
	tmp, err := os.MkdirTemp("", "mads-changes-index-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	index := filepath.Join(tmp, "index")
	if err := copyFile(filepath.Join(gitDir, "index"), index); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	env := []string{"GIT_INDEX_FILE=" + index}
	base := []string{"--git-dir=" + gitDir, "--work-tree=" + root}
	if _, err := (gitCmd{args: append(base, "add", "-A"), env: env, timeout: addTimeout}).run(ctx); err != nil {
		return nil, err
	}
	numstat, err := gitCmd{args: append(base, "diff", "--cached", "--numstat", "-z", "--no-renames", shadow.BaselineTag), env: env}.run(ctx)
	if err != nil {
		return nil, err
	}
	names, err := gitCmd{args: append(base, "diff", "--cached", "--name-status", "-z", "--no-renames", shadow.BaselineTag), env: env}.run(ctx)
	if err != nil {
		return nil, err
	}
	return mergeDiff(numstat, names), nil
}

// copyFile copies the shadow index to dst, keeping its mtime. That matters:
// git trusts an index entry's cached stat data only when the entry is older
// than the index file itself ("racy git"); a copy stamped "now" would make
// every entry look safely older, so a same-size edit made within the
// timestamp granularity of the last turn commit would read as unchanged.
func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // src is the shadow store's own index
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.Create(dst) //nolint:gosec // dst is in our own temp dir
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err = errors.Join(err, out.Close()); err != nil {
		return err
	}
	return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}

// hasShadowBaseline reports whether gitDir holds a baseline commit.
func hasShadowBaseline(ctx context.Context, gitDir string) bool {
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil {
		return false
	}
	_, err := gitCmd{args: []string{"--git-dir=" + gitDir, "rev-parse", "--quiet", "--verify", shadow.BaselineTag + "^{commit}"}}.run(ctx)
	return err == nil
}

// inShadowBaseline reports, for each root-relative path, whether it exists
// in the shadow baseline tree — one `cat-file --batch-check` for all of
// them. A failure reports nothing present.
func inShadowBaseline(ctx context.Context, gitDir string, rels []string) map[string]bool {
	res := map[string]bool{}
	if len(rels) == 0 {
		return res
	}
	var in strings.Builder
	for _, rel := range rels {
		in.WriteString(shadow.BaselineTag + ":" + rel + "\n")
	}
	out, err := gitCmd{args: []string{"--git-dir=" + gitDir, "cat-file", "--batch-check"}, stdin: strings.NewReader(in.String())}.run(ctx)
	if err != nil {
		return res
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i, line := range lines {
		if i < len(rels) && !strings.HasSuffix(line, " missing") {
			res[rels[i]] = true
		}
	}
	return res
}

// mergeBaseDiff lists what differs between commit mb and the working tree
// in repo (uncommitted work included), plus untracked non-ignored files as
// added.
func mergeBaseDiff(ctx context.Context, repo, mb string) ([]numstatEntry, error) {
	entries, err := trackedDiff(ctx, repo, mb)
	if err != nil {
		return nil, err
	}
	untracked, err := repoGit(ctx, repo, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, rel := range strings.Split(string(untracked), "\x00") {
		if rel == "" {
			continue
		}
		e := numstatEntry{Rel: rel, Change: "added"}
		e.Added, e.Binary = countFileLines(filepath.Join(repo, rel))
		entries = append(entries, e)
	}
	return entries, nil
}

// trackedDiff lists what differs between rev and the working tree in repo
// for tracked files only (uncommitted work included).
func trackedDiff(ctx context.Context, repo, rev string) ([]numstatEntry, error) {
	numstat, err := repoGit(ctx, repo, "diff", "--numstat", "-z", "--no-renames", rev)
	if err != nil {
		return nil, err
	}
	names, err := repoGit(ctx, repo, "diff", "--name-status", "-z", "--no-renames", rev)
	if err != nil {
		return nil, err
	}
	return mergeDiff(numstat, names), nil
}

// countFileLines counts a file's lines the way numstat would for an added
// file, reporting binary (a NUL in the first 8000 bytes, git's heuristic).
func countFileLines(path string) (lines int, binary bool) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is from git's own listing or the session ledger
	if err != nil {
		return 0, false
	}
	return countLines(raw)
}

func countLines(raw []byte) (lines int, binary bool) {
	head := raw
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return 0, true
	}
	n := bytes.Count(raw, []byte{'\n'})
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		n++
	}
	return n, false
}

// noIndexNumstat counts the line difference between original (content)
// and the file at path (absent → deleted) with `git diff --no-index`.
// same is true when the two are identical.
func noIndexNumstat(ctx context.Context, original, path string) (e numstatEntry, same bool, err error) {
	tmp, err := os.CreateTemp("", "mads-changes-orig-")
	if err != nil {
		return e, false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, werr := tmp.WriteString(original)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		return e, false, err
	}
	target := path
	if _, err := os.Lstat(path); err != nil {
		target = os.DevNull
	}
	out, err := gitCmd{args: []string{"diff", "--no-index", "--numstat", "-z", "--", tmp.Name(), target}, okExit: []int{1}}.run(ctx)
	if err != nil {
		return e, false, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return e, true, nil
	}
	// --no-index with -z prints "<a>\t<b>\t\0<old>\0<new>\0" for two named
	// files; only the counts matter here.
	fields := strings.SplitN(string(out), "\t", 3)
	if len(fields) < 2 {
		return e, false, fmt.Errorf("unexpected numstat output %q", out)
	}
	if fields[0] == "-" && fields[1] == "-" {
		e.Binary = true
	} else {
		e.Added, _ = strconv.Atoi(fields[0])
		e.Deleted, _ = strconv.Atoi(fields[1])
	}
	return e, false, nil
}

// repoRootOf finds the git work tree containing dir by walking up from its
// nearest existing ancestor looking for a .git entry (a dir for a primary
// checkout, a file for a linked worktree or submodule) — the same answer
// `git rev-parse --show-toplevel` gives, without a process per directory.
// "" when dir is in no repo.
func repoRootOf(dir string, cache map[string]string) string {
	if root, ok := cache[dir]; ok {
		return root
	}
	root := ""
	for d := dir; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			root = d
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	cache[dir] = root
	return root
}

// resolveRef reports whether rev names a commit in repo, returning its hash.
func resolveRef(ctx context.Context, repo, rev string) (string, bool) {
	out, err := repoGit(ctx, repo, "rev-parse", "--quiet", "--verify", rev+"^{commit}")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// isAncestorOfHEAD reports whether hash is reachable from repo's HEAD.
func isAncestorOfHEAD(ctx context.Context, repo, hash string) bool {
	_, err := repoGit(ctx, repo, "merge-base", "--is-ancestor", hash, "HEAD")
	return err == nil
}

// mergeBase is `git merge-base HEAD <ref>` in repo.
func mergeBase(ctx context.Context, repo, ref string) (string, error) {
	out, err := repoGit(ctx, repo, "merge-base", "HEAD", ref)
	if err != nil {
		return "", err
	}
	mb := strings.TrimSpace(string(out))
	if mb == "" {
		return "", errors.New("no merge base")
	}
	return mb, nil
}

// defaultBranch resolves the repo's default branch: origin/HEAD's target,
// else a local main, else master. ref is what to merge-base against;
// label is the branch's short name for the switcher.
func defaultBranch(ctx context.Context, repo string) (ref, label string, ok bool) {
	if out, err := repoGit(ctx, repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if r := strings.TrimSpace(string(out)); r != "" {
			if _, ok := resolveRef(ctx, repo, r); ok {
				return r, strings.TrimPrefix(r, "origin/"), true
			}
		}
	}
	for _, b := range []string{"main", "master"} {
		if _, ok := resolveRef(ctx, repo, "refs/heads/"+b); ok {
			return b, b, true
		}
	}
	return "", "", false
}

// dirtyPaths returns the repo-relative paths `git status` reports as
// differing from HEAD (including untracked), and the untracked directories
// it collapsed (with their trailing slash).
func dirtyPaths(ctx context.Context, repo string) (files map[string]bool, untrackedDirs []string, err error) {
	out, err := repoGit(ctx, repo, "status", "--porcelain", "-z", "--no-renames", "--untracked-files=normal")
	if err != nil {
		return nil, nil, err
	}
	files = map[string]bool{}
	for _, rec := range strings.Split(string(out), "\x00") {
		if len(rec) < 4 {
			continue
		}
		rel := rec[3:]
		if strings.HasSuffix(rel, "/") {
			untrackedDirs = append(untrackedDirs, rel)
			continue
		}
		files[rel] = true
	}
	return files, untrackedDirs, nil
}

// checkAttrGenerated returns the repo-relative paths whose
// linguist-generated attribute is set or true.
func checkAttrGenerated(ctx context.Context, repo string, rels []string) map[string]bool {
	res := map[string]bool{}
	if len(rels) == 0 {
		return res
	}
	out, err := gitCmd{
		args:  []string{"--no-optional-locks", "-C", repo, "check-attr", "--stdin", "-z", "linguist-generated"},
		stdin: strings.NewReader(strings.Join(rels, "\x00") + "\x00"),
	}.run(ctx)
	if err != nil {
		return res
	}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+2 < len(fields); i += 3 {
		if v := fields[i+2]; v == "set" || v == "true" {
			res[fields[i]] = true
		}
	}
	return res
}

// checkIgnored returns the repo-relative paths the repo's ignore rules
// ignore. gitDir, when set, checks against a shadow git dir (so its
// info/exclude applies) with repo as the work tree.
func checkIgnored(ctx context.Context, repo, gitDir string, rels []string) map[string]bool {
	res := map[string]bool{}
	if len(rels) == 0 {
		return res
	}
	args := []string{"--no-optional-locks", "-C", repo}
	if gitDir != "" {
		args = append(args, "--git-dir="+gitDir, "--work-tree="+repo)
	}
	args = append(args, "check-ignore", "--stdin", "-z")
	out, err := gitCmd{args: args, stdin: strings.NewReader(strings.Join(rels, "\x00") + "\x00"), okExit: []int{1}}.run(ctx)
	if err != nil {
		return res
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" {
			res[rel] = true
		}
	}
	return res
}

// commitFiles lists one commit's files (repo-relative, git's order).
func commitFiles(ctx context.Context, repo, hash string) ([]string, error) {
	out, err := repoGit(ctx, repo, "show", "--no-color", "--name-only", "-z", "--no-renames", "--format=", hash)
	if err != nil {
		return nil, err
	}
	var rels []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel = strings.TrimSpace(rel); rel != "" {
			rels = append(rels, rel)
		}
	}
	return rels, nil
}

// showFile reads rel at rev in repo; ok=false when it isn't there.
func showFile(ctx context.Context, repo, rev, rel string) (string, bool) {
	out, err := repoGit(ctx, repo, "show", rev+":"+rel)
	if err != nil {
		return "", false
	}
	return string(out), true
}
