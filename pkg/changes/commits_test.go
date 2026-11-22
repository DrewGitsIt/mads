package changes

import (
	"context"
	"crypto/sha1" //nolint:gosec // git's object names, not security
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// commitsFixture is a primary checkout and a session worktree off it with
// the histories commits() has to sort out: many commits on the session
// branch (renames, deletions, odd file names, an empty commit), a clean
// merge and an evil merge into it, a commit amended away, one landed on
// the primary's main, one ahead of HEAD, an annotated tag's hash, hashes
// that are abbreviated, ambiguous, repeated, missing or not hashes at
// all, and one recorded in a repo deleted since. Recorded
// oldest first, as the ledger holds them.
func commitsFixture(t *testing.T, many int) (primary, wt string, recorded []contracts.Commit) {
	t.Helper()
	primary = newRepo(t, map[string]string{"seed.txt": "seed\n", "gone.txt": "bye\n", "old name.txt": "x\n"})
	wt = filepath.Join(tempDir(t), "wt")
	git(t, primary, "worktree", "add", "-q", "-b", "mads/session/s", wt)
	rec := func(dir, msg string) string {
		h := git(t, dir, "rev-parse", "HEAD")
		recorded = append(recorded, contracts.Commit{Hash: h, Repo: dir, Message: msg + "\n\nbody"})
		return h
	}
	commit := func(dir, msg string, files map[string]string) string {
		for rel, content := range files {
			writeFile(t, filepath.Join(dir, rel), content)
		}
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
		return rec(dir, msg)
	}
	for i := range many {
		commit(wt, fmt.Sprintf("work %d", i), map[string]string{
			fmt.Sprintf("d%02d/f.txt", i%7):    fmt.Sprintf("v%d\n", i),
			fmt.Sprintf("n%02d ünï ç.md", i%3): fmt.Sprintf("%d\n", i),
		})
	}
	git(t, wt, "mv", "old name.txt", "new name.txt")
	git(t, wt, "rm", "-q", "gone.txt")
	git(t, wt, "commit", "-q", "-m", "rename and delete")
	rec(wt, "rename and delete")
	commit(wt, "empty", nil)

	// A side branch merged in cleanly, and one merged with an extra edit.
	git(t, wt, "checkout", "-q", "-b", "side", "HEAD~3")
	side := commit(wt, "side work", map[string]string{"side.txt": "side\n"})
	git(t, wt, "checkout", "-q", "mads/session/s")
	git(t, wt, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "-q", "--no-ff", "-m", "merge side", "side")
	rec(wt, "merge side")
	git(t, wt, "checkout", "-q", "-b", "side2", "HEAD~2")
	commit(wt, "side2 work", map[string]string{"side2.txt": "s2\n"})
	git(t, wt, "checkout", "-q", "mads/session/s")
	git(t, wt, "-c", "user.email=t@t", "-c", "user.name=t", "merge", "-q", "--no-ff", "--no-commit", "side2")
	writeFile(t, filepath.Join(wt, "seed.txt"), "evil\n")
	git(t, wt, "add", "-A")
	git(t, wt, "commit", "-q", "-m", "evil merge")
	rec(wt, "evil merge")

	// Amended away: the object stays, no branch reaches it.
	amended := commit(wt, "to amend", map[string]string{"amend.txt": "1\n"})
	writeFile(t, filepath.Join(wt, "amend.txt"), "2\n")
	git(t, wt, "add", "-A")
	git(t, wt, "commit", "-q", "--amend", "-m", "amended")
	rec(wt, "amended")

	// A commit ahead of the worktree's HEAD (a descendant it doesn't reach).
	git(t, wt, "checkout", "-q", "-b", "ahead")
	commit(wt, "ahead of HEAD", map[string]string{"ahead.txt": "a\n"})
	git(t, wt, "checkout", "-q", "mads/session/s")

	// An annotated tag's own hash, recorded in place of its commit's.
	git(t, wt, "tag", "-a", "t1", "-m", "a tag", side)
	tag := git(t, wt, "rev-parse", "t1")

	// Landed in the primary checkout, and one the primary has with no Repo.
	commit(primary, "on main", map[string]string{"main.txt": "m\n"})
	recorded[len(recorded)-1].Repo = ""
	commit(primary, "on main, recorded there", map[string]string{"main2.txt": "m2\n"})

	recorded = append(recorded,
		contracts.Commit{Hash: tag, Message: "annotated tag"},
		contracts.Commit{Hash: ambiguousPrefix(t, wt), Message: "ambiguous"},
		contracts.Commit{Hash: amended[:10], Message: "abbreviated"},
		contracts.Commit{Hash: side, Message: "again"},
		contracts.Commit{Hash: side, Repo: filepath.Join(tempDir(t), "deleted since"), Message: "its repo gone"},
		contracts.Commit{Hash: strings.Repeat("0123456789", 4), Message: "missing"},
		contracts.Commit{Hash: "not-a-hash", Message: "junk"},
		contracts.Commit{Hash: "", Message: "none"},
	)
	return primary, wt, recorded
}

// ambiguousPrefix writes two blobs into repo whose names share their first
// 7 hex digits and returns that prefix: a short hash git can't resolve.
func ambiguousPrefix(t *testing.T, repo string) string {
	t.Helper()
	byPrefix := map[string]string{}
	for i := 0; ; i++ {
		content := fmt.Sprintf("blob %d\n", i)
		sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
		prefix := hex.EncodeToString(sum[:])[:7]
		if other, ok := byPrefix[prefix]; ok {
			for _, c := range []string{other, content} {
				cmd := exec.Command("git", "hash-object", "-w", "--stdin")
				cmd.Dir, cmd.Stdin = repo, strings.NewReader(c)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("hash-object: %v\n%s", err, out)
				}
			}
			return prefix
		}
		byPrefix[prefix] = content
	}
}

// resetBatchCounters zeroes the fallback counters for a test.
func resetBatchCounters(t *testing.T) {
	t.Helper()
	batchFallbacks.Store(0)
	perHashAncestry.Store(0)
}

func commitsBuilder(sess contracts.Session, commits []contracts.Commit, roots ...string) *builder {
	b := &builder{
		in:        Inputs{Session: sess, Commits: commits},
		folder:    SessionFolder(sess),
		rootCache: map[string]string{},
		repoRoots: map[string]bool{},
	}
	for _, r := range roots {
		b.repoRoots[r] = true
	}
	return b
}

// The batched commits() lists the same commits, anchored in the same
// repos, with the same files in the same order, as one git call per
// commit and question did.
func TestCommitsBatchedMatchesPerCommit(t *testing.T) {
	primary, wt, recorded := commitsFixture(t, 40)
	sess := contracts.Session{ID: "s", Dir: primary, WorktreeDir: wt}
	ctx := context.Background()
	// Seen as a root, the primary anchors the commit recorded with no
	// Repo that only its HEAD reaches; otherwise the worktree, which has it.
	for _, tc := range []struct {
		name      string
		roots     []string
		inPrimary int
	}{{"worktree", nil, 1}, {"worktree and primary", []string{primary, wt}, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			b := commitsBuilder(sess, recorded, tc.roots...)
			resetBatchCounters(t)
			got, gotBy := b.commits(ctx)
			if n, m := batchFallbacks.Load(), perHashAncestry.Load(); n != 0 || m != 0 {
				t.Fatalf("batched path fell back (%d to commitsEach, %d per-hash ancestry): the comparison proves nothing", n, m)
			}
			want, wantBy := b.commitsEach(ctx)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("commits differ:\n got %+v\nwant %+v", got, want)
			}
			if !reflect.DeepEqual(gotBy, wantBy) {
				t.Errorf("by path differ:\n got %v\nwant %v", gotBy, wantBy)
			}
			if len(want) < 45 {
				t.Fatalf("only %d commits listed; the fixture should list them all", len(want))
			}
			var anchoredPrimary, merges int
			for _, c := range want {
				if c.Message == "annotated tag" && (len(c.Paths) != 1 || filepath.Base(c.Paths[0]) != "side.txt") {
					t.Errorf("annotated tag lists %q, want its commit's side.txt", c.Paths)
				}
				if len(c.Paths) > 0 && strings.HasPrefix(c.Paths[0], primary+string(filepath.Separator)) {
					anchoredPrimary++
				}
				if strings.HasPrefix(c.Message, "evil merge") && len(c.Paths) == 1 {
					merges++
				}
			}
			if anchoredPrimary != tc.inPrimary || merges != 1 {
				t.Errorf("fixture lost its point: %d commits anchored in the primary (want %d), evil merge listed %d (want 1)", anchoredPrimary, tc.inPrimary, merges)
			}
		})
	}
}

// A recorded commit from an unrelated history (no merge base with HEAD)
// takes the per-hash ancestry path, with the same answer.
func TestCommitsBatchedUnrelatedHistory(t *testing.T) {
	primary, wt, recorded := commitsFixture(t, 3)
	git(t, wt, "checkout", "-q", "--orphan", "lonely")
	git(t, wt, "rm", "-rq", "--cached", ".")
	writeFile(t, filepath.Join(wt, "lonely.txt"), "l\n")
	git(t, wt, "add", "lonely.txt")
	git(t, wt, "commit", "-q", "-m", "lonely")
	lonely := git(t, wt, "rev-parse", "HEAD")
	git(t, wt, "checkout", "-q", "-f", "mads/session/s")
	recorded = append(recorded, contracts.Commit{Hash: lonely, Message: "lonely"})
	b := commitsBuilder(contracts.Session{ID: "s", Dir: primary, WorktreeDir: wt}, recorded)
	ctx := context.Background()
	got, gotBy, err := b.commitsBatched(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, wantBy := b.commitsEach(ctx)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotBy, wantBy) {
		t.Errorf("differ:\n got %+v\nwant %+v", got, want)
	}
}

// countGit puts a git on PATH that counts its runs.
func countGit(t *testing.T) func() []string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "spawns.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		raw, _ := os.ReadFile(log)
		if s := strings.TrimSpace(string(raw)); s != "" {
			return strings.Split(s, "\n")
		}
		return nil
	}
}

// The commits of a change set cost a few git runs per repo, not three per
// commit (perf harness 9c: 40 recorded commits were ~120 runs, most of a
// second, on every change-set compute).
func TestCommitsGitRunsDoNotScaleWithCommits(t *testing.T) {
	primary, wt, recorded := commitsFixture(t, 40)
	b := commitsBuilder(contracts.Session{ID: "s", Dir: primary, WorktreeDir: wt}, recorded)
	spawns := countGit(t)
	ctx := context.Background()
	if _, _, err := b.commitsBatched(ctx); err != nil {
		t.Fatal(err)
	}
	batched := spawns()
	b.commitsEach(ctx)
	each := spawns()[len(batched):]
	// The worktree, then the primary for the commits recorded there that
	// the worktree's HEAD doesn't reach: cat-file, merge-base and rev-list
	// in each, a show in the one anchoring commits, and the deleted repo's
	// failed cat-file.
	if len(batched) > 8 {
		t.Errorf("batched: %d git runs, want at most 8:\n%s", len(batched), strings.Join(batched, "\n"))
	}
	if len(each) < 3*40 {
		t.Errorf("per commit: %d git runs; the fixture should cost the old way 3 per commit", len(each))
	}
	t.Logf("git runs for %d recorded commits: per commit %d, batched %d", len(recorded), len(each), len(batched))
}

// A criss-cross history: HEAD and a recorded commit have two merge bases,
// so the octopus answer is a set and rev-list excludes them all.
func TestCommitsBatchedCrissCross(t *testing.T) {
	repo := newRepo(t, map[string]string{"a.txt": "a\n"})
	commitOn := func(branch, file string) string {
		git(t, repo, "checkout", "-q", branch)
		writeFile(t, filepath.Join(repo, file), file+"\n")
		git(t, repo, "add", "-A")
		git(t, repo, "commit", "-q", "-m", file)
		return git(t, repo, "rev-parse", "HEAD")
	}
	git(t, repo, "branch", "x1")
	git(t, repo, "branch", "x2")
	c1 := commitOn("x1", "c1.txt")
	c2 := commitOn("x2", "c2.txt")
	git(t, repo, "checkout", "-q", "x1")
	git(t, repo, "merge", "-q", "--no-ff", "-m", "m1", c2)
	m1 := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "checkout", "-q", "x2")
	git(t, repo, "merge", "-q", "--no-ff", "-m", "m2", c1)
	m2 := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "checkout", "-q", "x1") // HEAD = m1
	if bases := strings.Fields(git(t, repo, "merge-base", "--all", m1, m2)); len(bases) != 2 {
		t.Fatalf("merge bases of m1 and m2 = %v, want two (criss-cross)", bases)
	}
	sess := contracts.Session{ID: "s", Dir: repo}
	ctx := context.Background()
	for _, hashes := range [][]string{{m2}, {c1, c2, m1, m2}} {
		var recorded []contracts.Commit
		for _, h := range hashes {
			recorded = append(recorded, contracts.Commit{Hash: h, Repo: repo, Message: h[:7]})
		}
		b := commitsBuilder(sess, recorded)
		resetBatchCounters(t)
		got, gotBy := b.commits(ctx)
		if n, m := batchFallbacks.Load(), perHashAncestry.Load(); n != 0 || m != 0 {
			t.Fatalf("%d recorded: batched path fell back (%d, %d)", len(hashes), n, m)
		}
		want, wantBy := b.commitsEach(ctx)
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotBy, wantBy) {
			t.Errorf("%d recorded: differ:\n got %+v\nwant %+v", len(hashes), got, want)
		}
	}
}

// A walk longer than revListCap is asked hash by hash, with the same answer.
func TestCommitsBatchedRevListCap(t *testing.T) {
	primary, wt, recorded := commitsFixture(t, 10)
	b := commitsBuilder(contracts.Session{ID: "s", Dir: primary, WorktreeDir: wt}, recorded)
	ctx := context.Background()
	old := revListCap
	revListCap = 2
	t.Cleanup(func() { revListCap = old })
	resetBatchCounters(t)
	got, gotBy := b.commits(ctx)
	if batchFallbacks.Load() != 0 || perHashAncestry.Load() == 0 {
		t.Fatalf("fallbacks %d, per-hash ancestry %d: want the capped walk asked hash by hash", batchFallbacks.Load(), perHashAncestry.Load())
	}
	want, wantBy := b.commitsEach(ctx)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotBy, wantBy) {
		t.Errorf("differ:\n got %+v\nwant %+v", got, want)
	}
}

// cat-file failing in a repo that is there (a timeout, say) isn't "it has
// none of them": commits() falls back to commitsEach, and says the same.
func TestCommitsCatFileFailureFallsBack(t *testing.T) {
	primary, wt, recorded := commitsFixture(t, 5)
	b := commitsBuilder(contracts.Session{ID: "s", Dir: primary, WorktreeDir: wt}, recorded)
	ctx := context.Background()
	want, wantBy := b.commitsEach(ctx)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = cat-file ] && exit 128; done\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	resetBatchCounters(t)
	got, gotBy := b.commits(ctx)
	if batchFallbacks.Load() != 1 {
		t.Fatalf("fallbacks = %d, want 1", batchFallbacks.Load())
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotBy, wantBy) {
		t.Errorf("differ:\n got %+v\nwant %+v", got, want)
	}
	if _, _, err := b.commitsBatched(ctx); err == nil {
		t.Error("commitsBatched with cat-file failing in a live repo: nil error, want one")
	}
}
