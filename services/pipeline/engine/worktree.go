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
	"github.com/drewkidwell/mads/pkg/dataclient"
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
	data      *dataclient.Client // task lookups for conflict diagnostics

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
// message. By the time this runs, rebaseBeforeMerge has already reconciled
// the branch with trunk under the same mergeMu lock, so a conflict here is a
// rare defensive fallback rather than the primary path — it still aborts
// cleanly and returns the conflicted files as the error (spec §6).
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

// rebaseBeforeMerge merges current trunk (primary HEAD) into the branch,
// inside the task's own worktree (dir), before the trunk-side merge — so
// "trunk moved under me" is resolved here instead of surfacing as a conflict
// against the primary. Called under mergeMu, same as the trunk merge itself.
func (w *worktrees) rebaseBeforeMerge(ctx context.Context, dir string, task contracts.Task) (rebased bool, err error) {
	trunkHead, err := w.git(ctx, w.primary, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	trunkHead = strings.TrimSpace(trunkHead)

	// A previous loop_back attempt may have left a merge in progress here
	// (a merge_resolve agent stage edited files but the resolution wasn't
	// finished). Don't re-invoke `git merge` on top of that — either finish
	// it mechanically (nothing left unmerged) or re-surface the same
	// conflict.
	if _, err := w.git(ctx, dir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		conflicts, _ := w.git(ctx, dir, "diff", "--name-only", "--diff-filter=U")
		if strings.TrimSpace(conflicts) != "" {
			return false, w.conflictErr(ctx, dir, conflicts, trunkHead)
		}
		if _, err := w.git(ctx, dir, "commit", "--no-edit"); err != nil {
			return false, fmt.Errorf("rebase-before-merge: finishing the resolved merge: %v", err)
		}
		return true, nil
	}

	if _, err := w.git(ctx, dir, "merge-base", "--is-ancestor", trunkHead, "HEAD"); err == nil {
		return false, nil // trunk hasn't moved since this branch last caught up: a non-event
	}

	subject := fmt.Sprintf("Merge trunk into %s (pipeline rebase-before-merge)", task.ID)
	if _, err := w.git(ctx, dir, "merge", trunkHead, "-m", subject); err == nil {
		return true, nil
	}
	conflicts, _ := w.git(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if strings.TrimSpace(conflicts) == "" {
		return false, fmt.Errorf("rebase-before-merge: merging trunk into the branch failed")
	}
	return false, w.conflictErr(ctx, dir, conflicts, trunkHead)
}

// conflictErr reports a rebase-before-merge conflict: the conflicted files
// plus the trunk-side tasks whose merges introduced the conflicting changes.
// The conflict state (markers, MERGE_MSG) is left in dir for a merge_resolve
// stage or manual resolution — no `merge --abort` here.
func (w *worktrees) conflictErr(ctx context.Context, dir, conflicts, trunkHead string) error {
	base, _ := w.git(ctx, dir, "merge-base", "HEAD", trunkHead)
	rangeSpec := trunkHead
	if b := strings.TrimSpace(base); b != "" {
		rangeSpec = b + ".." + trunkHead
	}
	return fmt.Errorf(
		"rebase-before-merge: trunk has moved and conflicts with this branch's changes in:\n%s\n\n%s\n\nthe worktree merge was left unresolved (conflict markers and MERGE_MSG intact) for a merge_resolve stage or manual resolution",
		strings.TrimSpace(conflicts), w.conflictingTrunkTasks(ctx, rangeSpec))
}

// conflictingTrunkTasks names the tasks whose merges landed the trunk
// commits in rangeSpec, via the Task: trailer worktrees.merge stamps on
// every merge commit (spec §5). Best-effort: this is diagnostic text for a
// prompt, not a gate — a lookup failure degrades the message, never the
// task's actual failure path.
func (w *worktrees) conflictingTrunkTasks(ctx context.Context, rangeSpec string) string {
	out, err := w.git(ctx, w.primary, "log", rangeSpec, "--format=%(trailers:key=Task,valueonly)")
	if err != nil {
		return "conflicting trunk-side task(s): could not be determined (" + err.Error() + ")"
	}
	seen := map[string]bool{}
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		id := strings.TrimSpace(line)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return "conflicting trunk-side task(s): none identified (no Task trailer on the trunk commits since this branch's base)"
	}
	var b strings.Builder
	b.WriteString("conflicting trunk-side task(s) since this branch's base:\n")
	for _, id := range ids {
		t, err := w.data.Task(ctx, id)
		if err != nil {
			fmt.Fprintf(&b, "- %s (could not fetch task: %v)\n", id, err)
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", id, t.Title)
		if t.Description != "" {
			fmt.Fprintf(&b, "  %s\n", strings.ReplaceAll(t.Description, "\n", "\n  "))
		}
	}
	return b.String()
}

// sweepHandoff removes any file named HANDOFF.md, at any depth, still
// present on the task branch — the architect→executor convention says the
// executor deletes it, but that's chronically forgotten; this is the
// backstop (Drew: "we can blindly delete anything called HANDOFF.md
// pre-merge").
func (w *worktrees) sweepHandoff(ctx context.Context, dir string, task contracts.Task) error {
	out, err := w.git(ctx, dir, "ls-files", "-z")
	if err != nil {
		return err
	}
	var stale []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" && filepath.Base(p) == "HANDOFF.md" {
			stale = append(stale, p)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	if _, err := w.git(ctx, dir, append([]string{"rm", "--"}, stale...)...); err != nil {
		return err
	}
	msg := fmt.Sprintf("Remove HANDOFF.md before merge (pipeline convention)\n\nTask: %s", task.ID)
	_, err = w.git(ctx, dir, "commit", "-m", msg)
	return err
}

// remove cleans up after a fully-merged task. Blocked tasks keep their
// worktrees for inspection and manual merge-back.
func (w *worktrees) remove(ctx context.Context, dir, branch string) {
	_, _ = w.git(ctx, w.primary, "worktree", "remove", "--force", dir)
	_, _ = w.git(ctx, w.primary, "branch", "-D", branch)
}
