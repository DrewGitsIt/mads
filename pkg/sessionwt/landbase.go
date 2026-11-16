package sessionwt

import (
	"context"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// A linked worktree's branch has two "bases" (docs/COMMIT-LAND-PUSH-DESIGN.md
// D12): the fork commit it started from, which "this branch's own work" is
// counted from, and the base branch, where Land puts that work. Neither is
// recorded by git itself; BaseOf infers them.

// Where a base came from (LandBase.Source), most trusted first.
const (
	// BaseRecorded: mads saw the branch created (shell tracking read the
	// `git worktree add` / `checkout -b` / `switch -c` that made it).
	BaseRecorded = "recorded"
	// BaseConfirmed: the user confirmed it in a Land before.
	BaseConfirmed = "confirmed"
	// BaseReflog: the branch's creation entry names it ("Created from main").
	BaseReflog = "reflog"
	// BaseDefault: the repo's default branch — a guess, not evidence.
	BaseDefault = "default"
)

// LandBase is where a branch lands and what it's counted from.
type LandBase struct {
	Base string
	// Fork is the commit the branch started from; "" when unknown.
	Fork   string
	Source string
}

// Git config keys mads keeps a branch's base in, in the repo's own config
// so they survive sessions and other tools can read them.
func baseKey(branch string) string   { return "branch." + branch + ".madsBase" }
func forkKey(branch string) string   { return "branch." + branch + ".madsFork" }
func sourceKey(branch string) string { return "branch." + branch + ".madsBaseSource" }

// RecordBase keeps base (and fork, when known) for branch in the repo's git
// config, as source.
func RecordBase(ctx context.Context, dir, branch string, b LandBase) error {
	if _, err := Git(ctx, dir, "config", baseKey(branch), b.Base); err != nil {
		return err
	}
	if b.Fork != "" {
		if _, err := Git(ctx, dir, "config", forkKey(branch), b.Fork); err != nil {
			return err
		}
	}
	_, err := Git(ctx, dir, "config", sourceKey(branch), b.Source)
	return err
}

// BaseOf infers branch's base in the repo holding dir: what mads recorded
// or the user confirmed, else the branch's creation entry in its reflog,
// else the repo's default branch. The fork commit comes from the record,
// else the reflog's oldest entry, else `merge-base --fork-point`, which
// survives a rewritten base. ok is false when no base can be named (a repo
// with one branch).
func BaseOf(ctx context.Context, dir, branch string) (LandBase, bool) {
	if base := gitConfig(ctx, dir, baseKey(branch)); base != "" && base != branch && refExists(ctx, dir, "refs/heads/"+base) {
		b := LandBase{Base: base, Fork: gitConfig(ctx, dir, forkKey(branch)), Source: gitConfig(ctx, dir, sourceKey(branch))}
		if b.Source == "" {
			b.Source = BaseConfirmed
		}
		if b.Fork == "" {
			b.Fork = forkPoint(ctx, dir, base, branch)
		}
		return b, true
	}

	from, firstTip := createdFrom(ctx, dir, branch)
	if from != "" && from != "HEAD" && from != branch && refExists(ctx, dir, "refs/heads/"+from) {
		return LandBase{Base: from, Fork: firstTip, Source: BaseReflog}, true
	}
	base := defaultBranch(ctx, dir, branch)
	if base == "" {
		return LandBase{}, false
	}
	fork := firstTip
	if fork == "" || !isAncestor(ctx, dir, fork, branch) {
		fork = forkPoint(ctx, dir, base, branch)
	}
	return LandBase{Base: base, Fork: fork, Source: BaseDefault}, true
}

// createdFrom reads the branch's oldest reflog entry: what its creation
// says it was "Created from" (a branch name, "HEAD", or a commit), and the
// commit it then pointed at — its fork commit. Empty when the reflog is
// gone (expired, or the branch came from a clone or fetch).
func createdFrom(ctx context.Context, dir, branch string) (from, tip string) {
	out, err := Git(ctx, dir, "reflog", "show", "--format=%H %gs", "refs/heads/"+branch)
	if err != nil {
		return "", ""
	}
	ls := lines(out)
	if len(ls) == 0 {
		return "", ""
	}
	hash, subject, _ := strings.Cut(ls[len(ls)-1], " ")
	if rest, ok := strings.CutPrefix(subject, "branch: Created from "); ok {
		from = strings.TrimPrefix(strings.TrimSpace(rest), "refs/heads/")
	}
	return from, hash
}

// defaultBranch is the repo's default branch as a local branch other than
// branch: the remote's HEAD (origin/HEAD → main), else what the main
// checkout has checked out, else main or master. "" when there's none.
func defaultBranch(ctx context.Context, dir, branch string) string {
	var candidates []string
	if remote, _, _ := RemoteFor(ctx, dir, branch); remote != "" {
		if out, err := Git(ctx, dir, "symbolic-ref", "-q", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil {
			candidates = append(candidates, strings.TrimPrefix(strings.TrimSpace(out), remote+"/"))
		}
	}
	if main, err := mainWorktree(ctx, dir); err == nil {
		if b, err := CurrentBranch(ctx, main); err == nil {
			candidates = append(candidates, b)
		}
	}
	candidates = append(candidates, "main", "master")
	for _, c := range candidates {
		if c != "" && c != branch && refExists(ctx, dir, "refs/heads/"+c) {
			return c
		}
	}
	return ""
}

// forkPoint is where branch left base: `merge-base --fork-point`, which
// reads base's reflog and so still finds it after base was rewritten,
// else the plain merge base.
func forkPoint(ctx context.Context, dir, base, branch string) string {
	for _, args := range [][]string{
		{"merge-base", "--fork-point", "refs/heads/" + base, "refs/heads/" + branch},
		{"merge-base", "refs/heads/" + base, "refs/heads/" + branch},
	} {
		if out, err := Git(ctx, dir, args...); err == nil {
			if h := strings.TrimSpace(out); h != "" {
				return h
			}
		}
	}
	return ""
}

func isAncestor(ctx context.Context, dir, commit, branch string) bool {
	_, err := Git(ctx, dir, "merge-base", "--is-ancestor", commit, "refs/heads/"+branch)
	return err == nil
}

func gitConfig(ctx context.Context, dir, key string) string {
	out, err := Git(ctx, dir, "config", "--get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// CountToLand counts the commits on branch whose changes base doesn't
// have yet, copies of ones it does left out — what a Land would bring.
func CountToLand(ctx context.Context, dir, base, branch string) (int, error) {
	return countRange(ctx, dir, "--cherry-pick", "--right-only", "refs/heads/"+base+"...refs/heads/"+branch)
}

// ToLand lists them, newest first, at most limit.
func ToLand(ctx context.Context, dir, base, branch string, limit int) []contracts.PushCommit {
	return commitsIn(ctx, dir, limit, "--cherry-pick", "--right-only", "refs/heads/"+base+"...refs/heads/"+branch)
}

// BaseFromCreation is the base a branch just created by a shell command
// should record (BaseRecorded), "" when it can't say or one is already
// kept: the start point the command named, as a local branch (origin/main
// → main); else, for `git worktree add`, the branch checked out where the
// command ran (that checkout's HEAD doesn't move); else, for `checkout -b`
// or `switch -c`, the branch that checkout moved from, from HEAD's reflog.
func BaseFromCreation(ctx context.Context, dir, branch, start string, worktree bool) string {
	if !refExists(ctx, dir, "refs/heads/"+branch) || gitConfig(ctx, dir, baseKey(branch)) != "" {
		return ""
	}
	base := ""
	switch {
	case start != "":
		if refExists(ctx, dir, "refs/heads/"+start) {
			base = start
		} else if _, rest, ok := strings.Cut(start, "/"); ok && refExists(ctx, dir, "refs/remotes/"+start) && refExists(ctx, dir, "refs/heads/"+rest) {
			base = rest
		}
	case worktree:
		base, _ = CurrentBranch(ctx, dir)
	default:
		out, err := Git(ctx, dir, "reflog", "show", "-n", "20", "--format=%gs", "HEAD")
		if err == nil {
			for _, l := range lines(out) {
				if rest, ok := strings.CutPrefix(l, "checkout: moving from "); ok {
					if from, to, ok := strings.Cut(rest, " to "); ok && to == branch {
						base = from
						break
					}
				}
			}
		}
	}
	if base == branch || base == "" || !refExists(ctx, dir, "refs/heads/"+base) {
		return ""
	}
	return base
}
