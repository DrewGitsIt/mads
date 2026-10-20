package changes

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// resolvedBaseline is a switcher entry plus what it resolved to.
type resolvedBaseline struct {
	contracts.ChangeBaseline
	repo string // the session folder's repo (base/main)
	mb   string // merge-base commit (base/main)
}

// resolveBaselines builds the switcher: Session start always; Base branch
// with a worktree base in a git repo; the default branch in a git repo.
// A later entry resolving to the same merge base as an earlier one is
// unavailable, so the UI never offers two names for one comparison.
func resolveBaselines(ctx context.Context, sess contracts.Session, repo string) []resolvedBaseline {
	start := resolvedBaseline{ChangeBaseline: contracts.ChangeBaseline{
		ID: contracts.BaselineSessionStart, Label: "Session start", Available: true,
	}}
	base := resolvedBaseline{ChangeBaseline: contracts.ChangeBaseline{ID: contracts.BaselineBase, Label: "Base branch"}, repo: repo}
	main := resolvedBaseline{ChangeBaseline: contracts.ChangeBaseline{ID: contracts.BaselineMain, Label: "main"}, repo: repo}

	switch {
	case repo == "":
		base.Reason = "not a git repo"
	case sess.WorktreeBase == "":
		base.Reason = "no base branch (the session works in place)"
	default:
		base.Label = "Base branch · " + sess.WorktreeBase
		if mb, err := mergeBase(ctx, repo, sess.WorktreeBase); err != nil {
			base.Reason = fmt.Sprintf("base branch %s not found", sess.WorktreeBase)
		} else {
			base.mb, base.Available = mb, true
		}
	}

	if repo == "" {
		main.Reason = "not a git repo"
	} else if ref, label, ok := defaultBranch(ctx, repo); !ok {
		main.Reason = "no default branch found"
	} else {
		main.Label = label
		switch mb, err := mergeBase(ctx, repo, ref); {
		case err != nil:
			main.Reason = fmt.Sprintf("no merge base with %s", label)
		case base.Available && mb == base.mb:
			main.Reason = "same as Base branch"
		default:
			main.mb, main.Available = mb, true
		}
	}
	return []resolvedBaseline{start, base, main}
}

func findBaseline(list []resolvedBaseline, id string) (resolvedBaseline, bool) {
	for _, bl := range list {
		if bl.ID == id {
			return bl, true
		}
	}
	return resolvedBaseline{}, false
}

// MergeBaseOriginal is a file's content at the base or main baseline's
// merge base, for the per-file diff. ok=false when path isn't in the
// session folder's repo (the caller falls back to the session-start
// original); content "" with ok=true is a file the merge base doesn't
// have. An unavailable baseline is ErrBaselineUnavailable.
func MergeBaseOriginal(ctx context.Context, sess contracts.Session, vs, path string) (content string, ok bool, err error) {
	folder := SessionFolder(sess)
	repo := ""
	if folder != "" {
		repo = repoRootOf(folder, map[string]string{})
	}
	bl, found := findBaseline(resolveBaselines(ctx, sess, repo), vs)
	if !found || bl.ID == contracts.BaselineSessionStart {
		return "", false, fmt.Errorf("%w: %q is not a merge-base baseline", ErrBaselineUnavailable, vs)
	}
	if !bl.Available {
		return "", false, fmt.Errorf("%w: %s: %s", ErrBaselineUnavailable, vs, bl.Reason)
	}
	rel, under := relUnder(repo, shadow.RealPath(path))
	if !under {
		return "", false, nil
	}
	content, _ = showFile(ctx, repo, bl.mb, filepath.ToSlash(rel))
	return content, true, nil
}
