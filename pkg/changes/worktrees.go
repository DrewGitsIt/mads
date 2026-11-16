package changes

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/drewkidwell/mads/services/session/shadow"
)

// Agent harnesses give subagents their own worktree inside the session's
// folder (Claude Code: .claude/worktrees/<name>/; Hermes, with
// delegation.worktree_isolation: .worktrees/subagent-<id>/) and remove it
// when the subagent is done. The subagent's edits reach the ledger as the
// session's own, but they're scratch copies: what counts is whatever lands
// in the session's folder. Listing them double-counts the work while the worktree
// exists, and once it's removed every file it touched reads as deleted —
// its only "before" is the pre-image captured mid-session, since the
// worktree didn't exist at session start. So they're left out, with a note.
// A stopgap until delegates have their own changes view.

// worktreeDir is where a harness puts subagent worktrees: path segments
// under the session folder, then a segment naming the worktree, which must
// start with prefix when one is set.
type worktreeDir struct {
	dirs   []string
	prefix string
}

// worktreeDirs are the known harness locations. Codex and goose subagents
// share the parent's folder, so their edits are real and stay listed.
var worktreeDirs = []worktreeDir{
	{dirs: []string{".claude", "worktrees"}},
	// Prefixed: people keep their own worktrees in .worktrees/ too, and a
	// user's worktree isn't a harness's scratch copy (unless it's live, which
	// the linked-worktree check covers either way).
	{dirs: []string{".worktrees"}, prefix: "subagent-"},
}

// nestedWorktrees finds the subagent worktrees under a session folder.
type nestedWorktrees struct {
	folder    string
	commonDir string          // the folder's repo's git dir, "" when the folder isn't in a repo
	linked    map[string]bool // dir → is a live linked worktree of the folder's repo
}

func newNestedWorktrees(folder string) *nestedWorktrees {
	n := &nestedWorktrees{folder: folder, linked: map[string]bool{}}
	if folder != "" {
		if root := repoRootOf(folder, map[string]string{}); root != "" {
			n.commonDir = gitCommonDir(root)
		}
	}
	return n
}

// rootOf is the root of the nested worktree p is in (p itself when p is
// that root), "" when p isn't in one. A nested worktree is a directory
// strictly under the session folder that is either at a known harness
// location (matched by path, so a removed one still counts) or a live
// linked worktree of the folder's own repo (.git is a file pointing into
// that repo's worktrees/). Nested repos and submodules don't count, so a
// session started in a parent of several repos keeps them all — and so do
// worktrees of other repos: a session in / or ~/repo that makes its own
// worktrees is doing its own work there, not a subagent's.
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
		for _, wd := range worktreeDirs {
			k := len(wd.dirs)
			if i+k < len(parts) && segmentsEqual(parts[i:i+k], wd.dirs) && strings.HasPrefix(parts[i+k], wd.prefix) {
				return filepath.Join(n.folder, filepath.Join(parts[:i+k+1]...))
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
	// "gitdir: /repo/.git/worktrees/<name>" — a submodule's points into
	// .git/modules/ instead.
	if n.commonDir != "" {
		if gitdir := readGitFile(dir); gitdir != "" {
			v = filepath.Dir(gitdir) == filepath.Join(n.commonDir, "worktrees")
		}
	}
	n.linked[dir] = v
	return v
}

// gitCommonDir is the git dir shared by root's repo and all its worktrees:
// root/.git for a main checkout, or the main repo's .git when root is
// itself a linked worktree (its gitdir's commondir file says where). ""
// when it can't tell.
func gitCommonDir(root string) string {
	dotGit := filepath.Join(root, ".git")
	fi, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	if fi.IsDir() {
		return shadow.RealPath(dotGit)
	}
	gitdir := readGitFile(root)
	if gitdir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return gitdir // a submodule: its own git dir is the common one
	}
	common := strings.TrimSpace(string(b))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitdir, common)
	}
	return shadow.RealPath(common)
}

// readGitFile is the gitdir a .git file in dir points to, absolute and
// symlink-free; "" when dir/.git isn't such a file.
func readGitFile(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	return shadow.RealPath(gitdir)
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
