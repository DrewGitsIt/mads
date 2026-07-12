package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// hookScript is the post-commit hook installed in the workspace repo
// (spec §5): it reports every commit — from any of the repo's worktrees —
// to the Data Service with the task context stored in the worktree's own
// git config. Reporting must never break a commit, hence the trailing
// `|| true` and the silent curl.
const hookScript = `#!/bin/sh
# installed by mads — reports commits with task context (spec §5)
session_id=$(git config mads.sessionId 2>/dev/null)
data_url=$(git config mads.dataUrl 2>/dev/null)
[ -n "$session_id" ] && [ -n "$data_url" ] || exit 0
hash=$(git rev-parse HEAD)
subject=$(git log -1 --format=%s | sed 's/\\/\\\\/g; s/"/\\"/g')
trailer=$(git log -1 --format=%B | sed -n 's/^Task:[[:space:]]*//p' | head -n1)
curl -s -X POST -H 'Content-Type: application/json' \
  -H "X-Mads-Actor: session-$session_id" \
  -d "{\"hash\":\"$hash\",\"message\":\"$subject\",\"trailer_task_id\":\"$trailer\"}" \
  "$data_url/sessions/$session_id/commits" >/dev/null 2>&1 || true
`

// worktrees owns per-task isolation (spec §6, §17): worktree + branch per
// task, task context in per-worktree config for the hook, serialized merges
// back into the primary, cleanup after a clean merge.
type worktrees struct {
	primary   string // the workspace repo
	root      string // where task worktrees live
	dataURL   string
	sessionID string

	setup   sync.Once
	setupEr error
	mergeMu sync.Mutex // one merge (and its verify) at a time
}

// git runs one git command in dir.
func (w *worktrees) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Identity for engine safety commits, without touching user config.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=mads-pipeline", "GIT_AUTHOR_EMAIL=pipeline@mads.local",
		"GIT_COMMITTER_NAME=mads-pipeline", "GIT_COMMITTER_EMAIL=pipeline@mads.local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ensureSetup enables per-worktree config and installs the post-commit hook,
// once per run.
func (w *worktrees) ensureSetup(ctx context.Context) error {
	w.setup.Do(func() {
		if _, err := w.git(ctx, w.primary, "config", "extensions.worktreeConfig", "true"); err != nil {
			w.setupEr = err
			return
		}
		gitDir, err := w.git(ctx, w.primary, "rev-parse", "--git-common-dir")
		if err != nil {
			w.setupEr = err
			return
		}
		hooksDir := filepath.Join(w.primary, strings.TrimSpace(gitDir), "hooks")
		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
			w.setupEr = err
			return
		}
		hook := filepath.Join(hooksDir, "post-commit")
		if raw, err := os.ReadFile(hook); err == nil && !strings.Contains(string(raw), "installed by mads") {
			w.setupEr = fmt.Errorf("a foreign post-commit hook exists at %s — not overwriting", hook)
			return
		}
		w.setupEr = os.WriteFile(hook, []byte(hookScript), 0o755) //nolint:gosec // hooks must be executable
	})
	return w.setupEr
}

// create makes the task's worktree and branch and stamps the task context
// into per-worktree config for the hook.
func (w *worktrees) create(ctx context.Context, task contracts.Task) (dir, branch string, err error) {
	if err := w.ensureSetup(ctx); err != nil {
		return "", "", err
	}
	branch = "mads/" + task.ID
	dir = filepath.Join(w.root, task.ID)
	if _, err := w.git(ctx, w.primary, "worktree", "add", "-b", branch, dir); err != nil {
		return "", "", err
	}
	for key, val := range map[string]string{
		"mads.taskId":    task.ID,
		"mads.sessionId": w.sessionID,
		"mads.dataUrl":   w.dataURL,
	} {
		if val == "" {
			continue
		}
		if _, err := w.git(ctx, dir, "config", "--worktree", key, val); err != nil {
			return "", "", err
		}
	}
	return dir, branch, nil
}

// commitStray commits any uncommitted agent work so nothing is ever lost at
// merge time; the trailer keeps the commit linked to its task (spec §3, §5).
func (w *worktrees) commitStray(ctx context.Context, dir string, task contracts.Task) error {
	status, err := w.git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}
	if _, err := w.git(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	msg := fmt.Sprintf("%s (pipeline auto-commit)\n\nTask: %s", task.Title, task.ID)
	_, err = w.git(ctx, dir, "commit", "-m", msg)
	return err
}

// merge brings the task branch into the primary. mergeCmd comes from the
// template (e.g. "git merge --no-ff"); the engine supplies the branch and
// message. A conflict aborts cleanly and returns the conflicted files as the
// error — the loud path (spec §6).
func (w *worktrees) merge(ctx context.Context, mergeCmd string, task contracts.Task, branch string) error {
	msg := fmt.Sprintf("Merge %s: %s\n\nTask: %s", branch, task.Title, task.ID)
	full := fmt.Sprintf("%s %s -m %q", mergeCmd, branch, msg)
	cmd := exec.CommandContext(ctx, "sh", "-c", full)
	cmd.Dir = w.primary
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	conflicts, _ := w.git(ctx, w.primary, "diff", "--name-only", "--diff-filter=U")
	_, _ = w.git(ctx, w.primary, "merge", "--abort")
	if files := strings.TrimSpace(conflicts); files != "" {
		return fmt.Errorf("merge conflict with the current trunk in:\n%s\n\nthe merge was aborted; the task's work is intact on branch %s — resolve by rebasing that branch or adjusting the conflicting tasks", files, branch)
	}
	return fmt.Errorf("merge failed: %v\n%s", err, strings.TrimSpace(string(out)))
}

// remove cleans up after a fully-merged task. Blocked tasks keep their
// worktrees for inspection and manual merge-back.
func (w *worktrees) remove(ctx context.Context, dir, branch string) {
	_, _ = w.git(ctx, w.primary, "worktree", "remove", "--force", dir)
	_, _ = w.git(ctx, w.primary, "branch", "-D", branch)
}
