package sessionwt

import (
	"context"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// landTries bounds how often Land starts over because the base moved
// between bringing it into the session branch and merging into it (D6
// step 2). Each retry means someone landed in the meantime; three in a row
// is a busy repo, and the user can simply press Land again.
const landTries = 3

// afterStep1 is a test seam, called between Land's two steps: where a
// concurrent land in the same repo would move the base.
var afterStep1 func()

// LandSteps is D6's two-step Land, after LandGate has passed:
//
//  1. In the session's folder, record B = the base's commit and merge it
//     into the session branch. A conflict aborts the merge and comes back
//     as outcome conflicted with the files: the user's checkout is never
//     touched, and the agent can be asked to do this merge itself.
//  2. Under the repo lock, re-check the base is still B (if not, release
//     and start over from step 1), then `git merge --no-ff` the session
//     branch in the user's checkout with the user's identity. Any non-zero
//     exit aborts that merge, so the checkout is never left mid-merge, and
//     comes back as a refusal naming git's reason.
//
// Then, best-effort, the session branch fast-forwards to the base so the
// session carries on from the landed work; a refusal there is not an error.
// The caller holds the session lock and must not hold the repo lock
// (LandSteps takes it); it records the merge commit. Only unexpected git
// failures are errors.
func LandSteps(ctx context.Context, t Target) (contracts.LandResult, error) {
	res := contracts.LandResult{Branch: t.Branch, Base: t.Base}
	refuse := func(reason, detail string, files []string) (contracts.LandResult, error) {
		res.Outcome, res.Reason, res.Detail, res.Files = contracts.LandRefused, reason, detail, files
		return res, nil
	}
	baseRef := "refs/heads/" + t.Base
	for try := 0; try < landTries; try++ {
		b, err := Git(ctx, t.Primary, "rev-parse", "--verify", baseRef+"^{commit}")
		if err != nil {
			return refuse(contracts.LandBaseNotCheckedOut, "The branch "+t.Base+" no longer exists.", nil)
		}
		b = strings.TrimSpace(b)

		// Step 1, in the session's folder. It lands on the base in the end,
		// so it carries the user's identity too.
		out, err := userGit(ctx, t.Folder, "merge", "--no-edit",
			"-m", "Merge "+t.Base+" into "+t.Branch, "-m", trailer(t.SessionID), b)
		if err != nil {
			conflicted, _ := Git(ctx, t.Folder, "diff", "--name-only", "--diff-filter=U")
			abortMerge(ctx, t.Folder)
			if files := lines(conflicted); len(files) > 0 {
				res.Outcome, res.Files = contracts.LandConflicted, files
				return res, nil
			}
			return refuse(contracts.LandMergeFailed, gitOutput(out, err), nil)
		}

		if afterStep1 != nil {
			afterStep1()
		}
		// Step 2, in the user's checkout, under the repo lock.
		landed, result, err := landInPrimary(ctx, t, b)
		if err != nil {
			return contracts.LandResult{}, err
		}
		if !landed && result.Outcome == "" {
			continue // the base moved: step 1 again
		}
		if !landed {
			return result, nil
		}
		res = result
		if _, err := userGit(ctx, t.Folder, "merge", "--ff-only", "-q", baseRef); err == nil {
			res.FastForwarded = true
		}
		return res, nil
	}
	return refuse(contracts.LandBaseMoving, t.Base+" kept moving while landing. Try Land again.", nil)
}

// landInPrimary is step 2. landed false with an empty Outcome means the
// base moved off b and step 1 must run again.
func landInPrimary(ctx context.Context, t Target, b string) (landed bool, res contracts.LandResult, err error) {
	res = contracts.LandResult{Branch: t.Branch, Base: t.Base}
	release, err := AcquireLock(ctx, t.Primary)
	if err != nil {
		return false, res, err
	}
	defer release()

	cur, err := Git(ctx, t.Primary, "rev-parse", "--verify", "refs/heads/"+t.Base+"^{commit}")
	if err != nil || strings.TrimSpace(cur) != b {
		return false, res, nil
	}
	// The gate checked these before step 1; the user may have acted since,
	// and a merge --abort must only ever abort this merge, not theirs.
	if branch, err := CurrentBranch(ctx, t.Primary); err != nil || branch != t.Base {
		res.Outcome, res.Reason = contracts.LandRefused, contracts.LandBaseNotCheckedOut
		res.Detail = "Your checkout at " + t.Primary + " is no longer on " + t.Base + ". Check it out there to land."
		return false, res, nil
	}
	if op := midOperation(ctx, t.Primary); op != "" {
		res.Outcome, res.Reason = contracts.LandRefused, contracts.LandPrimaryMidMerge
		res.Detail = "Your checkout at " + t.Primary + " is in the middle of a " + op + ". Finish or abort it, then land."
		return false, res, nil
	}

	out, err := userGit(ctx, t.Primary, "merge", "--no-ff", "--no-edit",
		"-m", "Land "+landName(t), "-m", trailer(t.SessionID), "refs/heads/"+t.Branch)
	if err != nil {
		abortMerge(ctx, t.Primary)
		res.Outcome = contracts.LandRefused
		res.Reason, res.Detail, res.Files = classifyPrimaryRefusal(gitOutput(out, err))
		return false, res, nil
	}
	head, err := Git(ctx, t.Primary, "rev-parse", "HEAD")
	if err != nil {
		return false, res, err
	}
	res.Outcome, res.MergeCommit = contracts.LandLanded, strings.TrimSpace(head)
	return true, res, nil
}

// landName is the session's name for the merge subject, falling back to
// its branch when it has none.
func landName(t Target) string {
	if n := strings.TrimSpace(t.Name); n != "" {
		return n
	}
	return t.Branch
}

// abortMerge undoes a merge git left half-done in dir. Only called right
// after a merge this package started, in a tree checked not to be mid-merge
// before it, so the merge it aborts is always its own; a refused merge
// that never started leaves no MERGE_HEAD and nothing to abort.
func abortMerge(ctx context.Context, dir string) {
	if _, err := Git(ctx, dir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		_, _ = Git(ctx, dir, "merge", "--abort")
	}
}

// classifyPrimaryRefusal turns git's refusal to merge into the user's
// checkout into a reason code, naming the files where git does.
func classifyPrimaryRefusal(out string) (reason, detail string, files []string) {
	switch {
	case strings.Contains(out, "Your local changes to the following files would be overwritten"):
		return contracts.LandPrimaryDirty,
			"Your uncommitted changes in your checkout touch files this session changed. Commit or stash them there, then land.",
			indentedFiles(out, "would be overwritten")
	case strings.Contains(out, "untracked working tree files would be overwritten"):
		return contracts.LandPrimaryUntracked,
			"Untracked files in your checkout are in the way of files this session adds. Move or remove them there, then land.",
			indentedFiles(out, "untracked working tree files would be overwritten")
	default:
		return contracts.LandPrimaryRefused, out, nil
	}
}

// indentedFiles reads the tab-indented file list git prints after the
// line containing header.
func indentedFiles(out, header string) []string {
	var files []string
	in := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, header):
			in = true
		case in && strings.HasPrefix(line, "\t"):
			files = append(files, strings.TrimSpace(line))
		case in:
			in = false
		}
	}
	return files
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
