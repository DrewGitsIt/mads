package sessionwt

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/drewkidwell/mads/pkg/mergedriver"
)

// findMergiraf is the mergiraf binary Land merges through, or "" when it
// isn't installed. Looked up per Land, so installing mergiraf (or the e2e
// suite's $MADS_MERGIRAF stand-in) takes effect without a restart. A test
// seam: the package's tests pin it so a developer's own mergiraf can't
// change what a scratch-repo merge does.
var findMergiraf = func() string { return mergedriver.Find("") }

// cleaned remembers which repositories (by shared git dir) have had what
// earlier builds registered removed, so the many ensures a session makes
// cost one check per repo per process.
var cleaned sync.Map // common dir -> struct{}

// cleanupMergeDriver removes, once per repo per process, the mergiraf
// registration earlier mads builds wrote into primary's repository
// (mergedriver.Cleanup): mergiraf now applies only to the merges mads runs,
// via mergirafArgs, never to the user's own. Best-effort and logged.
func cleanupMergeDriver(ctx context.Context, primary string) {
	common, err := commonDir(ctx, primary)
	if err != nil {
		return
	}
	if _, ok := cleaned.Load(common); ok {
		return
	}
	removed, err := mergedriver.Cleanup(ctx, primary)
	if err != nil {
		log.Printf("sessionwt: clean up mergiraf registration in %s: %v", primary, err)
		return
	}
	if len(removed) > 0 {
		log.Printf("sessionwt: removed the mergiraf registration an earlier mads wrote to %s: %s", primary, strings.Join(removed, ", "))
	}
	cleaned.Store(common, struct{}{})
}

// mergirafArgs is the `-c` flags that route one of Land's merges in dir
// through mergiraf (mergedriver.Args), or nil — plain git — when mergiraf
// isn't installed or the flags can't be prepared. The attributes file they
// name lives in $MADS_HOME/mergiraf; t.Root is $MADS_HOME/session-worktrees.
func mergirafArgs(ctx context.Context, t Target, dir string) []string {
	path := findMergiraf()
	if path == "" || t.Root == "" {
		return nil
	}
	args, err := mergedriver.Args(ctx, dir, mergedriver.StateDir(filepath.Dir(t.Root)), path)
	if err != nil {
		log.Printf("sessionwt: merging in %s without mergiraf: %v", dir, err)
		return nil
	}
	return args
}

// plainConflicts is what plain git would leave conflicted merging rev into
// dir's HEAD: a dry run (`git merge-tree --write-tree`, which touches
// neither the index nor the working tree) without mads's mergiraf flags,
// and with any mergiraf driver the repository configures itself swapped
// for git's own text merge. Run before the real merge, it names the files
// only mergiraf could have merged: if the real merge then comes back
// clean, every file listed here is one mergiraf resolved. ok is false when the dry run couldn't answer (git before 2.38
// has no --write-tree), and the caller carries on as if mergiraf weren't
// there.
func plainConflicts(ctx context.Context, dir, rev string) (files []string, ok bool) {
	args := append(mergedriver.PlainText(), "merge-tree", "--write-tree", "--name-only", "-z", "HEAD", rev)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil, true
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		// Conflicts. -z output: the tree's id, then each conflicted path,
		// each NUL-terminated, then an empty field before git's messages.
		fields := strings.Split(stdout.String(), "\x00")
		if len(fields) < 2 {
			return nil, false
		}
		seen := map[string]bool{}
		for _, f := range fields[1:] {
			if f == "" {
				break
			}
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
		return files, true
	default:
		log.Printf("sessionwt: dry-run merge of %s in %s: %v", rev, dir, err)
		return nil, false
	}
}
