package mergedriver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// isolate gives the test a HOME of its own, so the developer's global git
// config and attributes can't change what these scratch repos do.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv(EnvPath, "")
	return home
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main", ".")
	gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func exe(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mergiraf")
	write(t, p, "#!/bin/sh\necho 'mergiraf 9.9.9'\n")
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// standIn is a driver with mergiraf's calling convention (`merge --git %O
// %A %B ...`) that resolves every conflict by keeping both sides' lines.
func standIn(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mergiraf")
	write(t, p, "#!/bin/sh\nexec git merge-file --union \"$4\" \"$3\" \"$5\"\n")
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// realMergiraf is $MADS_TEST_MERGIRAF, else mergiraf on PATH, else a skip.
func realMergiraf(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MADS_TEST_MERGIRAF"); p != "" {
		if _, err := exec.LookPath(p); err != nil {
			t.Fatalf("MADS_TEST_MERGIRAF=%s: %v", p, err)
		}
		return p
	}
	p, err := exec.LookPath("mergiraf")
	if err != nil {
		t.Skip("mergiraf not installed (set MADS_TEST_MERGIRAF or put it on PATH); skipping")
	}
	return p
}

// repoState is everything a merge-driver registration could have written:
// the repo config and the shared attributes file.
func repoState(t *testing.T, repo string) string {
	t.Helper()
	return read(t, filepath.Join(repo, ".git", "config")) + "\x00" + read(t, filepath.Join(repo, ".git", "info", "attributes"))
}

// legacyRegister writes what earlier builds' Register did.
func legacyRegister(t *testing.T, dir, path string) {
	t.Helper()
	gitRun(t, dir, "config", "merge.mergiraf.name", "mergiraf")
	gitRun(t, dir, "config", "merge.mergiraf.driver", path+commandArgs)
	common := strings.TrimSpace(gitRun(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	attr := filepath.Join(common, "info", "attributes")
	write(t, attr, read(t, attr)+"* merge=mergiraf\n")
}

const (
	goBase   = "package sample\n\nfunc Sample() {\n\tx := 1\n\ty := 2\n\t_ = x\n\t_ = y\n}\n"
	goOurs   = "package sample\n\nfunc Sample() {\n\tx := 1\n\ty := 200\n\t_ = x\n\t_ = y\n}\n"
	goTheirs = "package sample\n\nfunc Sample() {\n\tx := 100\n\ty := 2\n\t_ = x\n\t_ = y\n}\n"
	goMerged = "package sample\n\nfunc Sample() {\n\tx := 100\n\ty := 200\n\t_ = x\n\t_ = y\n}\n"
)

// adjacent sets up branch "theirs" and main each changing an adjacent
// line of each named file — plain git conflicts on every one — and leaves
// main checked out.
func adjacent(t *testing.T, repo string, files ...string) {
	t.Helper()
	for _, f := range files {
		write(t, filepath.Join(repo, f), goBase)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-q", "-m", "base")
	gitRun(t, repo, "checkout", "-q", "-b", "theirs")
	for _, f := range files {
		write(t, filepath.Join(repo, f), goTheirs)
	}
	gitRun(t, repo, "commit", "-q", "-am", "theirs")
	gitRun(t, repo, "checkout", "-q", "main")
	for _, f := range files {
		write(t, filepath.Join(repo, f), goOurs)
	}
	gitRun(t, repo, "commit", "-q", "-am", "ours")
}

func TestFind(t *testing.T) {
	isolate(t)
	if got := Find(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("Find(missing) = %q, want \"\" (no fallback to PATH)", got)
	}
	p := exe(t)
	if got := Find(p); got != p {
		t.Errorf("Find(%s) = %q", p, got)
	}
	t.Setenv("PATH", filepath.Dir(p))
	if got := Find(""); got != p {
		t.Errorf("Find(\"\") on PATH = %q, want %q", got, p)
	}
	other := exe(t)
	t.Setenv(EnvPath, other)
	if got := Find(""); got != other {
		t.Errorf("Find(\"\") with $%s = %q, want %q", EnvPath, got, other)
	}
	t.Setenv(EnvPath, filepath.Join(t.TempDir(), "missing"))
	if got := Find(""); got != "" {
		t.Errorf("Find(\"\") with an unusable $%s = %q, want \"\" (plain git)", EnvPath, got)
	}
}

func TestCommandQuotesOddPaths(t *testing.T) {
	if got := Command("/usr/bin/mergiraf"); got != "/usr/bin/mergiraf"+commandArgs {
		t.Errorf("Command = %q", got)
	}
	if got := Command("/Applications/My Tools/it's/mergiraf"); got != `'/Applications/My Tools/it'\''s/mergiraf'`+commandArgs {
		t.Errorf("Command = %q", got)
	}
}

// The real binary, through Args only: the merge mads runs resolves the
// adjacent edits, the repo's config and attributes are untouched, and the
// user's own plain `git merge` of the same branches still conflicts.
func TestArgsRouteOneMergeThroughRealMergiraf(t *testing.T) {
	isolate(t)
	path := realMergiraf(t)
	repo := initRepo(t)
	adjacent(t, repo, "sample.go")
	before := repoState(t, repo)

	args, err := Args(context.Background(), repo, t.TempDir(), path)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := gitTry(repo, append(args, "merge", "-q", "--no-edit", "theirs")...); err != nil {
		t.Fatalf("merge through mergiraf: %v\n%s", err, out)
	}
	if got := read(t, filepath.Join(repo, "sample.go")); got != goMerged {
		t.Errorf("sample.go = %q, want %q", got, goMerged)
	}
	if repoState(t, repo) != before {
		t.Errorf("Args wrote to the repo:\n%q\nwas\n%q", repoState(t, repo), before)
	}

	gitRun(t, repo, "reset", "-q", "--hard", "HEAD^")
	if _, err := gitTry(repo, "merge", "-q", "--no-edit", "theirs"); err == nil {
		t.Fatal("the user's own git merge came back clean: it went through mergiraf")
	}
	if !strings.Contains(read(t, filepath.Join(repo, "sample.go")), "<<<<<<<") {
		t.Error("the user's own git merge left no conflict markers")
	}
}

// Same with the stand-in, so it runs everywhere; and from a linked
// worktree, where mads's task and session merges run.
func TestArgsRouteOneMergeOnly(t *testing.T) {
	isolate(t)
	repo := initRepo(t)
	adjacent(t, repo, "a.txt")
	linked := filepath.Join(t.TempDir(), "linked")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "work", linked, "main")
	before := repoState(t, repo)

	args, err := Args(context.Background(), linked, t.TempDir(), standIn(t))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := gitTry(linked, append(args, "merge", "-q", "--no-edit", "theirs")...); err != nil {
		t.Fatalf("merge through the driver: %v\n%s", err, out)
	}
	if repoState(t, repo) != before {
		t.Error("Args wrote to the repo")
	}
	if _, err := gitTry(repo, "merge", "-q", "--no-edit", "theirs"); err == nil {
		t.Error("a plain git merge in the main checkout went through the driver")
	}
}

// A merge= the repository sets itself wins over mads's rule (git reads
// core.attributesFile last), and so does one in the user's global
// attributes, which Args copies after its rule.
func TestArgsYieldToRepoAndUserAttributes(t *testing.T) {
	home := isolate(t)
	repo := initRepo(t)
	write(t, filepath.Join(home, ".config", "git", "attributes"), "user.txt merge=binary\n")
	write(t, filepath.Join(repo, ".git", "info", "attributes"), "repo.txt merge=binary\n")
	adjacent(t, repo, "user.txt", "repo.txt", "free.txt")

	args, err := Args(context.Background(), repo, t.TempDir(), standIn(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitTry(repo, append(args, "merge", "-q", "--no-edit", "theirs")...); err == nil {
		t.Fatal("merge came back clean: the repo's and user's merge=binary were overridden")
	}
	conflicted := strings.Fields(gitRun(t, repo, "diff", "--name-only", "--diff-filter=U"))
	if !slices.Equal(conflicted, []string{"repo.txt", "user.txt"}) {
		t.Errorf("conflicted = %v, want [repo.txt user.txt] (free.txt through the driver)", conflicted)
	}
}

// One attributes file per content, written once; a second call reads it.
func TestArgsAttributesFileIsStable(t *testing.T) {
	isolate(t)
	repo := initRepo(t)
	state := t.TempDir()
	a1, err := Args(context.Background(), repo, state, "/bin/mergiraf")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := Args(context.Background(), repo, state, "/bin/mergiraf")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a1, a2) {
		t.Errorf("args differ: %v vs %v", a1, a2)
	}
	entries, _ := os.ReadDir(state)
	if len(entries) != 1 {
		t.Fatalf("state dir holds %d files, want 1", len(entries))
	}
	if got := read(t, filepath.Join(state, entries[0].Name())); !strings.Contains(got, "\n* merge=mergiraf\n") {
		t.Errorf("attributes file = %q", got)
	}
	if _, err := Args(context.Background(), repo, "", "/bin/mergiraf"); err == nil {
		t.Error("Args without a state dir succeeded")
	}
}

// Cleanup removes exactly what earlier builds wrote — the driver with
// mads's template, its name, the attributes line — keeps every other line
// and key, and a second run finds nothing and writes nothing.
func TestCleanupRemovesOnlyMadsEntries(t *testing.T) {
	isolate(t)
	repo := initRepo(t)
	attr := filepath.Join(repo, ".git", "info", "attributes")
	legacyRegister(t, repo, "/opt/homebrew/bin/mergiraf")
	gitRun(t, repo, "config", "merge.union.driver", "true")
	// The user's own lines on either side of the one mads appended.
	write(t, attr, "*.bin binary\n* merge=mergiraf\n*.md diff=markdown\n")

	removed, err := Cleanup(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Errorf("removed = %v, want driver, name and the attributes line", removed)
	}
	if got := read(t, attr); got != "*.bin binary\n*.md diff=markdown\n" {
		t.Errorf("attributes = %q", got)
	}
	if out, err := gitTry(repo, "config", "--local", "--get-regexp", `^merge\.mergiraf\.`); err == nil {
		t.Errorf("mergiraf config left: %s", out)
	}
	if strings.Contains(read(t, filepath.Join(repo, ".git", "config")), `[merge "mergiraf"]`) {
		t.Error("an empty [merge \"mergiraf\"] section was left behind")
	}
	if got := strings.TrimSpace(gitRun(t, repo, "config", "merge.union.driver")); got != "true" {
		t.Errorf("another driver's config was touched: %q", got)
	}
	if got := strings.TrimSpace(gitRun(t, repo, "check-attr", "merge", "--", "x.go")); got != "x.go: merge: unspecified" {
		t.Errorf("check-attr after cleanup = %q", got)
	}

	before := repoState(t, repo)
	if err := os.WriteFile(filepath.Join(repo, ".git", "config.lock"), nil, 0o644); err != nil {
		t.Fatal(err) // a held lock fails any config write: the repeat must not try one
	}
	removed, err = Cleanup(context.Background(), repo)
	if err != nil || len(removed) != 0 {
		t.Errorf("second Cleanup = %v, %v; want nothing", removed, err)
	}
	if repoState(t, repo) != before {
		t.Error("second Cleanup changed the repo")
	}
}

// A section holding more than mads's two keys keeps the rest.
func TestCleanupKeepsOtherKeysInTheSection(t *testing.T) {
	isolate(t)
	repo := initRepo(t)
	legacyRegister(t, repo, "/usr/local/bin/mergiraf")
	gitRun(t, repo, "config", "merge.mergiraf.recursive", "binary")
	if _, err := Cleanup(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(gitRun(t, repo, "config", "--local", "--get-regexp", `^merge\.mergiraf\.`))
	if got != "merge.mergiraf.recursive binary" {
		t.Errorf("section after cleanup = %q", got)
	}
}

// A registration the user wrote, by mergiraf's own instructions (a bare
// `mergiraf` command), is theirs: Cleanup leaves it and its rule alone.
func TestCleanupKeepsUserRegistration(t *testing.T) {
	isolate(t)
	repo := initRepo(t)
	gitRun(t, repo, "config", "merge.mergiraf.name", "mergiraf")
	gitRun(t, repo, "config", "merge.mergiraf.driver", "mergiraf"+commandArgs)
	write(t, filepath.Join(repo, ".git", "info", "attributes"), "* merge=mergiraf\n")
	before := repoState(t, repo)

	removed, err := Cleanup(context.Background(), repo)
	if err != nil || len(removed) != 0 {
		t.Errorf("Cleanup = %v, %v; want nothing removed", removed, err)
	}
	if repoState(t, repo) != before {
		t.Error("Cleanup changed the user's own registration")
	}
}

// The attributes line alone (its config already gone) names no driver and
// is removed — unless the user configured a mergiraf driver globally, when
// the line may be theirs.
func TestCleanupDanglingAttributesLine(t *testing.T) {
	home := isolate(t)
	repo := initRepo(t)
	attr := filepath.Join(repo, ".git", "info", "attributes")

	write(t, attr, "* merge=mergiraf\n")
	write(t, filepath.Join(home, ".gitconfig"), "[merge \"mergiraf\"]\n\tdriver = mergiraf"+commandArgs+"\n")
	if removed, err := Cleanup(context.Background(), repo); err != nil || len(removed) != 0 {
		t.Errorf("with a global driver, Cleanup = %v, %v; want nothing", removed, err)
	}
	if read(t, attr) != "* merge=mergiraf\n" {
		t.Error("the line next to the user's global driver was removed")
	}

	if err := os.Remove(filepath.Join(home, ".gitconfig")); err != nil {
		t.Fatal(err)
	}
	if removed, err := Cleanup(context.Background(), repo); err != nil || len(removed) != 1 {
		t.Errorf("without a driver, Cleanup = %v, %v; want the line", removed, err)
	}
	if _, err := os.Stat(attr); !os.IsNotExist(err) {
		t.Errorf("attributes holding only mads's line should be gone: %v", err)
	}
}

// From a linked worktree, --git-common-dir is absolute: cleanup must reach
// the main checkout's .git, not a path joined onto the worktree.
func TestCleanupFromLinkedWorktree(t *testing.T) {
	isolate(t)
	main := initRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitRun(t, main, "worktree", "add", "-q", "-b", "feature", linked)
	legacyRegister(t, main, "/opt/homebrew/bin/mergiraf")

	removed, err := Cleanup(context.Background(), linked)
	if err != nil || len(removed) != 3 {
		t.Fatalf("Cleanup from the linked worktree = %v, %v", removed, err)
	}
	if got := read(t, filepath.Join(main, ".git", "info", "attributes")); got != "" {
		t.Errorf("attributes in the main .git = %q", got)
	}
	if got := strings.TrimSpace(gitRun(t, linked, "check-attr", "merge", "--", "x.go")); got != "x.go: merge: unspecified" {
		t.Errorf("check-attr in the linked worktree = %q", got)
	}
}

func TestVersion(t *testing.T) {
	if got := Version(context.Background(), exe(t)); got != "mergiraf 9.9.9" {
		t.Errorf("Version = %q", got)
	}
	if got := Version(context.Background(), filepath.Join(t.TempDir(), "gone")); got != "" {
		t.Errorf("Version of a missing binary = %q", got)
	}
}
