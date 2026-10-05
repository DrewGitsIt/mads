// Package mergedriver finds mergiraf (https://mergiraf.org), a syntax-aware
// git merge driver, and routes mads's own merges through it. Mergiraf is an
// optional runtime tool, never a build dependency: when it isn't installed,
// every caller carries on with plain git merging and nothing else changes.
//
// Two processes use it, against the same repositories: the pipeline engine,
// whose land queue merges parallel task branches, and session worktrees
// (pkg/sessionwt), whose Land merges the base into a session's branch.
//
// Mergiraf applies only to merges mads itself runs, never to the user's own
// `git merge` or `git rebase` in the same repository. So nothing is written
// to the repository: each merge command mads runs carries the driver as
// `-c` flags (Args) — the `merge.mergiraf.*` driver definition plus
// core.attributesFile pointing at a mads-owned attributes file holding
// `* merge=mergiraf`. That is mergiraf's own documented integration
// (mergiraf.org/usage.html), scoped to one command instead of the repo.
//
// Precedence: core.attributesFile is git's lowest-priority attributes
// source, below the repository's .gitattributes files and
// <git-common-dir>/info/attributes. A repository that sets its own merge=
// attribute for a path keeps it in mads's merges too; mergiraf only fills
// in where the repository says nothing. The user's own global attributes
// file (their core.attributesFile, else $XDG_CONFIG_HOME/git/attributes),
// which the override would otherwise hide, is copied into mads's file after
// the mergiraf rule, so it still applies and its merge= lines still win.
//
// Earlier builds did register mergiraf in the repository's shared git
// state; Cleanup removes exactly what they wrote.
package mergedriver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/binlocate"
)

// Name is the git merge driver name mergiraf runs under.
const Name = "mergiraf"

// EnvPath names the environment variable that, when set, is the mergiraf
// binary to use instead of a PATH lookup, with the same rules as a
// configured path: unusable means plain git. The e2e suite uses it to put a
// stand-in in front of the services it boots.
const EnvPath = "MADS_MERGIRAF"

// Find returns the mergiraf binary to use, or "" when there is none.
// A configured path (or $MADS_MERGIRAF, when nothing is configured) is used
// only if it resolves to an executable: a configured-but-unusable path
// means plain git, the same as none at all, and never falls through to a
// PATH lookup (so a test or a user can force plain git by pointing it
// somewhere empty). With nothing configured, it's a PATH lookup plus the
// documented install locations binlocate knows (Homebrew's prefixes,
// ~/.cargo/bin), which matters for an app launched from a desktop whose
// PATH lacks them.
func Find(configured string) string {
	if configured == "" {
		configured = os.Getenv(EnvPath)
	}
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

// commandArgs is the argument template git hands the driver. The %S/%X/%Y
// label placeholders are only substituted by git 2.44+; on older git they
// pass through to mergiraf as literal text, which is harmless (mergiraf
// only uses them as conflict-marker labels on a hunk it can't resolve) —
// see mergiraf.org/usage.html#with-a-driver.
const commandArgs = " merge --git %O %A %B -s %S -x %X -y %Y -p %P -l %L"

// Command is the merge driver command for the binary at path. Git runs it
// through the shell, so a path with anything but plain characters in it is
// single-quoted.
func Command(path string) string {
	return shellQuote(path) + commandArgs
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// attributesRule is the gitattributes line that sends every file through
// the driver; mergiraf itself falls back to a line-based merge for file
// types it doesn't know.
const attributesRule = "* merge=" + Name

// StateDir is where mads keeps its mergiraf attributes files under a mads
// home ($MADS_HOME/mergiraf).
func StateDir(madsHome string) string {
	return filepath.Join(madsHome, "mergiraf")
}

// Args is the `-c` flags that make one git command in dir's repository
// merge through mergiraf at path, to go between `git` and the subcommand.
// They define the driver and point core.attributesFile at a mads-owned
// attributes file in stateDir: the mergiraf rule followed by the user's own
// global attributes, if they have any. That file is named for its content
// and written once (atomically, so concurrent callers are harmless);
// repeats only read. Nothing in the repository is written.
func Args(ctx context.Context, dir, stateDir, path string) ([]string, error) {
	if path == "" {
		return nil, errors.New("mergedriver: no mergiraf path")
	}
	if stateDir == "" {
		return nil, errors.New("mergedriver: no state dir for the attributes file")
	}
	attrs, err := attributesFile(ctx, dir, stateDir)
	if err != nil {
		return nil, err
	}
	return []string{
		"-c", "merge." + Name + ".name=" + Name,
		"-c", "merge." + Name + ".driver=" + Command(path),
		"-c", "core.attributesFile=" + attrs,
	}, nil
}

// attributesFile writes (if missing) and returns the attributes file Args
// points core.attributesFile at.
func attributesFile(ctx context.Context, dir, stateDir string) (string, error) {
	var b strings.Builder
	b.WriteString("# Written by mads (pkg/mergedriver). Used only as core.attributesFile on\n")
	b.WriteString("# merges mads itself runs, so they go through mergiraf; never read by your own git.\n")
	b.WriteString(attributesRule + "\n")
	if src, user := userAttributes(ctx, dir); user != "" {
		b.WriteString("# Your global attributes, from " + src + ", copied so mads's merges keep them:\n")
		b.WriteString(user)
		if !strings.HasSuffix(user, "\n") {
			b.WriteString("\n")
		}
	}
	content := b.String()
	sum := sha256.Sum256([]byte(content))
	file := filepath.Join(stateDir, "attributes-"+hex.EncodeToString(sum[:8]))
	if raw, err := os.ReadFile(file); err == nil && string(raw) == content {
		return file, nil
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", err
	}
	if err := writeAtomic(file, content); err != nil {
		return "", err
	}
	return file, nil
}

// userAttributes is the content of the global attributes file git would
// read in dir — core.attributesFile, else $XDG_CONFIG_HOME/git/attributes
// (or ~/.config/git/attributes) — and where it came from; "" when there's
// none.
func userAttributes(ctx context.Context, dir string) (src, content string) {
	if out, err := git(ctx, dir, "config", "--type=path", "--get", "core.attributesFile"); err == nil {
		src = strings.TrimSpace(out)
	} else if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		src = filepath.Join(x, "git", "attributes")
	} else if home, err := os.UserHomeDir(); err == nil {
		src = filepath.Join(home, ".config", "git", "attributes")
	}
	if src == "" {
		return "", ""
	}
	raw, err := os.ReadFile(src)
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return "", ""
	}
	return src, string(raw)
}

// PlainText is the `-c` config override that makes one git command merge
// with plain git's text merge wherever a mergiraf driver would otherwise
// run: `git merge-file` is git's own three-way text merge, run as the
// driver, with git's conflict marker size. Land uses it to ask "would plain
// git have conflicted here?" (git merge-tree with this override), which
// also neutralises a mergiraf the user registered in the repository
// themselves.
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

// isLegacyDriver reports whether a merge.mergiraf.driver value is one an
// earlier mads build wrote: an absolute binary path followed by mads's
// argument template, unquoted. Mergiraf's own instructions have users write
// a bare `mergiraf` instead, so a user's registration done by the book is
// never mistaken for mads's.
func isLegacyDriver(v string) bool {
	p, ok := strings.CutSuffix(v, commandArgs)
	return ok && filepath.IsAbs(p)
}

// Cleanup removes from dir's repository what earlier mads builds wrote to
// register mergiraf there, and nothing else:
//
//   - the repository config's `merge.mergiraf.driver` when its value is
//     mads's template (isLegacyDriver), with `merge.mergiraf.name` when
//     that is "mergiraf" (the whole section when those two are all it
//     holds);
//   - every `* merge=mergiraf` line in <git-common-dir>/info/attributes,
//     when either the driver just removed was mads's or no mergiraf driver
//     is configured anywhere any more (a rule naming no driver does
//     nothing for the user, and is the mads line whose config was cleaned
//     already). A line next to a mergiraf driver the user configured
//     themselves (globally, say) is kept: it may be theirs.
//
// It reports what it removed. Cheap and idempotent: with nothing to remove
// it only reads (two git calls and one file), never taking config.lock.
// dir may be the main checkout or any linked worktree of it: config is
// shared, and the common dir is resolved to an absolute path either way.
func Cleanup(ctx context.Context, dir string) (removed []string, err error) {
	keys := map[string]string{}
	if out, err := git(ctx, dir, "config", "--local", "--get-regexp", `^merge\.`+Name+`\.`); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			k, v, _ := strings.Cut(line, " ")
			keys[strings.ToLower(k)] = v
		}
	}
	driverKey, nameKey := "merge."+Name+".driver", "merge."+Name+".name"
	madsConfig := isLegacyDriver(keys[driverKey])
	if madsConfig {
		drop := []string{driverKey}
		if keys[nameKey] == Name {
			drop = append(drop, nameKey)
		}
		if len(drop) == len(keys) {
			// Nothing else in the section: remove it, header and all.
			if err := configWrite(ctx, dir, "--remove-section", "merge."+Name); err != nil {
				return removed, err
			}
		} else {
			for _, k := range drop {
				if err := configWrite(ctx, dir, "--unset-all", k); err != nil {
					return removed, err
				}
			}
		}
		removed = append(removed, drop...)
	}

	common, err := git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return removed, err
	}
	common = strings.TrimSpace(common)
	// --git-common-dir is relative to dir (typically ".git") in a normal
	// checkout, but ABSOLUTE — the main worktree's .git, elsewhere on disk
	// entirely — when dir is itself a linked worktree (a session folder, or
	// a pipeline workspace made with `git worktree add`). Joining an
	// absolute path onto dir would silently name a bogus nested path git
	// never reads (the same trap pkg/githook fixed).
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	attrPath := filepath.Join(common, "info", "attributes")
	raw, err := os.ReadFile(attrPath)
	if errors.Is(err, os.ErrNotExist) {
		return removed, nil
	}
	if err != nil {
		return removed, err
	}
	var kept []string
	found := false
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		if strings.TrimSpace(line) == attributesRule {
			found = true
			continue
		}
		kept = append(kept, line)
	}
	if !found {
		return removed, nil
	}
	if !madsConfig {
		if _, err := git(ctx, dir, "config", "--get", driverKey); err == nil {
			return removed, nil // a driver of the user's own: the line may be theirs
		}
	}
	rest := strings.Join(kept, "")
	if strings.TrimSpace(rest) == "" {
		err = os.Remove(attrPath)
	} else {
		err = writeAtomic(attrPath, rest)
	}
	if err != nil {
		return removed, err
	}
	return append(removed, attrPath+": "+attributesRule), nil
}

// configWrite runs one `git config --local` write in dir. Another process
// writing the same config at the same moment makes git fail on config.lock
// rather than wait, so that one case is retried briefly; a key or section
// already gone (another cleanup got there first) is success.
func configWrite(ctx context.Context, dir string, args ...string) error {
	var err error
	for try := 0; try < 5; try++ {
		var out string
		out, err = git(ctx, dir, append([]string{"config", "--local"}, args...)...)
		if err == nil {
			return nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 5 {
			return nil // --unset of a key that isn't there
		}
		if strings.Contains(out, "no such section") {
			return nil
		}
		if !strings.Contains(out, "lock") {
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

// writeAtomic replaces path with content via a temporary file in the same
// directory and a rename, so a reader never sees it half-written.
func writeAtomic(path, content string) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.WriteString(content)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
