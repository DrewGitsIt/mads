package changes

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// Skip reasons reported in DeleteScratchSkip.Reason.
const (
	SkipNotInChanges = "not in this session's changes"
	SkipNotScratch   = "not a scratch file"
	SkipGone         = "already gone"
	SkipNotRegular   = "not a regular file"
	SkipPathMoved    = "path resolves elsewhere"
)

// DeleteScratch deletes the requested paths that set (a ChangeSet computed
// at request time) lists with Tier scratch and that are regular files on
// disk — never a directory, never through a symlink (the file itself or a
// directory above it). Everything else is skipped with a reason. Deleted
// files are returned too, for the caller to record in the ledger.
func DeleteScratch(set contracts.ChangeSet, paths []string) (contracts.DeleteScratchResult, []contracts.ChangedFile) {
	res := contracts.DeleteScratchResult{Deleted: []string{}, Skipped: []contracts.DeleteScratchSkip{}}
	byPath := map[string]contracts.ChangedFile{}
	for _, f := range set.Files {
		byPath[f.Path] = f
	}
	var deleted []contracts.ChangedFile
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		skip := func(reason string) {
			res.Skipped = append(res.Skipped, contracts.DeleteScratchSkip{Path: p, Reason: reason})
		}
		f, ok := byPath[p]
		switch {
		case !ok:
			skip(SkipNotInChanges)
			continue
		case f.Tier != contracts.TierScratch:
			skip(SkipNotScratch)
			continue
		}
		// Change-set paths are canonical (physical); a directory above
		// the file that resolves elsewhere now has been swapped for a
		// symlink since the set was computed.
		dir := filepath.Dir(p)
		if shadow.RealPath(dir) != dir {
			skip(SkipPathMoved)
			continue
		}
		fi, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			skip(SkipGone)
			continue
		case err != nil:
			skip(err.Error())
			continue
		case !fi.Mode().IsRegular():
			skip(SkipNotRegular)
			continue
		}
		if err := os.Remove(p); err != nil {
			skip(err.Error())
			continue
		}
		res.Deleted = append(res.Deleted, p)
		deleted = append(deleted, f)
	}
	return res, deleted
}
