// Package githook installs and configures the mads post-commit hook (spec
// §5): the same hook, with the same reporting contract, is installed in
// both pipeline task worktrees (services/pipeline/engine) and interactive
// session worktrees (services/session), so every commit — wherever it's
// made — reports to the Data Service with task linkage.
package githook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Script is the post-commit hook body (hook v2,
// docs/LOCAL-FRONT-DOOR-DESIGN.md D6): it reports every commit — from any of
// the repo's worktrees — to the Data Service with the task context stored
// in the worktree's own git config, authenticated with the install token.
//
// The token is read from the file named by `git config mads.tokenFile` — a
// path, never the token itself, since a repo's config can be readable by
// others — and reaches curl on stdin (`-K -`, written by the shell's
// printf builtin), never in an argv another user could see with ps. It goes
// only to a loopback mads.dataUrl (localhost, 127.0.0.1, [::1]; never one
// with userinfo), since repo config can name any URL (round 2). A
// missing or unreadable token file, like any other failure, stays silent:
// reporting must never break a commit, hence the `|| true`s and the silent
// curl.
const Script = `#!/bin/sh
# installed by mads (hook v2) — reports commits with task context (spec §5)
session_id=$(git config mads.sessionId 2>/dev/null)
data_url=$(git config mads.dataUrl 2>/dev/null)
token_file=$(git config mads.tokenFile 2>/dev/null)
[ -n "$session_id" ] && [ -n "$data_url" ] && [ -n "$token_file" ] || exit 0
# The token goes only to a Data Service on this machine: a repo's config
# could name any URL (round 2).
case "$data_url" in
  *@*|*[[:space:]]*) exit 0 ;;
  http://localhost|http://localhost:*|http://localhost/*) : ;;
  http://127.0.0.1|http://127.0.0.1:*|http://127.0.0.1/*) : ;;
  "http://[::1]"|"http://[::1]:"*|"http://[::1]/"*) : ;;
  *) exit 0 ;;
esac
token=""
{ IFS= read -r token < "$token_file"; } 2>/dev/null || true
[ -n "$token" ] || exit 0
hash=$(git rev-parse HEAD)
subject=$(git log -1 --format=%s | sed 's/\\/\\\\/g; s/"/\\"/g')
trailer=$(git log -1 --format=%B | sed -n 's/^Task:[[:space:]]*//p' | head -n1)
common_dir=$(git rev-parse --git-common-dir)
case "$common_dir" in
  /*) : ;;
  *) common_dir="$PWD/$common_dir" ;;
esac
repo=$(dirname "$common_dir" | sed 's/\\/\\\\/g; s/"/\\"/g')
printf 'header = "Authorization: Bearer %s"\n' "$token" | curl -s -K - -X POST -H 'Content-Type: application/json' \
  -H "X-Mads-Actor: session-$session_id" \
  -d "{\"hash\":\"$hash\",\"message\":\"$subject\",\"repo\":\"$repo\",\"trailer_task_id\":\"$trailer\"}" \
  "$data_url/sessions/$session_id/commits" >/dev/null 2>&1 || true
`

// git runs one git command in dir.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// EnsureInstalled enables per-worktree config and installs the post-commit
// hook in dir's repo, unless a foreign (non-mads) hook is already present.
// Idempotent — safe to call repeatedly, including from multiple worktrees
// of the same repo.
func EnsureInstalled(ctx context.Context, dir string) error {
	if _, err := git(ctx, dir, "config", "extensions.worktreeConfig", "true"); err != nil {
		return err
	}
	gitDir, err := git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	// git prints the common dir relative to dir from a primary checkout but
	// absolute from a linked worktree; joining an absolute path onto dir
	// would plant a junk hooks tree inside the worktree, which then reads as
	// uncommitted work.
	common := strings.TrimSpace(gitDir)
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	hooksDir := filepath.Join(common, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}
	hook := filepath.Join(hooksDir, "post-commit")
	if raw, err := os.ReadFile(hook); err == nil && !strings.Contains(string(raw), "installed by mads") {
		return fmt.Errorf("a foreign post-commit hook exists at %s — not overwriting", hook)
	}
	return os.WriteFile(hook, []byte(Script), 0o755) //nolint:gosec // hooks must be executable
}

// Config is the per-worktree task context the hook reads via git config.
// TokenFile is the install token's path ($MADS_HOME/local.token), which the
// hook reads at each commit.
type Config struct {
	TaskID    string
	SessionID string
	DataURL   string
	TokenFile string
}

// SetWorktreeConfig stamps cfg into dir's per-worktree git config
// (extensions.worktreeConfig must already be enabled — see EnsureInstalled).
// Empty fields are skipped.
func SetWorktreeConfig(ctx context.Context, dir string, cfg Config) error {
	for key, val := range map[string]string{
		"mads.taskId":    cfg.TaskID,
		"mads.sessionId": cfg.SessionID,
		"mads.dataUrl":   cfg.DataURL,
		"mads.tokenFile": cfg.TokenFile,
	} {
		if val == "" {
			continue
		}
		if _, err := git(ctx, dir, "config", "--worktree", key, val); err != nil {
			return err
		}
	}
	return nil
}
