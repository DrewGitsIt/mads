package changes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Subagent worktrees inside the session folder are left out: a removed
// one's files used to read as deleted (their only "before" is a mid-session
// pre-image), and a live one's double-counted the work.
func TestBuildLeavesOutSubagentWorktrees(t *testing.T) {
	repo := newRepo(t, map[string]string{".gitignore": "/.claude/\n", "a.go": "package a\n"})
	sess, store := shadowFor(t, repo)

	// A Claude Code subagent worktree, edited and then removed.
	gone := filepath.Join(repo, ".claude", "worktrees", "agent-1", "engine.go")
	writeFile(t, gone, "one\ntwo\nthree\n")
	store.Capture("toolu_1", gone)
	writeFile(t, gone, "one\n")
	if err := os.RemoveAll(filepath.Join(repo, ".claude")); err != nil {
		t.Fatal(err)
	}

	// A live linked worktree somewhere else under the folder.
	git(t, repo, "worktree", "add", "-q", "-b", "side", filepath.Join(repo, "wt", "live"))
	live := filepath.Join(repo, "wt", "live", "a.go")
	store.Capture("toolu_2", live)
	writeFile(t, live, "package a\n\nfunc Live() {}\n")

	// The session's own edit.
	own := filepath.Join(repo, "a.go")
	writeFile(t, own, "package a\n\nfunc A() {}\n")
	store.CommitTurnAsync()
	store.Wait()

	at := time.Now()
	set := build(t, Inputs{
		Session: sess,
		Ledger: []contracts.FileChange{
			ledgerRow(gone, "modified", at),
			ledgerRow(live, "modified", at),
			ledgerRow(own, "modified", at),
		},
	})
	files := byDisplay(set)
	if len(files) != 1 || files["a.go"].Change != "modified" {
		t.Fatalf("files = %v, want only a.go", keys(files))
	}
	found := false
	for _, n := range set.Notes {
		found = found || strings.Contains(n, "2 files in 2 subagent worktrees are not listed")
	}
	if !found {
		t.Errorf("notes = %v, want the subagent-worktree note", set.Notes)
	}
}

func TestNestedWorktreesRootOf(t *testing.T) {
	folder := tempDir(t)
	n := newNestedWorktrees(folder)

	// Known harness location: matched by path, existing or not.
	if got, want := n.rootOf(filepath.Join(folder, ".claude/worktrees/agent-9/pkg/x.go")), filepath.Join(folder, ".claude/worktrees/agent-9"); got != want {
		t.Errorf("claude worktree root = %q, want %q", got, want)
	}
	// Hermes (delegation.worktree_isolation): .worktrees/subagent-<id>, prefix required.
	if got, want := n.rootOf(filepath.Join(folder, ".worktrees/subagent-3/pkg/x.go")), filepath.Join(folder, ".worktrees/subagent-3"); got != want {
		t.Errorf("hermes worktree root = %q, want %q", got, want)
	}
	// .claude/worktrees itself, files directly in .claude, and a user's own
	// worktree folder under .worktrees (not live here) aren't in a subagent worktree.
	for _, p := range []string{".claude/worktrees", ".claude/settings.json", "src/.claude/x", ".worktrees/feature-x/a.go", ".worktrees/a.go"} {
		if got := n.rootOf(filepath.Join(folder, p)); got != "" {
			t.Errorf("rootOf(%s) = %q, want \"\"", p, got)
		}
	}

	// A submodule's .git file points into .git/modules/, a plain nested repo
	// has a .git dir: neither is a subagent worktree.
	writeFile(t, filepath.Join(folder, "sub", ".git"), "gitdir: ../.git/modules/sub\n")
	writeFile(t, filepath.Join(folder, "sub", "f.go"), "x")
	if err := os.MkdirAll(filepath.Join(folder, "inner", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A linked worktree's .git file points into worktrees/.
	writeFile(t, filepath.Join(folder, "tools", "wt", ".git"), "gitdir: /elsewhere/repo/.git/worktrees/wt\n")
	cases := map[string]string{
		"sub/f.go":          "",
		"inner/f.go":        "",
		"tools/wt/f.go":     filepath.Join(folder, "tools", "wt"),
		"tools/wt/a/b.go":   filepath.Join(folder, "tools", "wt"),
		"tools/other.go":    "",
		"plain/dir/file.go": "",
	}
	for rel, want := range cases {
		if got := n.rootOf(filepath.Join(folder, rel)); got != want {
			t.Errorf("rootOf(%s) = %q, want %q", rel, got, want)
		}
	}
	// Outside the folder: never.
	if got := n.rootOf("/elsewhere/.claude/worktrees/a/x.go"); got != "" {
		t.Errorf("outside folder = %q", got)
	}
	if got := newNestedWorktrees("").rootOf(filepath.Join(folder, ".claude/worktrees/a/x.go")); got != "" {
		t.Errorf("no folder = %q", got)
	}
}
