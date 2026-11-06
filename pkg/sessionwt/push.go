package sessionwt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Push (docs/COMMIT-LAND-PUSH-DESIGN.md D5): publish the branch the work is
// on to its remote — never forced — after a confirmation that lists what
// goes. When the remote has moved on, the push needs a rebase first, which
// runs only when the user chose it in that confirmation, and only where it
// can't disturb anyone: no agent working in the folder, nothing half-done,
// and no uncommitted file the incoming commits change. A rebase that
// conflicts is aborted, leaving the branch where it was.

// Network commands are bounded; a remote that never answers fails the push
// rather than hanging it.
const (
	fetchTimeout = 60 * time.Second
	pushTimeout  = 2 * time.Minute
)

// maxPushCommits and maxIncoming cap the confirmation's lists.
const (
	maxPushCommits = 50
	maxIncoming    = 20
)

// pushSpot is where a push runs and what it publishes.
type pushSpot struct {
	dir, branch string
}

// spotFor resolves a push target in t: the session's own branch from its
// worktree, its base from the user's checkout, or the branch checked out
// where it works in place. A refusal comes back as such.
func spotFor(ctx context.Context, t Target, target string) (pushSpot, *contracts.PushRefusal) {
	switch {
	case target == contracts.PushBase && !t.Isolated():
		return pushSpot{}, &contracts.PushRefusal{Reason: contracts.PushNotIsolated,
			Detail: "This session works directly in your checkout: there is no base to push separately."}
	case target == contracts.PushBase:
		return pushSpot{dir: t.Primary, branch: t.Base}, nil
	case t.Isolated():
		return pushSpot{dir: t.Folder, branch: t.Branch}, nil
	}
	branch, err := CurrentBranch(ctx, t.Folder)
	if err != nil {
		return pushSpot{}, &contracts.PushRefusal{Reason: contracts.PushDetached,
			Detail: t.Folder + " is on a detached HEAD: there's no branch to push."}
	}
	return pushSpot{dir: t.Folder, branch: branch}, nil
}

// RemoteFor is where branch in dir pushes: its upstream's remote and
// branch when it has one; else origin, else the only remote, under the
// same name. "" when the repo has no remote (or several, none of them
// origin). upstream reports whether the branch already tracks one.
func RemoteFor(ctx context.Context, dir, branch string) (remote, remoteBranch string, upstream bool) {
	if r, err := Git(ctx, dir, "config", "--get", "branch."+branch+".remote"); err == nil {
		if m, err := Git(ctx, dir, "config", "--get", "branch."+branch+".merge"); err == nil {
			r, m := strings.TrimSpace(r), strings.TrimPrefix(strings.TrimSpace(m), "refs/heads/")
			if r != "" && r != "." && m != "" {
				return r, m, true
			}
		}
	}
	out, err := Git(ctx, dir, "remote")
	if err != nil {
		return "", "", false
	}
	remotes := strings.Fields(out)
	for _, r := range remotes {
		if r == "origin" {
			return r, branch, false
		}
	}
	if len(remotes) == 1 {
		return remotes[0], branch, false
	}
	return "", "", false
}

// CountUnpushed counts the commits a push of branch would move its remote
// branch past, as last fetched — the same list the confirmation shows: the
// commits not on the remote branch, or for a branch the remote doesn't
// have yet, those on none of the remote's branches. 0 with no remote.
// Read-only and offline: the count is as fresh as the last fetch.
func CountUnpushed(ctx context.Context, dir, branch string) (int, error) {
	remote, remoteBranch, _ := RemoteFor(ctx, dir, branch)
	if remote == "" || branch == "" {
		return 0, nil
	}
	revs := []string{"refs/heads/" + branch, "--not", "--remotes=" + remote}
	if ref := "refs/remotes/" + remote + "/" + remoteBranch; refExists(ctx, dir, ref) {
		revs = []string{ref + "..refs/heads/" + branch}
	}
	out, err := Git(ctx, dir, append([]string{"rev-list", "--count"}, revs...)...)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// unpushedOf is a target's header count: what its next push publishes —
// the base for an isolated session (what Land moved), else the branch it
// works on.
func unpushedOf(ctx context.Context, t Target) (int, error) {
	if t.Isolated() {
		return CountUnpushed(ctx, t.Primary, t.Base)
	}
	branch, err := CurrentBranch(ctx, t.Folder)
	if err != nil {
		return 0, nil // detached: nothing to push
	}
	return CountUnpushed(ctx, t.Folder, branch)
}

// netGit is userGit for commands that reach a remote: bounded by timeout,
// and with every way of asking for credentials closed — no terminal
// prompt, no credential manager dialog, ssh in batch mode unless the user
// configured their own ssh command — so a sign-in failure comes back as
// git's message instead of a hang.
func netGit(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := append([]string{"-c", "mads.sessionId="}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	ownGroup(cmd)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_EDITOR=true", "GIT_MERGE_AUTOEDIT=no")
	if os.Getenv("GIT_SSH_COMMAND") == "" && os.Getenv("GIT_SSH") == "" {
		if own, _ := Git(ctx, dir, "config", "--get", "core.sshCommand"); strings.TrimSpace(own) == "" {
			cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return string(out), &GitError{Args: args, Output: strings.TrimSpace(string(out)) + "\n(timed out after " + timeout.String() + ")", Err: err}
		}
		return string(out), &GitError{Args: args, Output: string(out), Err: err}
	}
	return string(out), nil
}

// remoteState is a branch against its remote branch, after a fetch.
type remoteState struct {
	remote, remoteBranch string
	upstream, newBranch  bool
	remoteRef            string // refs/remotes/<remote>/<branch>; "" when NewBranch
	head                 string
	ahead, behind        int
	fetchErr             string
}

// readRemote fetches the remote branch and compares branch with it.
func readRemote(ctx context.Context, s pushSpot) (remoteState, *contracts.PushRefusal, error) {
	var st remoteState
	st.remote, st.remoteBranch, st.upstream = RemoteFor(ctx, s.dir, s.branch)
	if st.remote == "" {
		return st, &contracts.PushRefusal{Reason: contracts.PushNoRemote,
			Detail: "This repo has no remote to push to (or several, none of them origin)."}, nil
	}
	head, err := Git(ctx, s.dir, "rev-parse", "--verify", "refs/heads/"+s.branch+"^{commit}")
	if err != nil {
		return st, nil, err
	}
	st.head = strings.TrimSpace(head)
	if out, err := netGit(ctx, s.dir, fetchTimeout, "fetch", "--quiet", st.remote, st.remoteBranch); err != nil {
		st.fetchErr = gitOutput(out, err)
	}
	ref := "refs/remotes/" + st.remote + "/" + st.remoteBranch
	if !refExists(ctx, s.dir, ref) {
		st.newBranch = true
		n, err := CountUnpushed(ctx, s.dir, s.branch)
		if err != nil {
			return st, nil, err
		}
		st.ahead = n
		return st, nil, nil
	}
	st.remoteRef = ref
	out, err := Git(ctx, s.dir, "rev-list", "--left-right", "--count", "refs/heads/"+s.branch+"..."+ref)
	if err != nil {
		return st, nil, err
	}
	f := strings.Fields(out)
	if len(f) == 2 {
		st.ahead, _ = strconv.Atoi(f[0])
		st.behind, _ = strconv.Atoi(f[1])
	}
	return st, nil, nil
}

// commitsIn lists the commits in a rev-list range, newest first.
func commitsIn(ctx context.Context, dir string, limit int, revs ...string) []contracts.PushCommit {
	args := append([]string{"log", "--format=%H%x00%s", "-n", strconv.Itoa(limit)}, revs...)
	out, err := Git(ctx, dir, args...)
	if err != nil {
		return []contracts.PushCommit{}
	}
	list := []contracts.PushCommit{}
	for _, line := range lines(out) {
		if h, s, ok := strings.Cut(line, "\x00"); ok {
			list = append(list, contracts.PushCommit{Hash: h, Subject: s})
		}
	}
	return list
}

// rebaseGate is when "Rebase onto origin and push" may run (D5), in the
// order the user would want to hear about it. nil means it may.
func rebaseGate(ctx context.Context, s pushSpot, st remoteState, g Gates) (*contracts.PushRefusal, error) {
	refuse := func(reason, detail string, files ...string) (*contracts.PushRefusal, error) {
		return &contracts.PushRefusal{Reason: reason, Detail: detail, Files: files}, nil
	}
	switch {
	case g.Busy:
		return refuse(contracts.PushBusy, "An agent is working in "+s.dir+". Rebase when it finishes its turn.")
	case g.Pipeline:
		return refuse(contracts.PushPipeline, "A pipeline run is active for this session. Rebase when it finishes.")
	}
	if on, err := CurrentBranch(ctx, s.dir); err != nil || on != s.branch {
		return refuse(contracts.PushNotCheckedOut, s.dir+" doesn't have "+s.branch+" checked out, so it can't be rebased there.")
	}
	if op := midOperation(ctx, s.dir); op != "" {
		return refuse(contracts.PushMidOperation, s.dir+" is in the middle of a "+op+". Finish or abort it first.")
	}
	status, err := Git(ctx, s.dir, "--no-optional-locks", "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	dirty := map[string]bool{}
	for _, e := range parseStatusZ(status) {
		dirty[e.path] = true
	}
	if len(dirty) > 0 {
		in, err := Git(ctx, s.dir, "diff", "--name-only", "-z", "refs/heads/"+s.branch+"..."+st.remoteRef)
		if err != nil {
			return nil, err
		}
		var overlap []string
		for _, p := range strings.Split(in, "\x00") {
			if p != "" && dirty[p] {
				overlap = append(overlap, p)
			}
		}
		if len(overlap) > 0 {
			return refuse(contracts.PushOverlap, "The incoming commits change files that are uncommitted here. Commit or put them aside, then rebase.", overlap...)
		}
	}
	return nil, nil
}

// PushPreview is GET /sessions/{id}/push: fetch, then everything the
// confirmation shows. g gates only the rebase.
func PushPreview(ctx context.Context, t Target, target string, g Gates) (contracts.PushPreview, error) {
	if target == "" {
		target = contracts.PushBranch
	}
	p := contracts.PushPreview{Repo: t.Folder, Target: target, Commits: []contracts.PushCommit{}, Incoming: []contracts.PushCommit{}}
	s, refusal := spotFor(ctx, t, target)
	if refusal != nil {
		p.Blocked = refusal
		return p, nil
	}
	p.Dir, p.Branch = s.dir, s.branch
	st, refusal, err := readRemote(ctx, s)
	if err != nil {
		return p, err
	}
	p.Remote, p.RemoteBranch, p.NewBranch, p.Head, p.FetchError = st.remote, st.remoteBranch, st.newBranch, st.head, st.fetchErr
	if refusal != nil {
		p.Blocked = refusal
		return p, nil
	}
	p.Ahead, p.Behind = st.ahead, st.behind
	if st.newBranch {
		p.Commits = commitsIn(ctx, s.dir, maxPushCommits, "refs/heads/"+s.branch, "--not", "--remotes="+st.remote)
	} else {
		p.Commits = commitsIn(ctx, s.dir, maxPushCommits, st.remoteRef+"..refs/heads/"+s.branch)
		p.Incoming = commitsIn(ctx, s.dir, maxIncoming, "refs/heads/"+s.branch+".."+st.remoteRef)
	}
	if p.Ahead == 0 {
		p.Blocked = &contracts.PushRefusal{Reason: contracts.PushNothingToPush,
			Detail: st.remote + "/" + st.remoteBranch + " already has every commit on " + s.branch + "."}
		return p, nil
	}
	if p.Behind > 0 {
		if p.RebaseBlocked, err = rebaseGate(ctx, s, st, g); err != nil {
			return p, err
		}
	}
	return p, nil
}

// PushSteps is the push itself, under the caller's session lock: fetch
// again, check the branch is what the confirmation showed, rebase if the
// user chose to and the gate allows (under the repo's lock: it rewrites the
// checkout), then push without force.
func PushSteps(ctx context.Context, t Target, req contracts.PushRequest, g Gates, lockWait time.Duration) (contracts.PushResult, error) {
	res := contracts.PushResult{}
	refused := func(r *contracts.PushRefusal) (contracts.PushResult, error) {
		res.Outcome, res.Reason, res.Detail, res.Files = contracts.PushRefused, r.Reason, r.Detail, r.Files
		return res, nil
	}
	s, refusal := spotFor(ctx, t, req.Target)
	if refusal != nil {
		return refused(refusal)
	}
	res.Branch = s.branch
	st, refusal, err := readRemote(ctx, s)
	if err != nil {
		return res, err
	}
	res.Remote, res.RemoteBranch, res.Head = st.remote, st.remoteBranch, st.head
	if refusal != nil {
		return refused(refusal)
	}
	if req.Expect != "" && !strings.HasPrefix(st.head, req.Expect) {
		return refused(&contracts.PushRefusal{Reason: contracts.PushMoved,
			Detail: s.branch + " has moved since the confirmation. Look again before pushing."})
	}
	if st.ahead == 0 {
		return refused(&contracts.PushRefusal{Reason: contracts.PushNothingToPush,
			Detail: st.remote + "/" + st.remoteBranch + " already has every commit on " + s.branch + "."})
	}
	if st.behind > 0 {
		if !req.Rebase {
			return refused(&contracts.PushRefusal{Reason: contracts.PushBehind,
				Detail: st.remote + "/" + st.remoteBranch + " has " + plural(st.behind, "commit") + " you don't have."})
		}
		rebased, err := rebaseOnto(ctx, t, s, st, g, lockWait)
		if err != nil {
			return res, err
		}
		if rebased.Outcome != "" {
			rebased.Branch, rebased.Remote, rebased.RemoteBranch = res.Branch, res.Remote, res.RemoteBranch
			return rebased, nil
		}
		res.Rebased = true
		head, err := Git(ctx, s.dir, "rev-parse", "refs/heads/"+s.branch)
		if err != nil {
			return res, err
		}
		res.Head = strings.TrimSpace(head)
	}

	args := []string{"push", "--porcelain"}
	if !st.upstream {
		args = append(args, "--set-upstream")
	}
	args = append(args, st.remote, "refs/heads/"+s.branch+":refs/heads/"+st.remoteBranch)
	if out, err := netGit(ctx, s.dir, pushTimeout, args...); err != nil {
		return refused(&contracts.PushRefusal{Reason: contracts.PushFailed,
			Detail: "git push said no:\n" + gitOutput(out, err)})
	}
	res.Outcome, res.Pushed = contracts.PushPushed, st.ahead
	return res, nil
}

// rebaseOnto rebases s.branch onto the remote branch in s.dir, gated and
// under the repo's lock. A zero Outcome means it rebased; otherwise the
// result says why not, and the branch is where it was.
func rebaseOnto(ctx context.Context, t Target, s pushSpot, st remoteState, g Gates, lockWait time.Duration) (contracts.PushResult, error) {
	res := contracts.PushResult{}
	release, err := acquireWithin(ctx, lockWait, func(ctx context.Context) (func(), error) {
		return AcquireLock(ctx, s.dir)
	})
	if errors.Is(err, ErrLocked) {
		res.Outcome, res.Reason, res.Detail = contracts.PushRefused, contracts.PushLocked, LockedDetail
		return res, nil
	}
	if err != nil {
		return res, err
	}
	defer release()
	gate, err := rebaseGate(ctx, s, st, g)
	if err != nil {
		return res, err
	}
	if gate != nil {
		res.Outcome, res.Reason, res.Detail, res.Files = contracts.PushRefused, gate.Reason, gate.Detail, gate.Files
		return res, nil
	}
	rebase := append(mergirafArgs(ctx, t, s.dir), "rebase", "--rebase-merges", "--autostash", st.remoteRef)
	out, err := landGit(ctx, s.dir, rebase...)
	if err == nil {
		return res, nil
	}
	conflicted, _ := Git(ctx, s.dir, "diff", "--name-only", "--diff-filter=U")
	files := lines(conflicted)
	if _, aerr := landGit(ctx, s.dir, "rebase", "--abort"); aerr != nil && midOperation(ctx, s.dir) != "" {
		return res, errors.New("sessionwt: rebase failed and couldn't be aborted in " + s.dir + ": " + gitOutput(out, err))
	}
	if head, herr := Git(ctx, s.dir, "rev-parse", "refs/heads/"+s.branch); herr != nil || strings.TrimSpace(head) != st.head {
		return res, errors.New("sessionwt: after aborting the rebase, " + s.branch + " isn't back on " + st.head)
	}
	if len(files) > 0 {
		res.Outcome, res.Reason, res.Files = contracts.PushConflicted, contracts.PushRebaseFailed, files
		res.Detail = "Rebasing onto " + st.remote + "/" + st.remoteBranch + " conflicted, so it was undone: nothing changed. Ask the agent to rebase, or rebase by hand."
		return res, nil
	}
	res.Outcome, res.Reason = contracts.PushRefused, contracts.PushRebaseFailed
	res.Detail = "The rebase stopped, so it was undone: nothing changed.\n" + gitOutput(out, err)
	return res, nil
}

// plural is "1 commit", "2 commits".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
