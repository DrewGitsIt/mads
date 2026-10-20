package changes

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// --- helpers ---

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// tempDir is t.TempDir canonicalized (macOS /var → /private/var), the form
// every change-set path takes.
func tempDir(t *testing.T) string {
	t.Helper()
	return shadow.RealPath(t.TempDir())
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo makes a git repo on branch main holding files, committed.
func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := tempDir(t)
	git(t, repo, "init", "-q", "-b", "main")
	for rel, content := range files {
		writeFile(t, filepath.Join(repo, rel), content)
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "seed")
	return repo
}

// shadowFor baselines repo in a fresh shadow store and returns a session
// rooted there.
func shadowFor(t *testing.T, repo string) (contracts.Session, *shadow.Store) {
	t.Helper()
	store := shadow.New(t.TempDir(), "sess")
	t.Cleanup(store.Wait)
	store.Baseline(repo)
	store.Wait()
	return contracts.Session{ID: "sess", Dir: repo, ShadowDir: store.Dir()}, store
}

func build(t *testing.T, in Inputs) contracts.ChangeSet {
	t.Helper()
	set, err := Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return set
}

func byDisplay(set contracts.ChangeSet) map[string]contracts.ChangedFile {
	out := map[string]contracts.ChangedFile{}
	for _, f := range set.Files {
		out[f.Display] = f
	}
	return out
}

func ledgerRow(path, change string, at time.Time) contracts.FileChange {
	return contracts.FileChange{Path: path, Change: change, Origin: contracts.OriginToolUse, Attributed: true, Timestamp: at}
}

// --- session start ---

func TestBuildStartFromDisk(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"a.go":       "package a\n\nfunc A() {}\n",
		"gone.txt":   "one\ntwo\n",
		"revert.txt": "same\n",
		"keep.txt":   "untouched\n",
	})
	sess, _ := shadowFor(t, repo)

	writeFile(t, filepath.Join(repo, "a.go"), "package a\n\nfunc A() { B() }\n\nfunc B() {}\n")
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "revert.txt"), "changed\n")
	writeFile(t, filepath.Join(repo, "revert.txt"), "same\n") // put back: drops out
	writeFile(t, filepath.Join(repo, "new.go"), "package a\n")
	writeFile(t, filepath.Join(repo, "blob.bin"), "a\x00b\x00c")

	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	set := build(t, Inputs{
		Session: sess,
		Ledger: []contracts.FileChange{
			ledgerRow(filepath.Join(repo, "a.go"), "modified", at),
			ledgerRow(filepath.Join(repo, "revert.txt"), "modified", at),
		},
	})

	files := byDisplay(set)
	if len(files) != 4 {
		t.Fatalf("files = %v, want a.go, gone.txt, new.go, blob.bin", keys(files))
	}
	a := files["a.go"]
	if a.Change != "modified" || a.Added != 3 || a.Deleted != 1 || !a.Attributed || a.Tier != contracts.TierSource {
		t.Errorf("a.go = %+v", a)
	}
	if !a.LastChanged.Equal(at) {
		t.Errorf("a.go LastChanged = %v, want ledger time %v", a.LastChanged, at)
	}
	if a.Path != filepath.Join(repo, "a.go") || a.Outside {
		t.Errorf("a.go path = %q outside=%v", a.Path, a.Outside)
	}
	if g := files["gone.txt"]; g.Change != "deleted" || g.Deleted != 2 || g.Attributed {
		t.Errorf("gone.txt = %+v (unattributed: seen only on disk)", g)
	}
	if n := files["new.go"]; n.Change != "added" || n.Added != 1 || n.Attributed || !n.Uncommitted {
		t.Errorf("new.go = %+v", n)
	}
	if b := files["blob.bin"]; !b.Binary || b.Added != 0 || b.Deleted != 0 {
		t.Errorf("blob.bin = %+v, want binary with zero counts", b)
	}
	if !set.Git || set.Baseline != contracts.BaselineSessionStart {
		t.Errorf("Git=%v Baseline=%q", set.Git, set.Baseline)
	}
	if set.Totals.Files != 4 || set.Totals.Added != 4 || set.Totals.Deleted != 3 {
		t.Errorf("totals = %+v", set.Totals)
	}
	if len(set.Notes) != 0 {
		t.Errorf("notes = %v, want none", set.Notes)
	}
}

// The shadow store's own index, refs and commits must be untouched by a
// Build.
func TestBuildDoesNotMutateShadowStore(t *testing.T) {
	repo := newRepo(t, map[string]string{"a.txt": "a\n"})
	sess, _ := shadowFor(t, repo)
	gitDir := filepath.Join(sess.ShadowDir, shadow.RootKey(repo))
	indexBefore, err := os.ReadFile(filepath.Join(gitDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	refsBefore := git(t, gitDir, "--git-dir="+gitDir, "show-ref")
	headBefore := git(t, gitDir, "--git-dir="+gitDir, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(repo, "a.txt"), "b\n")
	writeFile(t, filepath.Join(repo, "n.txt"), "n\n")
	build(t, Inputs{Session: sess})

	indexAfter, _ := os.ReadFile(filepath.Join(gitDir, "index"))
	if string(indexBefore) != string(indexAfter) {
		t.Error("shadow index changed")
	}
	if got := git(t, gitDir, "--git-dir="+gitDir, "show-ref"); got != refsBefore {
		t.Errorf("refs changed: %q → %q", refsBefore, got)
	}
	if got := git(t, gitDir, "--git-dir="+gitDir, "rev-parse", "HEAD"); got != headBefore {
		t.Error("HEAD moved")
	}
}

func TestBuildStartRespectsIgnoresAndExcludes(t *testing.T) {
	repo := newRepo(t, map[string]string{".gitignore": "*.log\n", "a.txt": "a\n"})
	sess, _ := shadowFor(t, repo)
	writeFile(t, filepath.Join(repo, "node_modules", "x", "i.js"), "x\n") // mads exclude list
	writeFile(t, filepath.Join(repo, "noise.log"), "x\n")                 // gitignored, not in ledger
	writeFile(t, filepath.Join(repo, "agent.log"), "x\ny\n")              // gitignored, created by the agent

	set := build(t, Inputs{
		Session: sess,
		Ledger:  []contracts.FileChange{ledgerRow(filepath.Join(repo, "agent.log"), "added", time.Now())},
	})
	files := byDisplay(set)
	if len(files) != 1 {
		t.Fatalf("files = %v, want only agent.log", keys(files))
	}
	f := files["agent.log"]
	if f.Change != "added" || f.Added != 2 || f.Tier != contracts.TierScratch || f.ScratchRule != RuleGitignored || !f.Uncommitted {
		t.Errorf("agent.log = %+v", f)
	}
}

// The shadow store's own excludes (build/, dist/, …) hide a file from the
// disk diff, so the ledger lists it — but the user's repo doesn't ignore
// it, so it isn't "gitignored" scratch. A directory in the ledger isn't a
// file change.
func TestBuildShadowExcludeIsNotGitignored(t *testing.T) {
	repo := newRepo(t, map[string]string{"a.txt": "a\n"})
	sess, _ := shadowFor(t, repo)
	out := filepath.Join(repo, "build", "out.js")
	writeFile(t, out, "x\n")
	set := build(t, Inputs{
		Session: sess,
		Ledger: []contracts.FileChange{
			ledgerRow(out, "added", time.Now()),
			ledgerRow(filepath.Join(repo, "build"), "added", time.Now()),
		},
	})
	files := byDisplay(set)
	if len(files) != 1 {
		t.Fatalf("files = %v, want only build/out.js", keys(files))
	}
	f := files["build/out.js"]
	if f.Tier != contracts.TierGenerated || f.ScratchRule != "" || f.Added != 1 || !f.Uncommitted {
		t.Errorf("build/out.js = %+v, want generated, not gitignored scratch", f)
	}
}

// A root whose baseline isn't there yet lists files from the ledger and
// says so.
func TestBuildStartBaselinePending(t *testing.T) {
	repo := newRepo(t, map[string]string{"a.txt": "a\n"})
	store := shadow.New(t.TempDir(), "sess") // never baselined
	sess := contracts.Session{ID: "sess", Dir: repo, ShadowDir: store.Dir()}
	writeFile(t, filepath.Join(repo, "a.txt"), "a\nb\n")
	writeFile(t, filepath.Join(repo, "b.txt"), "b\n") // not in ledger: invisible

	set := build(t, Inputs{
		Session: sess,
		Ledger:  []contracts.FileChange{ledgerRow(filepath.Join(repo, "a.txt"), "modified", time.Now())},
	})
	files := byDisplay(set)
	if len(files) != 1 || files["a.txt"].Added != 1 {
		t.Fatalf("files = %+v, want a.txt +1 (vs HEAD)", set.Files)
	}
	if len(set.Notes) != 1 || !strings.Contains(set.Notes[0], "still running") {
		t.Errorf("notes = %v, want the snapshot-running note", set.Notes)
	}
}

func TestBuildNonRepoFiles(t *testing.T) {
	repo := newRepo(t, map[string]string{"a.txt": "a\n"})
	sess, store := shadowFor(t, repo)
	outside := tempDir(t) // not a repo, not the session folder

	captured := filepath.Join(outside, "captured.txt")
	writeFile(t, captured, "one\ntwo\n")
	store.Capture("toolu_1", captured)
	writeFile(t, captured, "one\nTWO\nthree\n")

	uncaptured := filepath.Join(outside, "shell.txt") // changed by a shell command: no pre-image
	writeFile(t, uncaptured, "x\n")

	created := filepath.Join(outside, "made.txt")
	store.Capture("toolu_2", created)
	writeFile(t, created, "new\n")

	transient := filepath.Join(outside, "transient.txt") // created then deleted: drops out
	store.Capture("toolu_3", transient)

	reverted := filepath.Join(outside, "reverted.txt")
	writeFile(t, reverted, "orig\n")
	store.Capture("toolu_4", reverted)

	at := time.Now()
	set := build(t, Inputs{
		Session: sess,
		Ledger: []contracts.FileChange{
			ledgerRow(captured, "modified", at),
			ledgerRow(uncaptured, "modified", at),
			ledgerRow(created, "added", at),
			ledgerRow(transient, "added", at),
			ledgerRow(transient, "deleted", at),
			ledgerRow(reverted, "modified", at),
		},
	})
	files := byDisplay(set)
	if len(files) != 3 {
		t.Fatalf("files = %v, want captured, shell, made", keys(files))
	}
	c := files[captured]
	if c.Change != "modified" || c.Added != 2 || c.Deleted != 1 || !c.Outside || c.Display != captured || c.Tier != contracts.TierSource || !c.Uncommitted {
		t.Errorf("captured = %+v", c)
	}
	if u := files[uncaptured]; !u.CountsUnknown || u.Change != "modified" {
		t.Errorf("shell.txt = %+v, want CountsUnknown", u)
	}
	if m := files[created]; m.Change != "added" || m.Added != 1 || m.Tier != contracts.TierScratch || m.ScratchRule != RuleOutside {
		t.Errorf("made.txt = %+v, want scratch (created outside folder)", m)
	}
	if len(set.Notes) != 1 || !strings.Contains(set.Notes[0], "line counts unavailable for 1 file") {
		t.Errorf("notes = %v", set.Notes)
	}
}

// A non-git session folder: no baseline, files from the ledger, Git false.
func TestBuildNonGitFolder(t *testing.T) {
	dir := tempDir(t)
	store := shadow.New(t.TempDir(), "sess")
	sess := contracts.Session{ID: "sess", Dir: dir, ShadowDir: store.Dir()}
	p := filepath.Join(dir, "notes.md")
	writeFile(t, p, "a\n")
	store.Baseline(dir) // a no-op for a non-repo root
	store.Capture("toolu_1", p)
	writeFile(t, p, "a\nb\n")

	set := build(t, Inputs{Session: sess, Ledger: []contracts.FileChange{ledgerRow(p, "modified", time.Now())}})
	if set.Git {
		t.Error("Git = true for a non-git folder")
	}
	f := byDisplay(set)["notes.md"]
	if f.Added != 1 || f.Outside || f.Display != "notes.md" {
		t.Errorf("notes.md = %+v", f)
	}
	for _, bl := range set.Baselines {
		if bl.ID != contracts.BaselineSessionStart && bl.Available {
			t.Errorf("baseline %s available in a non-git folder", bl.ID)
		}
	}
	if _, err := Build(context.Background(), Inputs{Session: sess, Baseline: contracts.BaselineMain}); !errors.Is(err, ErrBaselineUnavailable) {
		t.Errorf("vs=main err = %v, want ErrBaselineUnavailable", err)
	}
}

// --- scratch "created in session" ---

func TestBuildScratchNeedsCreatedInSession(t *testing.T) {
	repo := newRepo(t, map[string]string{"tmp_old.go": "package a\n", "keep.go": "package a\n"})
	sess, _ := shadowFor(t, repo)
	writeFile(t, filepath.Join(repo, "tmp_old.go"), "package a\n\nvar X = 1\n")
	writeFile(t, filepath.Join(repo, "tmp_new.go"), "package a\n")
	writeFile(t, filepath.Join(repo, "scratch", "try.go"), "package scratch\n")
	writeFile(t, filepath.Join(repo, "test_repro.py"), "print(1)\n")
	writeFile(t, filepath.Join(repo, "zz_gen.go"), "// Code generated by hand. DO NOT EDIT.\npackage a\n")

	files := byDisplay(build(t, Inputs{Session: sess}))
	for display, want := range map[string]struct{ tier, rule string }{
		"tmp_old.go":     {contracts.TierSource, ""},
		"tmp_new.go":     {contracts.TierScratch, RuleName},
		"scratch/try.go": {contracts.TierScratch, RuleScratchDir},
		"test_repro.py":  {contracts.TierScratch, RuleName},
		"zz_gen.go":      {contracts.TierGenerated, ""},
	} {
		f, ok := files[display]
		if !ok {
			t.Errorf("%s missing", display)
			continue
		}
		if f.Tier != want.tier || f.ScratchRule != want.rule {
			t.Errorf("%s = (%s, %q), want (%s, %q)", display, f.Tier, f.ScratchRule, want.tier, want.rule)
		}
	}
}

func TestBuildLinguistGenerated(t *testing.T) {
	repo := newRepo(t, map[string]string{".gitattributes": "api/** linguist-generated\n", "api/client.ts": "a\n"})
	sess, _ := shadowFor(t, repo)
	writeFile(t, filepath.Join(repo, "api", "client.ts"), "b\n")
	if f := byDisplay(build(t, Inputs{Session: sess}))["api/client.ts"]; f.Tier != contracts.TierGenerated {
		t.Errorf("api/client.ts tier = %s, want generated", f.Tier)
	}
}

// --- sort order ---

func TestBuildSortOrder(t *testing.T) {
	repo := newRepo(t, nil)
	sess, _ := shadowFor(t, repo)
	writeFile(t, filepath.Join(repo, "small.go"), "1\n")
	writeFile(t, filepath.Join(repo, "big.go"), "1\n2\n3\n")
	writeFile(t, filepath.Join(repo, "b.go"), "1\n")
	writeFile(t, filepath.Join(repo, "x_test.go"), "1\n2\n3\n4\n5\n")
	writeFile(t, filepath.Join(repo, "go.mod"), "module x\n")
	writeFile(t, filepath.Join(repo, "tmp_a.go"), "1\n")
	set := build(t, Inputs{Session: sess})
	var got []string
	for _, f := range set.Files {
		got = append(got, f.Display)
	}
	want := []string{"big.go", "b.go", "small.go", "x_test.go", "go.mod", "tmp_a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// --- turns and commits ---

func TestBuildTurnsAndCommits(t *testing.T) {
	repo := newRepo(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	sess, _ := shadowFor(t, repo)
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	transcript := []contracts.TranscriptMessage{
		{Role: "user", Text: "change a", Timestamp: t0},
		{Role: "agent", Text: "done", Timestamp: t0.Add(time.Minute)},
		{Role: "user", Text: "now b", Timestamp: t0.Add(10 * time.Minute)},
	}

	writeFile(t, filepath.Join(repo, "a.txt"), "a2\n")
	git(t, repo, "add", "a.txt")
	git(t, repo, "commit", "-q", "-m", "change a\n\nbody")
	hashA := git(t, repo, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(repo, "b.txt"), "b2\n")

	ledger := []contracts.FileChange{
		ledgerRow(filepath.Join(repo, "a.txt"), "modified", t0.Add(30*time.Second)),
		ledgerRow(filepath.Join(repo, "b.txt"), "modified", t0.Add(11*time.Minute)),
		ledgerRow(filepath.Join(repo, "a.txt"), "modified", t0.Add(12*time.Minute)),
	}
	commits := []contracts.Commit{{Hash: hashA, Repo: repo, Message: "change a\n\nbody", Timestamp: t0.Add(2 * time.Minute)}}
	set := build(t, Inputs{Session: sess, Ledger: ledger, Commits: commits, Transcript: transcript})

	if len(set.Turns) != 2 || set.Turns[0].Files != 1 || set.Turns[1].Files != 2 {
		t.Errorf("turns = %+v, want turn 1 with 1 file, turn 2 with 2", set.Turns)
	}
	files := byDisplay(set)
	a, b := files["a.txt"], files["b.txt"]
	if !reflect.DeepEqual(a.Turns, []int{1, 2}) || !reflect.DeepEqual(b.Turns, []int{2}) {
		t.Errorf("turns a=%v b=%v", a.Turns, b.Turns)
	}
	if !reflect.DeepEqual(a.Commits, []string{hashA}) || a.Uncommitted {
		t.Errorf("a.txt commits=%v uncommitted=%v, want [%s] false", a.Commits, a.Uncommitted, hashA)
	}
	if len(b.Commits) != 0 || !b.Uncommitted {
		t.Errorf("b.txt commits=%v uncommitted=%v", b.Commits, b.Uncommitted)
	}
	if len(set.Commits) != 1 || set.Commits[0].Message != "change a" ||
		!reflect.DeepEqual(set.Commits[0].Paths, []string{filepath.Join(repo, "a.txt")}) {
		t.Errorf("commits = %+v", set.Commits)
	}

	// No timestamps: no turns anywhere.
	set = build(t, Inputs{Session: sess, Ledger: ledger, Transcript: []contracts.TranscriptMessage{{Role: "user", Text: "x"}}})
	if len(set.Turns) != 0 {
		t.Errorf("turns = %+v, want none", set.Turns)
	}
	for _, f := range set.Files {
		if f.Turns == nil || len(f.Turns) != 0 {
			t.Errorf("%s turns = %#v, want empty", f.Display, f.Turns)
		}
	}
}

// --- base / main ---

func TestBuildMergeBaseBaselines(t *testing.T) {
	primary := newRepo(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	wt := filepath.Join(tempDir(t), "wt")
	git(t, primary, "worktree", "add", "-q", "-b", "mads/session/s", wt)

	// Committed on the session branch before the session's shadow baseline
	// (e.g. a resumed session), then uncommitted and untracked work.
	writeFile(t, filepath.Join(wt, "a.txt"), "a\nmore\n")
	git(t, wt, "commit", "-q", "-am", "earlier")
	sess, _ := shadowFor(t, wt)
	sess.WorktreeDir, sess.WorktreeBase = wt, "main"
	writeFile(t, filepath.Join(wt, "b.txt"), "B\n")
	writeFile(t, filepath.Join(wt, "tmp_new.txt"), "n\nn\n")

	set := build(t, Inputs{Session: sess, Baseline: contracts.BaselineBase})
	files := byDisplay(set)
	if len(files) != 3 {
		t.Fatalf("files = %v, want a.txt, b.txt, tmp_new.txt", keys(files))
	}
	if a := files["a.txt"]; a.Added != 1 || a.Deleted != 0 || a.Uncommitted || a.Tier != contracts.TierSource {
		t.Errorf("a.txt = %+v, want committed +1", a)
	}
	if b := files["b.txt"]; b.Added != 1 || b.Deleted != 1 || !b.Uncommitted {
		t.Errorf("b.txt = %+v", b)
	}
	if n := files["tmp_new.txt"]; n.Change != "added" || n.Added != 2 || n.Tier != contracts.TierScratch {
		t.Errorf("tmp_new.txt = %+v, want added scratch (absent at session start)", n)
	}

	// Session start only sees the later work.
	start := byDisplay(build(t, Inputs{Session: sess}))
	if _, ok := start["a.txt"]; ok || len(start) != 2 {
		t.Errorf("start files = %v, want b.txt and tmp_new.txt", keys(start))
	}

	// main resolves to the same merge base as the base branch here.
	want := []contracts.ChangeBaseline{
		{ID: "start", Label: "Session start", Available: true},
		{ID: "base", Label: "Base branch · main", Available: true},
		{ID: "main", Label: "main", Reason: "same as Base branch"},
	}
	if !reflect.DeepEqual(set.Baselines, want) {
		t.Errorf("baselines = %+v, want %+v", set.Baselines, want)
	}
	if _, err := Build(context.Background(), Inputs{Session: sess, Baseline: contracts.BaselineMain}); !errors.Is(err, ErrBaselineUnavailable) {
		t.Errorf("vs=main err = %v, want ErrBaselineUnavailable", err)
	}
	if _, err := Build(context.Background(), Inputs{Session: sess, Baseline: "bogus"}); !errors.Is(err, ErrBaselineUnavailable) {
		t.Errorf("vs=bogus err = %v, want ErrBaselineUnavailable", err)
	}

	// Per-file original at the merge base.
	orig, ok, err := MergeBaseOriginal(context.Background(), sess, contracts.BaselineBase, filepath.Join(wt, "a.txt"))
	if err != nil || !ok || orig != "a\n" {
		t.Errorf("MergeBaseOriginal(a.txt) = %q %v %v, want \"a\\n\"", orig, ok, err)
	}
	orig, ok, err = MergeBaseOriginal(context.Background(), sess, contracts.BaselineBase, filepath.Join(wt, "tmp_new.txt"))
	if err != nil || !ok || orig != "" {
		t.Errorf("MergeBaseOriginal(new) = %q %v %v, want empty", orig, ok, err)
	}
	if _, ok, _ := MergeBaseOriginal(context.Background(), sess, contracts.BaselineBase, "/etc/hosts"); ok {
		t.Error("MergeBaseOriginal outside the repo reported ok")
	}
}

// In place on a feature branch: no base branch; main is the default
// branch's merge base.
func TestBuildMainInPlace(t *testing.T) {
	repo := newRepo(t, map[string]string{"a.txt": "a\n"})
	git(t, repo, "checkout", "-q", "-b", "feat")
	writeFile(t, filepath.Join(repo, "a.txt"), "a\nfeat\n")
	git(t, repo, "commit", "-q", "-am", "feat")
	git(t, repo, "checkout", "-q", "main")
	writeFile(t, filepath.Join(repo, "main.txt"), "m\n")
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "main moves on")
	git(t, repo, "checkout", "-q", "feat")
	sess := contracts.Session{ID: "s", Dir: repo}

	set := build(t, Inputs{Session: sess, Baseline: contracts.BaselineMain})
	files := byDisplay(set)
	if len(files) != 1 || files["a.txt"].Added != 1 {
		t.Errorf("files = %+v, want only a.txt +1 (main's later commit isn't ours)", set.Files)
	}
	if set.Baselines[1].Available || set.Baselines[1].Reason == "" {
		t.Errorf("base = %+v, want unavailable with a reason", set.Baselines[1])
	}
	if !set.Baselines[2].Available {
		t.Errorf("main = %+v, want available", set.Baselines[2])
	}
}

// --- delete scratch ---

func TestDeleteScratch(t *testing.T) {
	repo := newRepo(t, map[string]string{"keep.go": "package a\n"})
	sess, _ := shadowFor(t, repo)
	writeFile(t, filepath.Join(repo, "keep.go"), "package a\n\nvar X = 1\n")
	scratch := filepath.Join(repo, "tmp_a.txt")
	writeFile(t, scratch, "x\n")
	link := filepath.Join(repo, "tmp_link.txt")
	if err := os.Symlink(filepath.Join(repo, "keep.go"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	nested := filepath.Join(repo, "tmp_repo")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, nested, "init", "-q")
	git(t, nested, "commit", "-q", "--allow-empty", "-m", "x")

	set := build(t, Inputs{Session: sess})
	tiers := map[string]string{}
	for _, f := range set.Files {
		tiers[f.Display] = f.Tier
	}
	if tiers["tmp_link.txt"] != contracts.TierScratch || tiers["tmp_repo"] != contracts.TierScratch {
		t.Fatalf("tiers = %v, want the symlink and nested repo listed as scratch", tiers)
	}

	res, deleted := DeleteScratch(set, []string{scratch, scratch, filepath.Join(repo, "keep.go"), link, nested, "/etc/hosts"})
	if !reflect.DeepEqual(res.Deleted, []string{scratch}) || len(deleted) != 1 {
		t.Errorf("deleted = %v", res.Deleted)
	}
	reasons := map[string]string{}
	for _, s := range res.Skipped {
		reasons[s.Path] = s.Reason
	}
	want := map[string]string{
		filepath.Join(repo, "keep.go"): SkipNotScratch,
		link:                           SkipNotRegular,
		nested:                         SkipNotRegular,
		"/etc/hosts":                   SkipNotInChanges,
	}
	if !reflect.DeepEqual(reasons, want) {
		t.Errorf("skipped = %v, want %v", reasons, want)
	}
	if _, err := os.Lstat(scratch); !errors.Is(err, os.ErrNotExist) {
		t.Error("scratch file still there")
	}
	for _, p := range []string{link, nested, filepath.Join(repo, "keep.go")} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
	// A second delete of the same set: already gone.
	res, _ = DeleteScratch(set, []string{scratch})
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != SkipGone {
		t.Errorf("second delete = %+v, want already gone", res)
	}
}

// Relative ledger paths resolve against the row's Repo (the runner's cwd).
func TestLedgerPathRelative(t *testing.T) {
	dir := tempDir(t)
	got := LedgerPath(contracts.FileChange{Path: "a/b.txt", Repo: dir}, "/elsewhere")
	if got != filepath.Join(dir, "a", "b.txt") {
		t.Errorf("LedgerPath = %q", got)
	}
	// No Repo: anchored on the session folder, never this process's cwd.
	if got := LedgerPath(contracts.FileChange{Path: "a/b.txt"}, dir); got != filepath.Join(dir, "a", "b.txt") {
		t.Errorf("LedgerPath(no repo) = %q", got)
	}
	if got := LedgerPath(contracts.FileChange{Path: "a/b.txt"}, ""); got != "" {
		t.Errorf("LedgerPath(no anchor) = %q, want \"\"", got)
	}
}

func keys(m map[string]contracts.ChangedFile) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBuildNoSnapshotAnchorsOnFirstCommit: a session with no shadow store
// (pre-shadow, imported, shadowing off) still lists what it committed —
// against the commit before its first recorded commit — while the user's
// pre-existing untracked files stay out unless the ledger says the session
// made them.
func TestBuildNoSnapshotAnchorsOnFirstCommit(t *testing.T) {
	repo := newRepo(t, map[string]string{"keep.go": "package keep\n", "edit.go": "a\n"})
	writeFile(t, filepath.Join(repo, "junk.txt"), "user's own untracked file\n")
	writeFile(t, filepath.Join(repo, "src/commit-file.ts"), "export const x = 1;\n")
	writeFile(t, filepath.Join(repo, "edit.go"), "a\nb\n")
	git(t, repo, "add", "src/commit-file.ts", "edit.go")
	git(t, repo, "commit", "-q", "-m", "session work")
	hash := git(t, repo, "rev-parse", "HEAD")
	made := filepath.Join(repo, "made.go")
	writeFile(t, made, "package made\n") // untracked, but the ledger says the session made it

	sess := contracts.Session{ID: "sess", Dir: repo} // no ShadowDir
	set := build(t, Inputs{
		Session: sess,
		Commits: []contracts.Commit{{Hash: hash, Message: "session work"}},
		Ledger:  []contracts.FileChange{ledgerRow(made, "added", time.Now())},
	})
	got := byDisplay(set)
	if f := got["src/commit-file.ts"]; f.Change != "added" || f.Added != 1 || f.Uncommitted || len(f.Commits) != 1 {
		t.Errorf("committed file = %+v", f)
	}
	if f := got["edit.go"]; f.Change != "modified" || f.Added != 1 {
		t.Errorf("committed edit = %+v", f)
	}
	if f := got["made.go"]; f.Change != "added" {
		t.Errorf("ledger-created untracked file = %+v", f)
	}
	for _, absent := range []string{"junk.txt", "keep.go"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s listed; want absent", absent)
		}
	}
	if len(set.Notes) != 1 || !strings.Contains(set.Notes[0], "commit before the session's first commit") {
		t.Errorf("notes = %q", set.Notes)
	}

	// The session's first commit is the repo's root commit: compare
	// against the empty tree rather than dropping the anchor.
	root := tempDir(t)
	git(t, root, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(root, "first.go"), "package first\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "first")
	rootHash := git(t, root, "rev-parse", "HEAD")
	set = build(t, Inputs{Session: contracts.Session{ID: "s2", Dir: root}, Commits: []contracts.Commit{{Hash: rootHash}}})
	if f := byDisplay(set)["first.go"]; f.Change != "added" {
		t.Errorf("root-commit file = %+v (files %v)", f, keys(byDisplay(set)))
	}
}

// TestBuildCommitRepoIsARoot: a worktree session whose recorded commit
// landed in the primary checkout — no ledger row or capture points there —
// still lists the commit's files, and a relative ledger path with no Repo
// resolves under the session folder rather than this process's cwd.
func TestBuildCommitRepoIsARoot(t *testing.T) {
	primary := newRepo(t, map[string]string{"a.go": "package a\n"})
	wt := filepath.Join(tempDir(t), "wt")
	git(t, primary, "worktree", "add", "-q", "-b", "mads/session/s", wt)
	sess, _ := shadowFor(t, wt)
	sess.Dir, sess.WorktreeDir = primary, wt

	writeFile(t, filepath.Join(primary, "src/commit-file.ts"), "export const x = 1;\n")
	git(t, primary, "add", "-A")
	git(t, primary, "commit", "-q", "-m", "seed a commit")
	hash := git(t, primary, "rev-parse", "HEAD")

	set := build(t, Inputs{
		Session: sess,
		Commits: []contracts.Commit{{Hash: hash, Repo: primary}},
		Ledger:  []contracts.FileChange{ledgerRow("src/example.ts", "added", time.Now())}, // relative, no Repo, not on disk
	})
	var found bool
	for _, f := range set.Files {
		if f.Path == filepath.Join(primary, "src", "commit-file.ts") {
			found = f.Change == "added" && len(f.Commits) == 1 && f.Tier != contracts.TierScratch
		}
		if !strings.HasPrefix(f.Path, primary) && !strings.HasPrefix(f.Path, wt) {
			t.Errorf("file outside the session's repos: %s", f.Path)
		}
	}
	if !found {
		t.Errorf("primary-checkout commit file not listed; files %+v notes %q", set.Files, set.Notes)
	}
}
