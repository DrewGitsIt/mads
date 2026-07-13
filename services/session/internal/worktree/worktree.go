// Package worktree gives interactive sessions the same git worktree
// isolation the pipeline engine gives tasks (spec §6, §17): a session gets
// its own worktree and branch, work happens there instead of on the primary
// tree, and merge-back is attempted on request. The git mechanics are
// mirrored from services/pipeline/engine/worktree.go rather than shared,
// since the two services' release timing shouldn't be coupled together —
// but the post-commit hook is a wire contract with the Data Service, so it
// comes from pkg/githook, never a local copy.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/githook"
)

// Manager owns per-session isolation: worktree + branch per session, session
// context in per-worktree config for the hook, serialized merges back into
// the primary, cleanup after a clean merge.
type Manager struct {
	Primary   string // the workspace repo
	Root      string // where session worktrees live
	DataURL   string
	SessionID string

	setup   sync.Once
	setupEr error
	mergeMu sync.Mutex // one merge (and its verify) at a time
}

// IsGitRepo reports whether dir is inside a git repository. The runner uses
// this to silently fall back to non-isolated behavior when Cwd isn't a git
// repo — isolation stays primary-repo-only (spec §17), never a hard error
// for a session opened on a plain document folder.
func IsGitRepo(ctx context.Context, dir string) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	return cmd.Run() == nil
}

// Git runs one git command in dir.
func (m *Manager) Git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Identity for engine safety commits, without touching user config.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=mads-session", "GIT_AUTHOR_EMAIL=session@mads.local",
		"GIT_COMMITTER_NAME=mads-session", "GIT_COMMITTER_EMAIL=session@mads.local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ensureSetup enables per-worktree config and installs the post-commit hook
// (the shared pkg/githook contract — see the package comment), once per
// manager.
func (m *Manager) ensureSetup(ctx context.Context) error {
	m.setup.Do(func() {
		m.setupEr = githook.EnsureInstalled(ctx, m.Primary)
	})
	return m.setupEr
}

// Create makes the session's worktree and branch and stamps the session
// context into per-worktree config for the hook.
func (m *Manager) Create(ctx context.Context, id, title string) (dir, branch string, err error) {
	if err := m.ensureSetup(ctx); err != nil {
		return "", "", err
	}
	branch = "mads/session/" + id
	dir = filepath.Join(m.Root, id)
	if _, err := m.Git(ctx, m.Primary, "worktree", "add", "-b", branch, dir); err != nil {
		return "", "", err
	}
	if err := githook.SetWorktreeConfig(ctx, dir, githook.Config{
		SessionID: m.SessionID,
		DataURL:   m.DataURL,
	}); err != nil {
		return "", "", err
	}
	return dir, branch, nil
}

// CommitStray commits any uncommitted agent work so nothing is ever lost at
// merge time; the trailer keeps the commit linked to its session (spec §3,
// §5).
func (m *Manager) CommitStray(ctx context.Context, dir, id, title string) error {
	status, err := m.Git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}
	if _, err := m.Git(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	msg := fmt.Sprintf("%s (session auto-commit)\n\nSession: %s", title, id)
	_, err = m.Git(ctx, dir, "commit", "-m", msg)
	return err
}

// Merge brings the session branch into the primary's current HEAD. mergeCmd
// is e.g. "git merge --no-ff". A conflict aborts cleanly and comes back as a
// structured contracts.MergeResult — not a Go error — so the API can hand it
// straight to the UI's resolution flow; only unexpected git failures (bad
// merge command, corrupt repo, etc.) come back as error.
func (m *Manager) Merge(ctx context.Context, mergeCmd, id, title, branch string) (contracts.MergeResult, error) {
	m.mergeMu.Lock()
	defer m.mergeMu.Unlock()
	// Two -m flags make subject + body paragraphs — %q on a multiline
	// string would bake literal \n into the subject and break the trailer.
	subject := fmt.Sprintf("Merge %s: %s", branch, title)
	full := fmt.Sprintf("%s %s -m %q -m %q", mergeCmd, branch, subject, "Session: "+id)
	cmd := exec.CommandContext(ctx, "sh", "-c", full)
	cmd.Dir = m.Primary
	out, err := cmd.CombinedOutput()
	if err == nil {
		return contracts.MergeResult{Branch: branch}, nil
	}
	conflicts, _ := m.Git(ctx, m.Primary, "diff", "--name-only", "--diff-filter=U")
	_, _ = m.Git(ctx, m.Primary, "merge", "--abort")
	if files := strings.TrimSpace(conflicts); files != "" {
		return contracts.MergeResult{
			Conflicted: true,
			Branch:     branch,
			Files:      strings.Split(files, "\n"),
		}, nil
	}
	return contracts.MergeResult{}, fmt.Errorf("merge failed: %v\n%s", err, strings.TrimSpace(string(out)))
}

// Remove cleans up after a fully-merged session. Conflicted merges keep
// their worktree and branch for inspection and manual merge-back.
func (m *Manager) Remove(ctx context.Context, dir, branch string) {
	_, _ = m.Git(ctx, m.Primary, "worktree", "remove", "--force", dir)
	_, _ = m.Git(ctx, m.Primary, "branch", "-D", branch)
}
