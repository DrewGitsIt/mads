package sessionwt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// SaveEditsMessage is the subject of the commit SaveEdits makes.
const SaveEditsMessage = "Save session edits before pipeline merge"

// DefaultAgentPoll is how often WaitIdle rereads the session record.
const DefaultAgentPoll = 2 * time.Second

// TaskMerge is the session side of D8: a pipeline merging its task branches
// into an isolated session's folder rather than the user's checkout. The
// engine drives the merge itself; this supplies what is particular to a
// session folder — its lock, the pre-merge overlap check, waiting for the
// agent, the safety commit, and the header counts afterwards.
//
// A task merge writes only the session folder, so it takes the session lock
// (Lock), never the repo lock: a session's pipeline must not queue behind
// every Land in the repo.
type TaskMerge struct {
	Store Store
	// Target is the session's, from TargetFor; it must be Isolated.
	Target Target
	// Poll is how often WaitIdle rereads the session record. Zero means
	// DefaultAgentPoll.
	Poll time.Duration
}

// Lock takes the session lock for a merge into the folder.
func (m *TaskMerge) Lock(ctx context.Context) (release func(), err error) {
	return AcquireSessionLock(ctx, m.Target.Primary, m.Target.SessionID)
}

// Overlap is the set of paths a merge of branch into folder would refuse
// over, worked out before merging rather than read from git's error (D8):
// what `git status` reports in the folder, staged, unstaged or untracked,
// intersected with what branch changed since it left folder's HEAD
// (`git diff --name-only HEAD...branch`). Both sides of a rename count.
// Sorted. Read-only; call it under the session lock so the answer still
// holds when the merge runs.
func Overlap(ctx context.Context, folder, branch string) ([]string, error) {
	raw, err := Git(ctx, folder, "--no-optional-locks", "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	dirty := map[string]bool{}
	fields := strings.Split(raw, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		dirty[f[3:]] = true
		if (f[0] == 'R' || f[0] == 'C') && i+1 < len(fields) {
			i++
			dirty[fields[i]] = true
		}
	}
	if len(dirty) == 0 {
		return nil, nil
	}
	changed, err := Git(ctx, folder, "diff", "--name-only", "-z", "HEAD..."+branch)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range strings.Split(changed, "\x00") {
		if p != "" && dirty[p] {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// AgentBusy reads whether the session's agent is mid-turn, from the
// agent_state its runner mirrors onto the record. No state at all (a
// session that never ran, or predates the mirror) counts as idle.
func (m *TaskMerge) AgentBusy(ctx context.Context) (bool, error) {
	sess, err := m.Store.Session(ctx, m.Target.SessionID)
	if err != nil {
		return false, err
	}
	return sess.AgentState != nil && sess.AgentState.State == contracts.AgentWorking, nil
}

// WaitIdle polls AgentBusy until the agent is idle or deadline passes,
// reporting which. The caller holds no lock while it waits, so Commit and
// Land stay usable for the minutes this can take.
func (m *TaskMerge) WaitIdle(ctx context.Context, deadline time.Time) (idle bool, err error) {
	poll := m.Poll
	if poll <= 0 {
		poll = DefaultAgentPoll
	}
	for {
		busy, err := m.AgentBusy(ctx)
		if err != nil {
			return false, err
		}
		if !busy {
			return true, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(min(poll, left)):
		}
	}
}

// WaitSettled polls the folder until it is no longer mid-merge, rebase,
// cherry-pick or revert (MidOperation) or deadline passes, returning the
// operation still under way then, or "" once there is none. Such a state
// is the agent's (finishing a Land conflict, say), so a task merge waits
// it out and never aborts it. Like WaitIdle, the caller holds no lock.
func (m *TaskMerge) WaitSettled(ctx context.Context, deadline time.Time) (op string, err error) {
	poll := m.Poll
	if poll <= 0 {
		poll = DefaultAgentPoll
	}
	for {
		if op = MidOperation(ctx, m.Target.Folder); op == "" {
			return "", nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			return op, nil
		}
		select {
		case <-ctx.Done():
			return op, ctx.Err()
		case <-time.After(min(poll, left)):
		}
	}
}

// SaveEdits commits files (relative to the folder's top) as they stand in
// the folder, so a task merge that touches them can run (D8). Only those
// files: anything else staged stays staged and out of the commit.
//
// It is an automatic commit, so it carries the mads-session safety
// identity (Git), never the user's, with a Session: trailer. It skips
// signing and pre-commit hooks, which could otherwise prompt no one or
// refuse the agent's half-finished edits and stall the run. The mads
// post-commit hook is silenced (mads.sessionId blanked, as userGit does)
// and the commit recorded here instead, so it is recorded exactly once.
// The caller holds the session lock.
func (m *TaskMerge) SaveEdits(ctx context.Context, files []string) (hash string, err error) {
	folder := m.Target.Folder
	var present, gone []string
	for _, p := range files {
		if _, err := os.Lstat(filepath.Join(folder, filepath.FromSlash(p))); os.IsNotExist(err) {
			gone = append(gone, p)
		} else {
			present = append(present, p)
		}
	}
	// As in CommitFiles: a deletion stages through rm --cached, since add -A
	// refuses a path in neither the index nor the tree.
	if len(gone) > 0 {
		if _, err := Git(ctx, folder, append([]string{"--literal-pathspecs", "rm", "--cached", "--ignore-unmatch", "-q", "--"}, gone...)...); err != nil {
			return "", err
		}
	}
	if len(present) > 0 {
		if _, err := Git(ctx, folder, append([]string{"--literal-pathspecs", "add", "-A", "--"}, present...)...); err != nil {
			return "", err
		}
	}
	args := append([]string{
		"-c", "mads.sessionId=", "-c", "commit.gpgsign=false", "--literal-pathspecs",
		"commit", "--no-verify", "--only", "-m", SaveEditsMessage, "-m", trailer(m.Target.SessionID), "--",
	}, files...)
	if _, err := Git(ctx, folder, args...); err != nil {
		return "", fmt.Errorf("save session edits: %w", err)
	}
	out, err := Git(ctx, folder, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	hash = strings.TrimSpace(out)
	Ops{Store: m.Store}.recordAs(ctx, m.Target, hash, SaveEditsMessage, provenance{
		source: contracts.CommitSourcePipelineSave, files: len(files),
	})
	return hash, nil
}

// Refresh recomputes and publishes the session's header counts (D4) after a
// merge moved its branch.
func (m *TaskMerge) Refresh(ctx context.Context) error {
	return RefreshCounts(context.WithoutCancel(ctx), m.Store, m.Target)
}
