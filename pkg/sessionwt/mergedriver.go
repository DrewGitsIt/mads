package sessionwt

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os/exec"
	"strings"
	"sync"

	"github.com/drewkidwell/mads/pkg/mergedriver"
)

// findMergiraf is the mergiraf binary session worktrees register, or ""
// when it isn't installed. A test seam: the package's tests pin it so a
// developer's own mergiraf can't change what a scratch-repo merge does.
var findMergiraf = func() string { return mergedriver.Find("") }

// registered remembers which repositories (by shared git dir) already have
// which mergiraf registered, so the many ensures a session makes cost one
// git call each after the first.
var registered sync.Map // common dir -> mergiraf path

// registerMergeDriver registers mergiraf, when it's installed, as the merge
// driver for primary's repository, so Land's step 1 gets it in every repo a
// session works in, not only ones a pipeline has touched. Best-effort and
// logged: a session works the same without it, just with plain git merges.
func registerMergeDriver(ctx context.Context, primary string) {
	path := findMergiraf()
	if path == "" {
		return
	}
	common, err := commonDir(ctx, primary)
	if err != nil {
		return
	}
	if p, ok := registered.Load(common); ok && p == path {
		return
	}
	if err := mergedriver.Register(ctx, primary, path); err != nil {
		log.Printf("sessionwt: register mergiraf for %s: %v", primary, err)
		return
	}
	registered.Store(common, path)
}

// mergeDriverState is how mergiraf stands in a session folder's repository
// for one merge.
type mergeDriverState int

const (
	driverNone     mergeDriverState = iota // not registered: plain git
	driverActive                           // registered and installed
	driverDangling                         // registered, but its binary is gone
)

func mergeDriverIn(ctx context.Context, dir string) mergeDriverState {
	driver := mergedriver.Registered(ctx, dir)
	switch {
	case driver == "":
		return driverNone
	case mergedriver.Usable(driver):
		return driverActive
	default:
		return driverDangling
	}
}

// plainConflicts is what plain git would leave conflicted merging rev into
// dir's HEAD: a dry run (`git merge-tree --write-tree`, which touches
// neither the index nor the working tree) with the mergiraf driver swapped
// for git's own text merge. Run before a real merge that does use
// mergiraf, it names the files only mergiraf could have merged: if the real
// merge then comes back clean, every file listed here is one mergiraf
// resolved. ok is false when the dry run couldn't answer (git before 2.38
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
