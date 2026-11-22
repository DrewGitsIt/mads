package changes

import (
	"context"
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
// the primary's main, hashes that are abbreviated, repeated, missing or
// not hashes at all, and one recorded in a repo deleted since. Recorded
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

	// Landed in the primary checkout, and one the primary has with no Repo.
	commit(primary, "on main", map[string]string{"main.txt": "m\n"})
	recorded[len(recorded)-1].Repo = ""
	commit(primary, "on main, recorded there", map[string]string{"main2.txt": "m2\n"})

	recorded = append(recorded,
		contracts.Commit{Hash: amended[:10], Message: "abbreviated"},
		contracts.Commit{Hash: side, Message: "again"},
		contracts.Commit{Hash: side, Repo: filepath.Join(tempDir(t), "deleted since"), Message: "its repo gone"},
		contracts.Commit{Hash: strings.Repeat("0123456789", 4), Message: "missing"},
		contracts.Commit{Hash: "not-a-hash", Message: "junk"},
		contracts.Commit{Hash: "", Message: "none"},
	)
	return primary, wt, recorded
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
			got, gotBy, err := b.commitsBatched(ctx)
			if err != nil {
				t.Fatal(err)
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
