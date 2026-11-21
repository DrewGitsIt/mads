// Package gitroot finds the git work tree holding a directory without
// spawning git: it walks up from the directory looking for a .git entry,
// the way git's own discovery does. Every caller used to run
// `git rev-parse --show-toplevel` once per directory (thousands per window
// on a busy session, ~15 ms each); a walk is a handful of stats.
//
// The walk answers only what it is sure matches git. Anything it can't
// vouch for (a bare repo or a .git directory on the way up, a .git file or
// directory that doesn't look valid, a filesystem boundary, git
// environment that steers discovery) comes back unsure, and the caller asks
// git instead.
package gitroot

import (
	"os"
	"path/filepath"
	"strings"
)

// discoveryEnv are the variables that change where git looks for a repo.
// With any set, the walk can't promise git's answer.
var discoveryEnv = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_COMMON_DIR",
}

// Find walks up from dir (taken as given: symlinks aren't resolved; see
// Resolve) to the first ancestor holding a .git entry. root is that
// ancestor, "" when none is found. sure reports whether git would say the
// same: root is then what `git rev-parse --show-toplevel` prints for dir
// (given dir is already symlink-free), and "" means git would find no repo.
// Unsure, root is still the plain walk's answer, for callers that only
// want that.
//
// Missing directories are walked past (a deleted file's directory may be
// gone): the walk starts at the nearest existing ancestor.
func Find(dir string) (root string, sure bool) {
	sure = true
	for _, name := range discoveryEnv {
		if os.Getenv(name) != "" {
			sure = false
		}
	}
	dir = filepath.Clean(dir)
	var startDev uint64
	haveDev := false
	for d := dir; ; {
		info, err := os.Stat(d)
		if err == nil && info.IsDir() {
			if dev, ok := device(info); ok {
				if !haveDev {
					startDev, haveDev = dev, true
				} else if dev != startDev {
					// git stops discovery at a filesystem boundary by default.
					sure = false
				}
			}
			if filepath.Base(d) == ".git" {
				// Inside a git dir itself: git answers "not a work tree".
				sure = false
			}
			if entry, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
				if !validDotGit(d, entry) {
					sure = false
				}
				return d, sure
			}
			if isGitDir(d) {
				// A bare repo (or a git dir) on the way up: git stops here.
				sure = false
			}
		} else if err == nil {
			// A file where a directory was expected: let git say what it says.
			sure = false
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", sure
		}
		d = parent
	}
}

// Resolve is dir with symlinks resolved, as git reports paths: the nearest
// existing ancestor resolved, the missing rest appended. ok is false when
// even that fails.
func Resolve(dir string) (string, bool) {
	dir = filepath.Clean(dir)
	var rest []string
	for d := dir; ; {
		if real, err := filepath.EvalSymlinks(d); err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				real = filepath.Join(real, rest[i])
			}
			return real, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		rest = append(rest, filepath.Base(d))
		d = parent
	}
}

// validDotGit reports whether the .git entry in dir is one git would take
// as this work tree's: a directory that is a git dir, or a file pointing
// at one ("gitdir: <path>", a linked worktree or submodule). A symlinked
// .git is followed, as git does.
func validDotGit(dir string, entry os.FileInfo) bool {
	path := filepath.Join(dir, ".git")
	if entry.Mode()&os.ModeSymlink != 0 {
		info, err := os.Stat(path)
		if err != nil {
			return false
		}
		entry = info
	}
	if entry.IsDir() {
		return isGitDir(path)
	}
	if !entry.Mode().IsRegular() {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	line := strings.TrimRight(string(raw), "\r\n")
	target, ok := strings.CutPrefix(line, "gitdir: ")
	if !ok || target == "" || strings.ContainsAny(target, "\n") {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return isGitDir(target)
}

// isGitDir is git's own test, roughly: a HEAD, and either the objects and
// refs of a repo or the commondir pointer of a linked worktree's git dir.
func isGitDir(path string) bool {
	if !isFile(filepath.Join(path, "HEAD")) {
		return false
	}
	if isFile(filepath.Join(path, "commondir")) {
		return true
	}
	return isDir(filepath.Join(path, "objects")) && isDir(filepath.Join(path, "refs"))
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
