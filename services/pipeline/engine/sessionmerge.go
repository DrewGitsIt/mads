package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/pkg/template"
)

// maxSessionMergeRounds bounds how often one land attempt into a session
// folder goes back to rebase and gate again because the session branch
// moved under it. The last round merges into whatever the branch is by
// then; a conflict there aborts like any other (merge).
const maxSessionMergeRounds = 3

// sessionMerge is one land attempt's state across its rounds of
// mergeIntoSession.
type sessionMerge struct {
	// deadline is when waiting for the agent gives up: the merge stage's
	// timeout from the start of the attempt.
	deadline time.Time
	// waitedOut is set once the agent was still working at deadline. The
	// merge then goes ahead regardless, and isn't held up again.
	waitedOut bool
	// saved and commits are the overlapping files SaveEdits committed, and
	// its commits.
	saved   []string
	commits []string
	// changed is what the merge changed in the folder: the files that
	// moved under a working agent, when waitedOut.
	changed []string
}

// annotate adds the session side of a land to its merge.completed payload:
// the auto-committed files and commits, and — when the merge went ahead
// with the agent still working — that it did, and which files changed
// under it, for the conversation card telling the user so.
func (s *sessionMerge) annotate(p map[string]any) {
	if len(s.saved) > 0 {
		p["auto_committed"] = s.saved
		p["auto_commits"] = s.commits
	}
	if s.waitedOut {
		p["waited_out"] = true
		p["files"] = s.changed
	}
}

// mergeIntoSession is landAttempt's merge step for a session folder (D8),
// under the session lock:
//
//  0. If the folder is mid-merge or rebase (the agent finishing a Land
//     conflict, say), release the lock and wait for that to settle, up to
//     s.deadline; past it, fail. That state is the agent's: a task merge
//     on top of it would fail and its abort would throw the agent's
//     resolution away.
//  1. If the session branch moved since the gate ran against trunkHead,
//     release and ask for another round (again), so the task branch takes
//     the move in through its own worktree first.
//  2. Overlap: files dirty in the folder that the task branch also changed,
//     which `git merge` would refuse over (and then `merge --abort` would
//     fail too). If there are any and the agent is working, release the
//     lock and wait for it to go idle, up to s.deadline, then start over.
//  3. Commit the overlapping files (SaveEdits). The branch moved, so ask for
//     another round, as in 1.
//  4. Merge.
//
// last skips the "another round" exits: the merge runs on whatever the
// branch is by then.
func (e *engine) mergeIntoSession(ctx context.Context, st template.Stage, task contracts.Task, run *taskRun, trunkHead string, last bool, s *sessionMerge) (again bool, err error) {
	sm := e.cfg.Session
	folder := e.wt.primary
	for {
		release, err := sm.Lock(ctx)
		if err != nil {
			return false, err
		}
		if op := sessionwt.MidOperation(ctx, folder); op != "" {
			release()
			still, werr := sm.WaitSettled(ctx, s.deadline)
			if werr != nil {
				return false, werr
			}
			if still != "" {
				return false, fmt.Errorf("session folder is mid-%s: the agent is in the middle of a %s there, and a task merge on top of it would undo that work; merge %s once it finishes", still, still, run.branch)
			}
			continue
		}
		head, err := e.wt.git(ctx, folder, "rev-parse", "HEAD")
		if err != nil {
			release()
			return false, err
		}
		if strings.TrimSpace(head) != trunkHead && !last {
			release()
			return true, nil
		}
		overlap, err := sessionwt.Overlap(ctx, folder, run.branch)
		if err != nil {
			release()
			return false, err
		}
		if len(overlap) > 0 && !s.waitedOut {
			busy, berr := sm.AgentBusy(ctx)
			if berr != nil {
				// Not knowing is treated as idle: the edits get saved
				// either way, and a Data Service blip shouldn't stall
				// the run.
				log.Printf("engine: read agent state for %s: %v", e.cfg.SessionID, berr)
			}
			if busy {
				release()
				idle, werr := sm.WaitIdle(ctx, s.deadline)
				if werr != nil {
					return false, werr
				}
				s.waitedOut = !idle
				continue
			}
		}
		if len(overlap) > 0 {
			hash, err := sm.SaveEdits(ctx, overlap)
			if err != nil {
				release()
				return false, err
			}
			s.saved = append(s.saved, overlap...)
			s.commits = append(s.commits, hash)
			if !last {
				release()
				return true, nil
			}
		}
		before, err := e.wt.git(ctx, folder, "rev-parse", "HEAD")
		if err != nil {
			release()
			return false, err
		}
		err = e.wt.merge(ctx, st.Command, task, run.branch, trunkHead)
		if err == nil && s.waitedOut {
			out, _ := e.wt.git(ctx, folder, "diff", "--name-only", strings.TrimSpace(before)+"..HEAD")
			s.changed = splitLines(out)
		}
		release()
		return false, err
	}
}
