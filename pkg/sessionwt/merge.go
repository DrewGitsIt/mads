package sessionwt

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// The merge-back path below is W4.5's, kept working until Land (D6) replaces
// it: Runner.Merge still calls it. Every function here changes the repo, so
// the caller holds the repo lock.

// CommitStray commits any uncommitted work in dir so nothing is lost at merge
// time, with the safety identity and a Session trailer linking it back.
// Caller holds the repo lock.
func CommitStray(ctx context.Context, dir, sessionID, title string) error {
	status, err := Git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		return nil
	}
	if _, err := Git(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	msg := fmt.Sprintf("%s (session auto-commit)\n\nSession: %s", title, sessionID)
	_, err = Git(ctx, dir, "commit", "-m", msg)
	return err
}

// Merge brings branch into primary's current HEAD. mergeCmd is e.g. "git
// merge --no-ff". A conflict aborts cleanly and comes back as a structured
// contracts.MergeResult, not an error, so the API can hand it straight to
// the UI; only unexpected git failures are errors. Caller holds the repo
// lock.
func Merge(ctx context.Context, primary, mergeCmd, sessionID, title, branch string) (contracts.MergeResult, error) {
	// Two -m flags make subject + body paragraphs — %q on a multiline
	// string would bake literal \n into the subject and break the trailer.
	subject := fmt.Sprintf("Merge %s: %s", branch, title)
	full := fmt.Sprintf("%s %s -m %q -m %q", mergeCmd, branch, subject, "Session: "+sessionID)
	cmd := exec.CommandContext(ctx, "sh", "-c", full)
	cmd.Dir = primary
	out, err := cmd.CombinedOutput()
	if err == nil {
		return contracts.MergeResult{Branch: branch}, nil
	}
	conflicts, _ := Git(ctx, primary, "diff", "--name-only", "--diff-filter=U")
	_, _ = Git(ctx, primary, "merge", "--abort")
	if files := strings.TrimSpace(conflicts); files != "" {
		return contracts.MergeResult{
			Conflicted: true,
			Branch:     branch,
			Files:      strings.Split(files, "\n"),
		}, nil
	}
	return contracts.MergeResult{}, fmt.Errorf("merge failed: %v\n%s", err, strings.TrimSpace(string(out)))
}

// Remove deletes a session's worktree and branch, best-effort. git removes a
// worktree even with a process still working inside it, so the caller makes
// sure nothing is (D3). Caller holds the repo lock.
func Remove(ctx context.Context, primary, dir, branch string) {
	_, _ = Git(ctx, primary, "worktree", "remove", "--force", dir)
	_, _ = Git(ctx, primary, "branch", "-D", branch)
}
