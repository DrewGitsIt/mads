package sessionwt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/gitroot"
)

// gitToplevel is what git itself says, "" for no work tree.
func gitToplevel(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// toplevelLayout is a temp tree with every shape Toplevel meets: a repo
// with a nested dir, a linked worktree, a submodule, a bare repo, a
// symlink into the repo, a nested repo and a plain folder. The temp root
// itself is left unresolved (on macOS it sits behind /var -> /private/var),
// so every case also crosses a symlink.
func toplevelLayout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	mkdirs(t, filepath.Join(repo, "src", "deep"), filepath.Join(root, "plain", "sub"))
	mustGit(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", ".")
	mustGit(t, repo, "commit", "-q", "-m", "init")
	mustGit(t, repo, "worktree", "add", "-q", "-b", "feature", filepath.Join(root, "wt"))
	mkdirs(t, filepath.Join(root, "wt", "pkg"))

	sub := filepath.Join(root, "subsrc")
	mkdirs(t, sub)
	mustGit(t, sub, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(sub, "s.txt"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, sub, "add", ".")
	mustGit(t, sub, "commit", "-q", "-m", "sub")
	mustGit(t, repo, "submodule", "add", "-q", sub, "vendor/sub")
	mkdirs(t, filepath.Join(repo, "vendor", "sub", "inner"))

	mustGit(t, root, "init", "-q", "--bare", filepath.Join(root, "repo", "bare.git"))
	nested := filepath.Join(repo, "src", "nested")
	mkdirs(t, nested)
	mustGit(t, nested, "init", "-q")
	if err := os.Symlink(filepath.Join(repo, "src"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

// Toplevel finds the root without git wherever it can, and its answer is
// always git's own: a normal repo, a linked worktree, a submodule, a
// nested repo, a symlinked path, a deleted directory's nearest ancestor,
// outside every repo. A bare repo and a .git dir fall back to git.
func TestToplevelMatchesGit(t *testing.T) {
	ctx := context.Background()
	root := toplevelLayout(t)
	cases := []struct {
		name string
		dir  string
		// walked: the answer comes from the walk, no git spawned.
		walked bool
	}{
		{"repo top", "repo", true},
		{"repo subdir", "repo/src/deep", true},
		{"linked worktree", "wt/pkg", true},
		{"submodule", "repo/vendor/sub/inner", true},
		{"nested repo", "repo/src/nested", true},
		{"symlink into the repo", "link/deep", true},
		{"outside every repo", "plain/sub", true},
		{"inside a bare repo", "repo/bare.git/refs", false},
		{"inside .git", "repo/.git/refs", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(root, tc.dir)
			want := gitToplevel(t, dir)
			got, err := Toplevel(ctx, dir)
			if want == "" {
				if err == nil {
					t.Fatalf("Toplevel(%s) = %q, want an error like git's", tc.dir, got)
				}
			} else if err != nil || got != want {
				t.Fatalf("Toplevel(%s) = %q, %v; git says %q", tc.dir, got, err, want)
			}
			_, walked, _ := walkToplevel(dir)
			if walked != tc.walked {
				t.Errorf("walked = %v, want %v", walked, tc.walked)
			}
		})
	}

	// A deleted directory: toplevelOf walks up to the nearest existing
	// ancestor, as it did with git.
	gone := filepath.Join(root, "repo", "src", "gone", "deeper")
	if got, want := toplevelOf(ctx, gone, map[string]string{}), gitToplevel(t, filepath.Join(root, "repo", "src")); got != want {
		t.Errorf("deleted dir: toplevelOf = %q, want %q", got, want)
	}
	if got := toplevelOf(ctx, filepath.Join(root, "plain", "gone"), map[string]string{}); got != "" {
		t.Errorf("deleted dir outside every repo: toplevelOf = %q, want \"\"", got)
	}
}

// An unusual layout the walk can't vouch for goes to git: a .git file that
// isn't a gitdir pointer (git refuses it), a pruned linked worktree.
func TestToplevelFallsBackToGitWhenUnsure(t *testing.T) {
	ctx := context.Background()
	root := toplevelLayout(t)
	bogus := filepath.Join(root, "plain", "bogus")
	mkdirs(t, bogus)
	if err := os.WriteFile(filepath.Join(bogus, ".git"), []byte("not a pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, sure := gitroot.Find(bogus); sure {
		t.Error("a bad .git file: walk claims to be sure")
	}
	if _, err := Toplevel(ctx, bogus); err == nil && gitToplevel(t, bogus) == "" {
		t.Error("a bad .git file: Toplevel answered where git refuses")
	}

	mustGit(t, filepath.Join(root, "repo"), "worktree", "add", "-q", "-b", "pruned", filepath.Join(root, "wt2"))
	if err := os.RemoveAll(filepath.Join(root, "repo", ".git", "worktrees", "wt2")); err != nil {
		t.Fatal(err)
	}
	wt2 := filepath.Join(root, "wt2")
	if _, sure := gitroot.Find(wt2); sure {
		t.Error("pruned worktree: walk claims to be sure")
	}
	got, err := Toplevel(ctx, wt2)
	if want := gitToplevel(t, wt2); (want == "") != (err != nil) || (err == nil && got != want) {
		t.Errorf("pruned worktree: Toplevel = %q, %v; git says %q", got, err, want)
	}

	t.Setenv("GIT_DIR", filepath.Join(root, "repo", ".git"))
	if _, sure := gitroot.Find(filepath.Join(root, "plain")); sure {
		t.Error("GIT_DIR set: walk claims to be sure")
	}
}

// spawnCounter puts a git on PATH that logs each run and then runs the real
// one, so a test can count git processes from outside.
func spawnCounter(t *testing.T) func() int {
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
	return func() int {
		raw, err := os.ReadFile(log)
		if err != nil {
			return 0
		}
		return strings.Count(string(raw), "\n")
	}
}

// RepoTargets (GET /sessions/{id}/repos, every recount, the Router's
// active-session sweep) found each ledger directory's repo with one
// `git rev-parse --show-toplevel`. Now it finds them without git at all.
func TestRepoTargetsSpawnsNoGitForRoots(t *testing.T) {
	root := toplevelLayout(t)
	repo := filepath.Join(root, "repo")
	var rows []contracts.FileChange
	// 40 distinct directories across the main repo, the worktree, the
	// submodule, a deleted dir and a folder in no repo.
	for i := range 10 {
		for _, d := range []string{"repo/src/deep", "wt/pkg", "repo/vendor/sub/inner", "repo/gone", "plain/sub"} {
			rows = append(rows, contracts.FileChange{
				Path:   filepath.Join(root, d, "d"+string(rune('a'+i)), "f.go"),
				Origin: contracts.OriginToolUse,
			})
		}
	}
	primary := &Target{Folder: repo, Primary: repo}
	count := spawnCounter(t)

	// The old cost, for comparison: with discovery env set the walk defers
	// to git (a ceiling that changes nothing here), one spawn per directory.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Join(root, "no-such-ceiling"))
	before := count()
	old := RepoTargets(context.Background(), contracts.Session{ID: "s"}, primary, rows)
	if spawns := count() - before; spawns != len(rows) {
		t.Errorf("via git: %d spawns for %d directories, want one each", spawns, len(rows))
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", "")

	before = count()
	got := RepoTargets(context.Background(), contracts.Session{ID: "s"}, primary, rows)
	if spawns := count() - before; spawns != 0 {
		t.Errorf("RepoTargets spawned %d git processes for %d directories, want 0", spawns, len(rows))
	}
	if len(old) != len(got) {
		t.Errorf("walk found %d targets, git %d", len(got), len(old))
	}
	// The primary, plus the worktree (outside the primary's tree); the
	// submodule nests inside the primary, so it's left out as before.
	var folders []string
	for _, tg := range got {
		folders = append(folders, tg.Folder)
	}
	wantWT := gitToplevel(t, filepath.Join(root, "wt"))
	if len(got) != 2 || got[0].Folder != repo || got[1].Folder != wantWT {
		t.Errorf("targets = %v, want [%s %s]", folders, repo, wantWT)
	}
}
