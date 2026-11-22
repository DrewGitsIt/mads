//go:build perf

package main

// perfseed gitfx: real-git fixtures for the git-route scenarios (9x) and
// checks f–h (tools/perf/docs/PLAN.md, "Git routes").
//
//	perfseed gitfx -db <seeded.db> -manifest <manifest.json> -shape <gitfx.shape.json>
//	               -src <repo to clone> -root <fixture dir> -guard <run temp dir>
//
// It clones -src (a read: `git clone --mirror --no-local`, the only git
// command that touches -src) into -root, which must not exist
// and must lie under -guard, and builds every checkout, worktree, branch,
// commit, edit, untracked file and shadow baseline there. Every other git
// command runs with its working directory inside -root, with the user's
// global and system git config ignored (no hooks, no signing). Then it adds
// the fixture sessions, their file-change ledgers and recorded commits to
// the seeded database through the store, and the "git" section to the
// manifest. The origin every clone pushes to and fetches from is
// <root>/origin.git, a local bare clone: GET /push's fetch never leaves the
// temp tree.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// GitFxShape is the fixture's shape (tools/perf/profiles/gitfx.shape.json),
// calibrated by counts against a live session.
type GitFxShape struct {
	Calibration string `json:"calibration"`
	Seed        uint64 `json:"seed"`
	// FarBranches: how many of the source's branches (most commits past
	// main first) become linked worktrees of the multi-repo session.
	Isolated FxSession `json:"isolated"`
	Land     FxSession `json:"land"`
	Multi    FxMulti   `json:"multi"`
	Typical  FxSession `json:"typical"`
}

// FxSession is one single-root session's ledger and work-tree state.
type FxSession struct {
	Name string `json:"name"`
	// Files in the ledger, over Dirs directories (existing, tracked).
	Files int `json:"files"`
	Dirs  int `json:"dirs"`
	// Rows: ledger rows (a file changed several times has several).
	Rows int `json:"rows"`
	// After the session-start baseline: CommittedEdits ledger files edited
	// and committed in Commits commits (recorded when RecordCommits);
	// Uncommitted edited and left; Added new files; Deleted tracked files
	// removed (both uncommitted).
	CommittedEdits int  `json:"committed_edits"`
	Commits        int  `json:"commits"`
	RecordCommits  bool `json:"record_commits"`
	Uncommitted    int  `json:"uncommitted"`
	Added          int  `json:"added"`
	Deleted        int  `json:"deleted"`
	// Untracked, non-ignored files outside the ledger: before the baseline
	// (the user's scratch) and after it (changed outside the session).
	UntrackedBefore int `json:"untracked_before"`
	UntrackedAfter  int `json:"untracked_after"`
}

// FxMulti is the multi-repo session: its folder is not a repo, and its
// ledger spans Checkouts (the first the primary clone, the rest linked
// worktrees on the source's far branches).
type FxMulti struct {
	Name      string       `json:"name"`
	Files     int          `json:"files"`
	Rows      int          `json:"rows"`
	GoneDirs  int          `json:"gone_dirs"`
	Checkouts []FxCheckout `json:"checkouts"`
	// Edited after the baseline and committed (unrecorded: the live
	// session's ledger has no commit rows), in Commits commits.
	CommittedEdits int `json:"committed_edits"`
	Commits        int `json:"commits"`
}

type FxCheckout struct {
	Dirs        int `json:"dirs"`
	Uncommitted int `json:"uncommitted"`
	// Upstream: the worktree's branch tracks its origin branch (else none:
	// to-push counts against origin's default branch).
	Upstream bool `json:"upstream"`
}

// GitFxManifest is what the harness needs to drive and check the fixture.
type GitFxManifest struct {
	Root     string                `json:"root"`
	Origin   string                `json:"origin"`
	Sessions map[string]GitFxEntry `json:"sessions"`
	Counts   map[string]int        `json:"counts"`
	BuildMS  int64                 `json:"build_ms"`
}

type GitFxEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Dir         string   `json:"dir"`
	Folder      string   `json:"folder"`
	Branch      string   `json:"branch,omitempty"`
	Base        string   `json:"base,omitempty"`
	Roots       []string `json:"roots"`
	LedgerFiles int      `json:"ledger_files"`
	LedgerDirs  int      `json:"ledger_dirs"`
	LedgerRows  int      `json:"ledger_rows"`
	Commits     int      `json:"commits_recorded"`
	// A tracked ledger file in Folder the checks may edit, and a dir for new files.
	EditFile string `json:"edit_file"`
	NewDir   string `json:"new_dir"`
}

func runGitFx(args []string) error {
	fs := flag.NewFlagSet("gitfx", flag.ExitOnError)
	dbPath := fs.String("db", "", "seeded database to add the fixture sessions to")
	manPath := fs.String("manifest", "", "the seed's manifest (read, then rewritten with the git section)")
	shapePath := fs.String("shape", "", "fixture shape JSON (tools/perf/profiles/gitfx.shape.json)")
	src := fs.String("src", "", "repository to clone (read only)")
	root := fs.String("root", "", "fixture directory to create (must not exist)")
	guard := fs.String("guard", "", "the run's temp dir: -root must be inside it")
	_ = fs.Parse(args)
	if *dbPath == "" || *manPath == "" || *shapePath == "" || *src == "" || *root == "" || *guard == "" {
		return fmt.Errorf("gitfx: -db, -manifest, -shape, -src, -root and -guard are required")
	}
	var shape GitFxShape
	if b, err := os.ReadFile(*shapePath); err != nil {
		return err
	} else if err := json.Unmarshal(b, &shape); err != nil {
		return fmt.Errorf("gitfx: shape: %w", err)
	}
	var man Manifest
	if b, err := os.ReadFile(*manPath); err != nil {
		return err
	} else if err := json.Unmarshal(b, &man); err != nil {
		return fmt.Errorf("gitfx: manifest: %w", err)
	}
	g, err := newGitFx(*src, *root, *guard, shape)
	if err != nil {
		return err
	}
	started := time.Now()
	gm, err := g.build(context.Background(), *dbPath, &man)
	if err != nil {
		return err
	}
	gm.BuildMS = time.Since(started).Milliseconds()
	man.Git = gm
	fmt.Fprintf(os.Stderr, "perfseed: gitfx: %d sessions, %d git commands, in %.1fs\n", len(gm.Sessions), g.gitCalls, float64(gm.BuildMS)/1000)
	return writeJSON(*manPath, &man)
}

type gitFx struct {
	src, root string
	shape     GitFxShape
	rng       *rand.Rand
	env       []string
	gitCalls  int
}

func physical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// inside reports whether p is dir or under it.
func inside(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func newGitFx(src, root, guard string, shape GitFxShape) (*gitFx, error) {
	var err error
	if src, err = physical(src); err != nil {
		return nil, fmt.Errorf("gitfx: -src: %w", err)
	}
	if guard, err = physical(guard); err != nil {
		return nil, fmt.Errorf("gitfx: -guard: %w", err)
	}
	if _, err := os.Lstat(root); err == nil {
		return nil, fmt.Errorf("gitfx: %s already exists; the fixture is only ever built fresh", root)
	}
	parent, err := physical(filepath.Dir(root))
	if err != nil {
		return nil, fmt.Errorf("gitfx: -root's parent: %w", err)
	}
	root = filepath.Join(parent, filepath.Base(root))
	if !inside(guard, root) || root == guard {
		return nil, fmt.Errorf("gitfx: refusing %s: not inside the run's temp dir %s", root, guard)
	}
	if inside(src, root) || inside(root, src) {
		return nil, fmt.Errorf("gitfx: -root and -src overlap")
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=perf user", "GIT_AUTHOR_EMAIL=perf@perf.test",
		"GIT_COMMITTER_NAME=perf user", "GIT_COMMITTER_EMAIL=perf@perf.test",
		"GIT_CEILING_DIRECTORIES="+guard, "GIT_TERMINAL_PROMPT=0",
	)
	seed := shape.Seed
	if seed == 0 {
		seed = 9
	}
	return &gitFx{src: src, root: root, shape: shape, rng: rand.New(rand.NewPCG(seed, seed^0x51f15e)), env: env}, nil
}

// git runs git with its working directory dir, which must be inside the
// fixture root (the one exception, the clone's read of -src, is cloneSrc).
func (g *gitFx) git(dir string, args ...string) (string, error) {
	if !inside(g.root, dir) {
		return "", fmt.Errorf("gitfx: refusing git in %s (outside %s)", dir, g.root)
	}
	return g.run(dir, args...)
}

func (g *gitFx) run(dir string, args ...string) (string, error) {
	g.gitCalls++
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = g.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s (in %s): %w: %s", strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *gitFx) must(dir string, args ...string) string {
	out, err := g.git(dir, args...)
	if err != nil {
		panic(err)
	}
	return out
}

type fxSess struct {
	key   string
	entry GitFxEntry
	sess  *contracts.Session
	rows  []contracts.FileChange
	// commits to record: hash and the repo they're recorded against.
	commits []contracts.Commit
}

func (g *gitFx) build(ctx context.Context, dbPath string, man *Manifest) (gm *GitFxManifest, err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
				return
			}
			panic(r)
		}
	}()
	if err := os.MkdirAll(g.root, 0o755); err != nil {
		return nil, err
	}
	gm = &GitFxManifest{Root: g.root, Sessions: map[string]GitFxEntry{}, Counts: map[string]int{}}

	// --- The remote: a mirror clone of the source (every ref, its
	// remote-tracking ones too — the only git touching -src), cut loose
	// from it, its main moved back to the source's own origin/main, so local
	// main is ahead of origin/main by as much as the source's is.
	origin := filepath.Join(g.root, "origin.git")
	g.gitCalls++
	clone := exec.Command("git", "clone", "--quiet", "--mirror", "--no-local", g.src, origin)
	clone.Dir = g.root
	clone.Env = g.env
	if out, err := clone.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("gitfx: clone %s: %w: %s", g.src, err, out)
	}
	gm.Origin = origin
	g.must(origin, "remote", "remove", "origin")
	localMain := g.must(origin, "rev-parse", "--verify", "refs/heads/main^{commit}")
	if rm, err := g.git(origin, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/main^{commit}"); err == nil && rm != "" {
		g.must(origin, "update-ref", "refs/heads/main", rm)
	}
	// Only branches and tags: the source's remote-tracking refs, stash and
	// notes are its own business.
	for _, ref := range strings.Split(g.must(origin, "for-each-ref", "--format=%(refname)"), "\n") {
		if ref != "" && !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
			g.must(origin, "update-ref", "-d", ref)
		}
	}
	g.must(origin, "symbolic-ref", "HEAD", "refs/heads/main")

	repos := filepath.Join(g.root, "repos")
	if err := os.MkdirAll(repos, 0o755); err != nil {
		return nil, err
	}
	cloneTo := func(name string) string {
		d := filepath.Join(repos, name)
		g.must(repos, "clone", "--quiet", origin, d)
		return d
	}

	// --- Far branches: the source's branches with the most commits past
	// origin/main (bounded, so one pathological branch can't dominate).
	primary := cloneTo("mads")
	g.must(primary, "checkout", "--quiet", "-B", "main", localMain)
	g.must(primary, "branch", "--quiet", "--set-upstream-to=origin/main", "main")
	far := g.farBranches(primary)
	if len(far) < len(g.shape.Multi.Checkouts) {
		return nil, fmt.Errorf("gitfx: the source has %d far branches, the shape needs %d", len(far), len(g.shape.Multi.Checkouts))
	}

	// --- Multi-repo session: primary + linked worktrees on far branches.
	multiCheckouts := []string{primary}
	for i := 1; i < len(g.shape.Multi.Checkouts); i++ {
		b := far[i]
		d := filepath.Join(repos, fmt.Sprintf("mads-wt%02d", i))
		lb := fmt.Sprintf("perf/wt%02d", i)
		g.must(primary, "worktree", "add", "--quiet", "--no-track", "-b", lb, d, "origin/"+b.name)
		if g.shape.Multi.Checkouts[i].Upstream {
			g.must(d, "branch", "--quiet", "--set-upstream-to=origin/"+b.name, lb)
		}
	}
	for i := 1; i < len(g.shape.Multi.Checkouts); i++ {
		multiCheckouts = append(multiCheckouts, filepath.Join(repos, fmt.Sprintf("mads-wt%02d", i)))
	}

	// --- The isolated sessions' primary: a clone whose checked-out branch
	// (their base) is the farthest branch, tracking origin/main.
	iso := cloneTo("mads-iso")
	g.must(iso, "checkout", "--quiet", "-b", "perf-far", "origin/"+far[0].name)
	g.must(iso, "branch", "--quiet", "--set-upstream-to=origin/main", "perf-far")
	typical := cloneTo("typical")
	g.must(typical, "checkout", "--quiet", "-B", "main", localMain)
	g.must(typical, "branch", "--quiet", "--set-upstream-to=origin/main", "main")

	// The shadow stores where a session child of this stack looks for them:
	// shadow.DefaultRoot() under the XDG_DATA_HOME the harness gives every
	// fixture stack (<root>/xdg), so a child started for a fixture session
	// finds its baseline already made, as a resumed session does.
	shadowRoot := filepath.Join(g.root, "xdg", "mads", "shadow")
	// Session worktrees where the runner keeps them, $MADS_HOME/session-worktrees:
	// a fixture stack's MADS_HOME is <root>/home (stack.ts).
	wtRoot := filepath.Join(g.root, "home", "session-worktrees")
	if err := os.MkdirAll(wtRoot, 0o755); err != nil {
		return nil, err
	}
	created := time.Now().UTC().Add(-6 * time.Hour)

	var all []*fxSess
	// Isolated sessions: a worktree on mads/session/<id> from the base, as
	// the runner's isolate makes it (worktree add -b <branch> <dir> refs/heads/<base>).
	for _, k := range []struct {
		key   string
		shape FxSession
	}{{"isolated", g.shape.Isolated}, {"land", g.shape.Land}} {
		id := hexID(g.rng, "ses")
		dir := filepath.Join(wtRoot, id)
		branch := "mads/session/" + id
		g.must(iso, "worktree", "add", "--quiet", "-b", branch, dir, "refs/heads/perf-far")
		fs := &fxSess{key: k.key}
		fs.entry = GitFxEntry{ID: id, Name: k.shape.Name, Kind: "isolated", Dir: iso, Folder: dir, Branch: branch, Base: "perf-far", Roots: []string{dir}}
		fs.sess = &contracts.Session{
			ID: id, Name: k.shape.Name, Activity: contracts.SessionActive, Workspace: man.WorkspaceID, Provider: "fake", Trust: contracts.TrustFull,
			Isolation: contracts.IsolationWorktree, Dir: iso, Cwd: dir, CreatedAt: created,
		}
		g.single(fs, dir, k.shape, shadowRoot, created)
		all = append(all, fs)
	}
	{
		id := hexID(g.rng, "ses")
		fs := &fxSess{key: "typical"}
		fs.entry = GitFxEntry{ID: id, Name: g.shape.Typical.Name, Kind: "in_place", Dir: typical, Folder: typical, Roots: []string{typical}}
		fs.sess = &contracts.Session{
			ID: id, Name: g.shape.Typical.Name, Activity: contracts.SessionActive, Workspace: man.WorkspaceID, Provider: "fake", Trust: contracts.TrustFull,
			Isolation: contracts.IsolationCurrentDir, Dir: typical, Cwd: typical, CreatedAt: created,
		}
		g.single(fs, typical, g.shape.Typical, shadowRoot, created)
		all = append(all, fs)
	}
	{
		id := hexID(g.rng, "ses")
		fs := &fxSess{key: "multi"}
		fs.entry = GitFxEntry{ID: id, Name: g.shape.Multi.Name, Kind: "multi_repo", Dir: repos, Folder: repos, Roots: multiCheckouts}
		fs.sess = &contracts.Session{
			ID: id, Name: g.shape.Multi.Name, Activity: contracts.SessionActive, Workspace: man.WorkspaceID, Provider: "fake", Trust: contracts.TrustFull,
			Isolation: contracts.IsolationCurrentDir, Dir: repos, Cwd: repos, CreatedAt: created,
		}
		g.multi(fs, multiCheckouts, shadowRoot, created)
		all = append(all, fs)
	}

	// --- Records, through the store.
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	log := store.NewLog(db)
	sessions := store.NewSessions(log)
	for _, fs := range all {
		if err := sessions.Create(ctx, fs.sess, actor); err != nil {
			return nil, fmt.Errorf("gitfx: session %s: %w", fs.key, err)
		}
		if fs.entry.Kind == "isolated" {
			wd, wb, base := fs.entry.Folder, fs.entry.Branch, fs.entry.Base
			if err := sessions.SetWorktree(ctx, fs.sess.ID, contracts.SessionWorktreePatch{WorktreeDir: &wd, WorktreeBranch: &wb, WorktreeBase: &base}, actor); err != nil {
				return nil, fmt.Errorf("gitfx: worktree of %s: %w", fs.key, err)
			}
		}
		for i := range fs.rows {
			fc := fs.rows[i]
			fc.SessionID = fs.sess.ID
			if err := sessions.RecordFileChange(ctx, &fc, actor); err != nil {
				return nil, fmt.Errorf("gitfx: ledger row of %s: %w", fs.key, err)
			}
		}
		for _, c := range fs.commits {
			c.SessionID = fs.sess.ID
			if _, err := sessions.RecordCommit(ctx, &store.CommitReport{Commit: c}, actor); err != nil {
				return nil, fmt.Errorf("gitfx: commit of %s: %w", fs.key, err)
			}
		}
		fs.entry.Commits = len(fs.commits)
		gm.Sessions[fs.key] = fs.entry
		man.Sessions = append(man.Sessions, ManifestRef{ID: fs.sess.ID, Name: fs.sess.Name})
		gm.Counts[fs.key+"_ledger_rows"] = len(fs.rows)
		gm.Counts[fs.key+"_commits_recorded"] = len(fs.commits)
	}
	gm.Counts["far_branch_commits_past_origin_main"] = far[0].ahead
	gm.Counts["linked_worktrees"] = len(multiCheckouts) - 1
	gm.Counts["fixture_git_commands"] = g.gitCalls
	return gm, nil
}

type farBranch struct {
	name  string
	ahead int
}

// farBranches lists origin's branches (but main) by commits past
// origin/main, most first, those with 10–600.
func (g *gitFx) farBranches(repo string) []farBranch {
	refs := strings.Split(g.must(repo, "for-each-ref", "--format=%(refname:short)", "refs/remotes/origin/"), "\n")
	var out []farBranch
	for _, r := range refs {
		name := strings.TrimPrefix(r, "origin/")
		if name == "" || name == "HEAD" || name == "main" || name == "origin" {
			continue
		}
		n := 0
		_, _ = fmt.Sscan(g.must(repo, "rev-list", "--count", "origin/main.."+r), &n)
		if n >= 10 && n <= 600 {
			out = append(out, farBranch{name, n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ahead != out[j].ahead {
			return out[i].ahead > out[j].ahead
		}
		return out[i].name < out[j].name
	})
	return out
}

// textFile: a tracked file the fixture may append a line to.
func textFile(p string) bool {
	switch filepath.Ext(p) {
	case ".go", ".ts", ".tsx", ".md", ".css", ".sh", ".yaml", ".yml", ".txt", ".mjs", ".js", ".html", ".sql", ".toml":
		return true
	}
	return false
}

// trackedByDir is the checkout's tracked text files grouped by directory
// (absolute dir → absolute paths), and the dirs sorted.
func (g *gitFx) trackedByDir(checkout string) (map[string][]string, []string) {
	by := map[string][]string{}
	for _, rel := range strings.Split(g.must(checkout, "ls-files"), "\n") {
		if rel == "" || !textFile(rel) {
			continue
		}
		abs := filepath.Join(checkout, rel)
		if fi, err := os.Lstat(abs); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		d := filepath.Dir(abs)
		by[d] = append(by[d], abs)
	}
	dirs := make([]string, 0, len(by))
	for d := range by {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return by, dirs
}

// pickFiles picks nFiles tracked text files over nDirs directories of checkout.
func (g *gitFx) pickFiles(checkout string, nDirs, nFiles int) (files []string, dirs []string) {
	by, all := g.trackedByDir(checkout)
	perm := g.rng.Perm(len(all))
	if nDirs > len(all) {
		nDirs = len(all)
	}
	for _, i := range perm[:nDirs] {
		dirs = append(dirs, all[i])
	}
	sort.Strings(dirs)
	// One file per dir first, then the rest spread at random over them.
	taken := map[string]bool{}
	for _, d := range dirs {
		f := by[d][g.rng.IntN(len(by[d]))]
		taken[f] = true
		files = append(files, f)
	}
	for tries := 0; len(files) < nFiles && tries < nFiles*20; tries++ {
		d := dirs[g.rng.IntN(len(dirs))]
		f := by[d][g.rng.IntN(len(by[d]))]
		if !taken[f] {
			taken[f] = true
			files = append(files, f)
		}
	}
	return files, dirs
}

func appendLine(p, line string) {
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		panic(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line + "\n"); err != nil {
		panic(err)
	}
}

func writeNew(p, content string) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		panic(err)
	}
}

// ledgerRows spreads rows over files: each file one row (its change),
// the extra rows earlier "modified" rows of files picked at random.
func (g *gitFx) ledgerRows(repo string, changes []fileChange, total int, from time.Time) []contracts.FileChange {
	extra := map[int]int{}
	for i := len(changes); i < total; i++ {
		extra[g.rng.IntN(len(changes))]++
	}
	var rows []contracts.FileChange
	t := from
	for i, c := range changes {
		for range extra[i] {
			t = t.Add(3 * time.Second)
			rows = append(rows, contracts.FileChange{Repo: repo, Path: c.path, Change: "modified", Origin: contracts.OriginToolUse, Timestamp: t})
		}
		t = t.Add(3 * time.Second)
		rows = append(rows, contracts.FileChange{Repo: repo, Path: c.path, Change: c.change, Origin: contracts.OriginToolUse, Timestamp: t})
	}
	// Ledger order is time order.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Timestamp.Before(rows[j].Timestamp) })
	return rows
}

type fileChange struct {
	path, change string
}

// commitFiles edits files and commits them in n commits in checkout;
// returns the commit hashes, oldest first.
func (g *gitFx) commitFiles(checkout string, files []string, n int, tag string) []string {
	if n <= 0 || len(files) == 0 {
		return nil
	}
	var hashes []string
	per := (len(files) + n - 1) / n
	for i := 0; i < n; i++ {
		lo, hi := i*per, min((i+1)*per, len(files))
		if lo >= hi {
			// More commits than files: an extra edit to the last file.
			lo, hi = len(files)-1, len(files)
		}
		args := []string{"add", "--"}
		for _, f := range files[lo:hi] {
			appendLine(f, fmt.Sprintf("// perf %s commit %d", tag, i+1))
			rel, _ := filepath.Rel(checkout, f)
			args = append(args, rel)
		}
		g.must(checkout, args...)
		g.must(checkout, "commit", "--quiet", "--no-verify", "-m", fmt.Sprintf("perf %s: change %d of %d", tag, i+1, n))
		hashes = append(hashes, g.must(checkout, "rev-parse", "HEAD"))
	}
	return hashes
}

// single builds a one-root session (isolated or in place): baseline, then
// the session's work, then its ledger.
func (g *gitFx) single(fs *fxSess, folder string, sh FxSession, shadowRoot string, from time.Time) {
	files, dirs := g.pickFiles(folder, sh.Dirs, sh.Files)
	// The user's own scratch, there before the session started.
	for i := 0; i < sh.UntrackedBefore; i++ {
		writeNew(filepath.Join(dirs[g.rng.IntN(len(dirs))], fmt.Sprintf("perf_scratch_before_%02d.txt", i)), "scratch\n")
	}
	st := shadow.New(shadowRoot, fs.sess.ID)
	st.Baseline(folder)
	st.Wait()
	fs.sess.ShadowDir = st.Dir()

	// Files: committed edits, uncommitted edits, deletions; the rest of the
	// ledger was changed and changed back (only the ledger knows).
	perm := g.rng.Perm(len(files))
	at := 0
	take := func(n int) []string {
		var out []string
		for ; n > 0 && at < len(perm); n-- {
			out = append(out, files[perm[at]])
			at++
		}
		return out
	}
	committed := take(sh.CommittedEdits)
	uncommitted := take(sh.Uncommitted)
	deleted := take(sh.Deleted)
	hashes := g.commitFiles(folder, committed, sh.Commits, fs.key)
	for _, f := range uncommitted {
		appendLine(f, "// perf uncommitted edit")
	}
	for _, f := range deleted {
		if err := os.Remove(f); err != nil {
			panic(err)
		}
	}
	changes := make([]fileChange, 0, len(files)+sh.Added)
	gone := map[string]bool{}
	for _, f := range deleted {
		gone[f] = true
	}
	for _, f := range files {
		c := "modified"
		if gone[f] {
			c = "deleted"
		}
		changes = append(changes, fileChange{f, c})
	}
	for i := 0; i < sh.Added; i++ {
		p := filepath.Join(dirs[g.rng.IntN(len(dirs))], fmt.Sprintf("perf_added_%02d.md", i))
		writeNew(p, fmt.Sprintf("# perf added %d\n", i))
		changes = append(changes, fileChange{p, "added"})
	}
	for i := 0; i < sh.UntrackedAfter; i++ {
		writeNew(filepath.Join(dirs[g.rng.IntN(len(dirs))], fmt.Sprintf("perf_scratch_after_%02d.txt", i)), "outside\n")
	}
	g.rng.Shuffle(len(changes), func(i, j int) { changes[i], changes[j] = changes[j], changes[i] })
	rows := sh.Rows
	if rows < len(changes) {
		rows = len(changes)
	}
	fs.rows = g.ledgerRows(folder, changes, rows, from)
	if sh.RecordCommits {
		repo := filepath.Dir(g.must(folder, "rev-parse", "--path-format=absolute", "--git-common-dir"))
		t := from.Add(time.Hour)
		for i, h := range hashes {
			fs.commits = append(fs.commits, contracts.Commit{Hash: h, Repo: repo, Message: fmt.Sprintf("perf %s: change %d of %d", fs.key, i+1, len(hashes)), Author: "perf user", Timestamp: t.Add(time.Duration(i) * time.Minute)})
		}
	}
	fs.entry.LedgerFiles = len(changes)
	fs.entry.LedgerDirs = len(dirs)
	fs.entry.LedgerRows = len(fs.rows)
	// What the checks edit: a ledger file still tracked and unedited, and a dir.
	for _, f := range files[min(at, len(files)-1):] {
		if !gone[f] {
			fs.entry.EditFile = f
			break
		}
	}
	if fs.entry.EditFile == "" {
		fs.entry.EditFile = files[0]
	}
	fs.entry.NewDir = dirs[0]
}

// multi builds the multi-repo session over checkouts: per-root baselines,
// edits spread over the checkouts, a few dirs that no longer exist.
func (g *gitFx) multi(fs *fxSess, checkouts []string, shadowRoot string, from time.Time) {
	sh := g.shape.Multi
	totalDirs := 0
	for _, c := range sh.Checkouts {
		totalDirs += c.Dirs
	}
	st := shadow.New(shadowRoot, fs.sess.ID)
	for _, c := range checkouts {
		st.Baseline(c)
	}
	st.Wait()
	fs.sess.ShadowDir = st.Dir()

	var changes []fileChange
	dirsSeen := 0
	var firstFile, firstDir string
	for i, co := range sh.Checkouts {
		checkout := checkouts[i]
		nFiles := (sh.Files - sh.GoneDirs*2) * co.Dirs / totalDirs
		files, dirs := g.pickFiles(checkout, co.Dirs, max(nFiles, co.Dirs))
		dirsSeen += len(dirs)
		if i == 0 {
			firstDir = dirs[0]
		}
		perm := g.rng.Perm(len(files))
		nCommitted := sh.CommittedEdits * len(files) / max(1, sh.Files)
		committed := make([]string, 0, nCommitted)
		at := 0
		for ; at < len(perm) && len(committed) < nCommitted; at++ {
			committed = append(committed, files[perm[at]])
		}
		nCommits := max(1, sh.Commits*len(files)/max(1, sh.Files))
		g.commitFiles(checkout, committed, nCommits, fmt.Sprintf("multi-%02d", i))
		for n := 0; n < co.Uncommitted && at < len(perm); n, at = n+1, at+1 {
			appendLine(files[perm[at]], "// perf uncommitted edit")
		}
		if i == 0 && at < len(perm) {
			firstFile = files[perm[at]]
		}
		for _, f := range files {
			changes = append(changes, fileChange{f, "modified"})
		}
	}
	// Dirs that are gone (a deleted package, a removed worktree's subdir):
	// toplevel resolution walks up to their nearest existing ancestor.
	for i := 0; i < sh.GoneDirs; i++ {
		co := checkouts[g.rng.IntN(len(checkouts))]
		d := filepath.Join(co, "perf-gone", fmt.Sprintf("pkg%02d", i))
		for k := 0; k < 2; k++ {
			changes = append(changes, fileChange{filepath.Join(d, fmt.Sprintf("gone_%d.go", k)), "deleted"})
		}
	}
	g.rng.Shuffle(len(changes), func(i, j int) { changes[i], changes[j] = changes[j], changes[i] })
	fs.rows = g.ledgerRows(filepath.Dir(checkouts[0]), changes, sh.Rows, from)
	fs.entry.LedgerFiles = len(changes)
	fs.entry.LedgerDirs = dirsSeen + sh.GoneDirs
	fs.entry.LedgerRows = len(fs.rows)
	fs.entry.EditFile = firstFile
	fs.entry.NewDir = firstDir
}
