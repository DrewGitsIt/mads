package sessionwt

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Spec names one session's isolation. Ensure derives the folder and branch
// from Root and SessionID alone; the session record is never consulted, so a
// stale or half-written record can't send it somewhere else (D2).
type Spec struct {
	// Primary is any directory inside the user's checkout — the repo the
	// worktree is registered against.
	Primary string
	// Root is where session folders live ($MADS_HOME/session-worktrees).
	Root      string
	SessionID string
	// Base is the branch a brand-new session branch starts from: the
	// session's WorktreeBase. Empty means the primary's HEAD.
	Base string
}

// Action is what Ensure had to do to produce a usable folder.
type Action string

const (
	// ActionReused: the folder was already a worktree of this repo.
	ActionReused Action = "reused"
	// ActionReattached: the folder was gone but the branch survived, so it
	// was checked out again at the same path. Uncommitted edits are lost;
	// commits are not.
	ActionReattached Action = "reattached"
	// ActionCreated: neither folder nor branch existed; both were made.
	ActionCreated Action = "created"
)

// EnsureResult says what Ensure found and did, for the caller to put on a
// session event: every case but a plain reuse is one the user should see
// (D2, "nothing heals silently").
type EnsureResult struct {
	Dir    string `json:"dir"`
	Branch string `json:"branch"`
	Action Action `json:"action"`
	// StaleMovedTo is set when something that wasn't this session's
	// worktree sat at Dir and was moved aside to this path, not deleted.
	StaleMovedTo string `json:"stale_moved_to,omitempty"`
	// UnlockedStale is set when a locked registration for the lost folder
	// had to be unlocked before git would let the path be reused.
	UnlockedStale bool `json:"unlocked_stale,omitempty"`
	// BaseMissing is set when Spec.Base no longer exists, so a new branch
	// started from the primary's HEAD instead.
	BaseMissing bool `json:"base_missing,omitempty"`
	// StartPoint is the commit a created branch started from.
	StartPoint string `json:"start_point,omitempty"`
	// Head is set on a reused folder that isn't on Branch: the branch it is
	// on, or "HEAD" when detached (say, mid-rebase). The folder is still
	// used — moving aside a live worktree because the agent ran `git
	// checkout` in it would be the worse surprise.
	Head string `json:"head,omitempty"`
}

// Notable reports whether the result is worth a card: anything but a clean
// reuse.
func (r EnsureResult) Notable() bool {
	return r.Action != ActionReused || r.StaleMovedTo != "" || r.Head != ""
}

// Ensure makes the session's folder usable, following D2's table:
//
//   - the folder is a worktree of this repo: use it;
//   - folder gone, branch present: prune, unlock a locked stale
//     registration, and check the branch out again at the same path, so a
//     provider that resumes by folder (claude-code) still finds its
//     conversation;
//   - something else is at the path: move it to <path>.stale-<unix-ts>,
//     then recreate as below;
//   - both gone: `worktree add -b <branch> <path> <Base>`, falling back to
//     the primary's HEAD when Base is gone.
//
// The caller holds the session lock (WithSessionLock / EnsureWithLock).
// Ensure never touches the primary checkout's files, so it doesn't need the
// repo lock and doesn't wait behind a land.
func Ensure(ctx context.Context, s Spec) (EnsureResult, error) {
	res, ok, err := reuse(ctx, s)
	if err != nil || ok {
		return res, err
	}
	root := filepath.Dir(res.Dir)

	switch _, err := os.Lstat(res.Dir); {
	case err == nil:
		stale, err := moveAside(res.Dir)
		if err != nil {
			return EnsureResult{}, err
		}
		res.StaleMovedTo = stale
	case !errors.Is(err, fs.ErrNotExist):
		return EnsureResult{}, err
	}

	// The folder is gone now. A registration that outlived it would make
	// `worktree add` refuse the path, or refuse the branch as already
	// checked out; prune clears it, except a locked one, which it skips.
	res.UnlockedStale, err = unlockRegistration(ctx, s.Primary, res.Dir)
	if err != nil {
		return EnsureResult{}, err
	}
	if _, err := Git(ctx, s.Primary, "worktree", "prune"); err != nil {
		return EnsureResult{}, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return EnsureResult{}, err
	}

	if refExists(ctx, s.Primary, "refs/heads/"+res.Branch) {
		if _, err := Git(ctx, s.Primary, "worktree", "add", res.Dir, res.Branch); err != nil {
			return EnsureResult{}, err
		}
		res.Action = ActionReattached
		return res, nil
	}

	// The start point is always explicit: the primary may have moved to
	// another branch since the session recorded its base.
	start := "HEAD"
	if s.Base != "" {
		if refExists(ctx, s.Primary, "refs/heads/"+s.Base) {
			start = "refs/heads/" + s.Base
		} else {
			res.BaseMissing = true
		}
	}
	sha, err := Git(ctx, s.Primary, "rev-parse", "--verify", start+"^{commit}")
	if err != nil {
		return EnsureResult{}, err
	}
	res.StartPoint = strings.TrimSpace(sha)
	if _, err := Git(ctx, s.Primary, "worktree", "add", "-b", res.Branch, res.Dir, res.StartPoint); err != nil {
		return EnsureResult{}, err
	}
	res.Action = ActionCreated
	return res, nil
}

// EnsureWithLock is Ensure for a caller that doesn't hold the session lock.
// A folder that is already a worktree of the repo is used without taking
// it, since checking is read-only; anything that needs repair waits for
// the session lock (never the repo lock).
func EnsureWithLock(ctx context.Context, s Spec) (EnsureResult, error) {
	if res, ok, err := reuse(ctx, s); err == nil && ok {
		return res, nil
	}
	var res EnsureResult
	err := WithSessionLock(ctx, s.Primary, s.SessionID, func() error {
		var err error
		res, err = Ensure(ctx, s)
		return err
	})
	return res, err
}

// reuse is the first row of D2's table: ok when the deterministic folder is
// already a worktree of this repo. Otherwise res carries the folder and
// branch for the caller to repair.
func reuse(ctx context.Context, s Spec) (res EnsureResult, ok bool, err error) {
	if s.SessionID == "" || s.Root == "" || s.Primary == "" {
		return EnsureResult{}, false, errors.New("sessionwt: ensure needs Primary, Root and SessionID")
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return EnsureResult{}, false, err
	}
	res = EnsureResult{Dir: Path(root, s.SessionID), Branch: Branch(s.SessionID)}
	common, err := commonDir(ctx, s.Primary)
	if err != nil {
		return EnsureResult{}, false, err
	}
	head, ok := isWorktreeOf(ctx, common, res.Dir)
	if !ok {
		return res, false, nil
	}
	res.Action = ActionReused
	if head != res.Branch {
		res.Head = head
	}
	return res, true, nil
}

// isWorktreeOf reports whether dir is the top of a working tree belonging to
// the repo whose shared .git dir is common, and which branch it has checked
// out ("HEAD" when detached). A folder nested inside some other repo, or a
// worktree whose registration was pruned, is not.
func isWorktreeOf(ctx context.Context, common, dir string) (head string, ok bool) {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", false
	}
	top, err := Toplevel(ctx, dir)
	if err != nil || !samePath(top, dir) {
		return "", false
	}
	c, err := commonDir(ctx, dir)
	if err != nil || !samePath(c, common) {
		return "", false
	}
	head, err = CurrentBranch(ctx, dir)
	if errors.Is(err, ErrDetachedHEAD) {
		return "HEAD", true
	}
	if err != nil {
		return "", false
	}
	return head, true
}

// moveAside renames whatever is at path to a sibling <path>.stale-<unix-ts>,
// adding a counter if that name is taken too. Never deletes: it may be the
// user's own files.
func moveAside(path string) (string, error) {
	base := fmt.Sprintf("%s.stale-%d", path, time.Now().Unix())
	dest := base
	for i := 1; ; i++ {
		if _, err := os.Lstat(dest); errors.Is(err, fs.ErrNotExist) {
			break
		}
		dest = fmt.Sprintf("%s-%d", base, i)
	}
	if err := os.Rename(path, dest); err != nil {
		return "", fmt.Errorf("move aside %s: %w", path, err)
	}
	return dest, nil
}

// unlockRegistration unlocks any locked worktree registration for path, so
// the prune that follows can drop it. Reports whether it unlocked one.
func unlockRegistration(ctx context.Context, primary, path string) (bool, error) {
	out, err := Git(ctx, primary, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	unlocked := false
	for _, entry := range strings.Split(out, "\n\n") {
		var wt string
		var locked bool
		for _, line := range strings.Split(entry, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				wt = strings.TrimPrefix(line, "worktree ")
			case line == "locked" || strings.HasPrefix(line, "locked "):
				locked = true
			}
		}
		if !locked || wt == "" || !samePath(wt, path) {
			continue
		}
		if _, err := Git(ctx, primary, "worktree", "unlock", wt); err != nil {
			return unlocked, err
		}
		unlocked = true
	}
	return unlocked, nil
}

// refExists reports whether a fully qualified ref resolves to a commit.
func refExists(ctx context.Context, dir, ref string) bool {
	_, err := Git(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}
