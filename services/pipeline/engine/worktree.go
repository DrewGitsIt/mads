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
	"github.com/drewkidwell/mads/pkg/githook"
)

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
		w.setupEr = githook.EnsureInstalled(ctx, w.primary)
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
	if err := githook.SetWorktreeConfig(ctx, dir, githook.Config{
		TaskID:    task.ID,
		SessionID: w.sessionID,
		DataURL:   w.dataURL,
	}); err != nil {
		return "", "", err
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
	// Two -m flags make subject + body paragraphs — %q on a multiline
	// string would bake literal \n into the subject and break the trailer.
	subject := fmt.Sprintf("Merge %s: %s", branch, task.Title)
	full := fmt.Sprintf("%s %s -m %q -m %q", mergeCmd, branch, subject, "Task: "+task.ID)
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
