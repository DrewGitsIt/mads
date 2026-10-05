// Package mergedriver finds mergiraf (https://mergiraf.org), a syntax-aware
// git merge driver, and registers it with a repository. Mergiraf is an
// optional runtime tool, never a build dependency: when it isn't installed,
// every caller carries on with plain git merging and nothing else changes.
//
// Two processes use it, against the same repositories: the pipeline engine,
// whose land queue merges parallel task branches, and session worktrees
// (pkg/sessionwt), whose Land merges the base into a session's branch. Both
// register mergiraf the same way, mergiraf's own documented integration
// (mergiraf.org/usage.html): a `merge.mergiraf.driver` config command plus
// a `* merge=mergiraf` rule in `<git-common-dir>/info/attributes`. Both
// live in the repository's shared git dir, so one registration covers the
// user's checkout and every linked worktree of it.
package mergedriver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/binlocate"
)

// Name is the git merge driver name mergiraf is registered under.
const Name = "mergiraf"

// Find returns the mergiraf binary to use, or "" when there is none.
// A configured path is used only if it resolves to an executable: a
// configured-but-unusable path means plain git, the same as none at all,
// and never falls through to a PATH lookup (so a test or a user can force
// plain git by pointing it somewhere empty). With nothing configured, it's
// a PATH lookup plus the documented install locations binlocate knows
// (Homebrew's prefixes, ~/.cargo/bin), which matters for an app launched
// from a desktop whose PATH lacks them.
func Find(configured string) string {
	if configured != "" {
		if p, err := exec.LookPath(configured); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
			return p
		}
		return ""
	}
	if p, err := binlocate.Find(Name); err == nil {
		return p
	}
	return ""
}

// Command is the merge driver command registered for the binary at path.
// The %S/%X/%Y label placeholders are only substituted by git 2.44+; on
// older git they pass through to mergiraf as literal text, which is
// harmless (mergiraf only uses them as conflict-marker labels on a hunk it
// can't resolve) — see mergiraf.org/usage.html#with-a-driver.
func Command(path string) string {
	return path + " merge --git %O %A %B -s %S -x %X -y %Y -p %P -l %L"
}

// attributesRule is the gitattributes line that sends every file through
// the driver; mergiraf itself falls back to a line-based merge for file
// types it doesn't know.
const attributesRule = "* merge=" + Name

// Register makes mergiraf at path the merge driver for the repository dir
// belongs to. Idempotent and cheap to repeat: config is read first and only
// written when it differs (so a repeat never takes git's config.lock), and
// the attributes rule is appended only when missing. dir may be the main
// checkout or any linked worktree of it.
func Register(ctx context.Context, dir, path string) error {
	if path == "" {
		return errors.New("mergedriver: no mergiraf path to register")
	}
	if err := setConfig(ctx, dir, "merge."+Name+".name", Name); err != nil {
		return err
	}
	if err := setConfig(ctx, dir, "merge."+Name+".driver", Command(path)); err != nil {
		return err
	}
	gitDir, err := git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	gitDir = strings.TrimSpace(gitDir)
	// --git-common-dir is relative to dir (typically ".git") in a normal
	// checkout, but ABSOLUTE — pointing at the main worktree's .git,
	// elsewhere on disk entirely — when dir is itself a linked worktree (a
	// session folder, or a pipeline workspace made with `git worktree
	// add`). Joining an absolute gitDir onto dir would silently produce a
	// bogus nested path git never reads, making this registration a no-op
	// without ever returning an error (the same trap pkg/githook fixed).
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return err
	}
	attrPath := filepath.Join(infoDir, "attributes")
	raw, err := os.ReadFile(attrPath)
	if err == nil && strings.Contains(string(raw), "merge="+Name) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(attrPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	prefix := ""
	if len(raw) > 0 && !strings.HasSuffix(string(raw), "\n") {
		prefix = "\n"
	}
	_, err = f.WriteString(prefix + attributesRule + "\n")
	return err
}

// Registered returns the merge driver command registered for mergiraf in
// dir's repository, or "" when there is none.
func Registered(ctx context.Context, dir string) string {
	out, err := git(ctx, dir, "config", "--get", "merge."+Name+".driver")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Usable reports whether a registered driver command's binary (its first
// word) still exists. A driver left behind by an uninstalled mergiraf
// fails on every file both sides changed, which git reads as a conflict.
func Usable(driver string) bool {
	fields := strings.Fields(driver)
	if len(fields) == 0 {
		return false
	}
	_, err := exec.LookPath(fields[0])
	return err == nil
}

// PlainText is the `-c` config override that makes one git command merge
// with plain git's text merge even though mergiraf is registered:
// `git merge-file` is git's own three-way text merge, run as the driver,
// with git's conflict marker size. Used to ask "would plain git have
// conflicted here?" (git merge-tree with this override) and to keep an
// uninstalled mergiraf's dangling driver from failing a real merge.
func PlainText() []string {
	return []string{"-c", "merge." + Name + ".driver=git merge-file --marker-size=%L %A %O %B"}
}

// Version is mergiraf's own version string ("mergiraf 0.20.0"), or "" when
// it can't be read within a few seconds.
func Version(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

// setConfig writes key=value into dir's repository config unless it's
// already that. Another process writing the same config at the same moment
// makes git fail on config.lock rather than wait, so that one case is
// retried briefly.
func setConfig(ctx context.Context, dir, key, value string) error {
	if cur, err := git(ctx, dir, "config", "--get", key); err == nil && strings.TrimSpace(cur) == value {
		return nil
	}
	var err error
	for try := 0; try < 5; try++ {
		var out string
		out, err = git(ctx, dir, "config", key, value)
		if err == nil || !strings.Contains(out, "lock") {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(try+1) * 50 * time.Millisecond):
		}
	}
	return err
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
