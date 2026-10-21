package changes

import (
	"os"
	"path/filepath"
	"strings"
)

// Agent harnesses give subagents their own worktree inside the session's
// folder (Claude Code: .claude/worktrees/<name>/) and remove it when the
// subagent finishes. The subagent's edits reach the ledger as the session's
// own, but they're scratch copies: what counts is whatever lands in the
// session's folder. Listing them double-counts the work while the worktree
// exists, and once it's removed every file it touched reads as deleted —
// its only "before" is the pre-image captured mid-session, since the
// worktree didn't exist at session start. So they're left out, with a note.
// A stopgap until delegates have their own changes view.

// worktreeDirs is where harnesses put subagent worktrees, as path segments
// under the session folder; the next segment names the worktree.
var worktreeDirs = [][]string{{".claude", "worktrees"}}

// nestedWorktrees finds the subagent worktrees under a session folder.
type nestedWorktrees struct {
	folder string
	linked map[string]bool // dir → is a live linked worktree
}

func newNestedWorktrees(folder string) *nestedWorktrees {
	return &nestedWorktrees{folder: folder, linked: map[string]bool{}}
}

// rootOf is the root of the nested worktree p is in (p itself when p is
// that root), "" when p isn't in one. A nested worktree is a directory
// strictly under the session folder that is either at a known harness
// location (matched by path, so a removed one still counts) or a live
// linked worktree (.git is a file pointing into some repo's worktrees/).
// Nested repos and submodules don't count, so a session started in a
// parent of several repos keeps them all.
func (n *nestedWorktrees) rootOf(p string) string {
	if n == nil || n.folder == "" {
		return ""
	}
	rel, ok := relUnder(n.folder, p)
	if !ok {
		return ""
	}
	parts := strings.Split(rel, "/")
	for i := range parts {
		for _, pat := range worktreeDirs {
			if i+len(pat) < len(parts) && segmentsEqual(parts[i:i+len(pat)], pat) {
				return filepath.Join(n.folder, filepath.Join(parts[:i+len(pat)+1]...))
			}
		}
		d := filepath.Join(n.folder, filepath.Join(parts[:i+1]...))
		if n.isLinkedWorktree(d) {
			return d
		}
	}
	return ""
}

func segmentsEqual(a, b []string) bool {
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (n *nestedWorktrees) isLinkedWorktree(dir string) bool {
	if v, ok := n.linked[dir]; ok {
		return v
	}
	v := false
	if b, err := os.ReadFile(filepath.Join(dir, ".git")); err == nil {
		// "gitdir: /repo/.git/worktrees/<name>" — a submodule's points
		// into .git/modules/ instead.
		line := strings.TrimSpace(string(b))
		if gitdir, ok := strings.CutPrefix(line, "gitdir:"); ok {
			gitdir = filepath.ToSlash(strings.TrimSpace(gitdir))
			v = strings.Contains(gitdir, "/worktrees/")
		}
	}
	n.linked[dir] = v
	return v
}

// dropNestedWorktrees removes ledger paths inside nested worktrees,
// returning how many files and worktrees were left out.
func dropNestedWorktrees(ledger map[string]*ledgerFile, n *nestedWorktrees) (files, worktrees int) {
	roots := map[string]bool{}
	for p := range ledger {
		if r := n.rootOf(p); r != "" {
			delete(ledger, p)
			roots[r] = true
			files++
		}
	}
	return files, len(roots)
}
