package shellwatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is one path git reports changed, with what the folder says about it
// now.
type Entry struct {
	X, Y    byte // porcelain status columns
	Exists  bool
	ModTime time.Time
}

// added is whether git sees the path as new: untracked, or staged as added,
// renamed or copied.
func (e Entry) added() bool {
	return e.X == '?' || e.X == 'A' || e.X == 'R' || e.X == 'C'
}

// Snapshot is the changed files of one working tree at one moment.
type Snapshot struct {
	// Taken is when the look began: anything already on disk then is in it.
	Taken time.Time
	// Took is how long it ran — status plus a stat per entry — which is
	// what decides whether a repo is too slow to look at per command.
	Took    time.Duration
	Entries map[string]Entry // by path relative to the top of the tree
}

// Take runs one `git status` over the working tree at top, untracked files
// included, then stats each entry. --no-optional-locks keeps it from
// refreshing the index, so it never contends with the agent's own git
// commands; submodules are skipped, since a commit can't take their
// contents anyway. Only stdout is parsed: a warning on stderr must not
// read as a path.
func Take(ctx context.Context, top string) (*Snapshot, error) {
	taken := time.Now()
	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "status",
		"--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all")
	cmd.Dir = top
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status in %s: %w", top, err)
	}
	s := &Snapshot{Taken: taken, Entries: map[string]Entry{}}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		e := Entry{X: f[0], Y: f[1]}
		path := f[3:]
		if e.X == 'R' || e.X == 'C' {
			i++ // the source path; the destination is the file that changed
		}
		if fi, err := os.Lstat(filepath.Join(top, filepath.FromSlash(path))); err == nil {
			e.Exists, e.ModTime = true, fi.ModTime()
		}
		s.Entries[path] = e
	}
	s.Took = time.Since(taken)
	return s, nil
}

// Window is the span one shell command ran in: from when the agent asked
// for it to when its result came back. An open window (still running) has
// a zero End.
type Window struct {
	Start, End time.Time
}

// Slack around a window. Providers announce a call before running it, but
// the pump stamps the start a moment after the line arrived, so the start
// reaches back a little; the end needs only clock rounding, since the result
// is written after the command's last write. Measured against claude-code
// 2.1.285 on mads's stream-json flags (2026-10-11, 24 Bash calls, solo and
// parallel): the tool_use line arrived 15–53 ms before the command's first
// instruction ran, and the tool_result 4–9 ms after its last.
const (
	startSlack = 250 * time.Millisecond
	endSlack   = 50 * time.Millisecond
)

// contains reports whether a modification time falls inside the window. A
// filesystem that keeps whole seconds rounds mtimes down, so the start is
// rounded down to match.
func (w Window) contains(mtime time.Time, now time.Time) bool {
	start, end := w.Start.Add(-startSlack), w.End
	if end.IsZero() {
		end = now
	}
	end = end.Add(endSlack)
	if mtime.Nanosecond() == 0 {
		start = start.Truncate(time.Second)
	}
	return !mtime.Before(start) && !mtime.After(end)
}

func (w Window) overlaps(from, to time.Time) bool {
	end := w.End
	if end.IsZero() {
		end = to
	}
	return !w.Start.After(to) && !end.Before(from)
}

// Change is one file a shell command changed.
type Change struct {
	Path string // relative to the top of the tree
	Kind string // added | modified | deleted
	// At is when it changed: the file's modification time, or for a change
	// with none (a deletion, a moved-in file) the look that found it.
	At time.Time
	// Since..Until is when it could have happened, which bounds whose it
	// might be instead.
	Since, Until time.Time
}

// Attribute finds the changes in cur that the windows' commands made.
//
//   - A file whose modification time falls inside a window was written by
//     that command, whatever git thinks of it — a second edit to a file
//     that was already modified counts too.
//   - A deletion, or a new path whose time predates the previous look (mv,
//     cp -p and tar keep old times), has no time of its own. It counts if
//     it is new since prev and some window ran between the two looks.
//
// prev nil (no earlier look) leaves only the first rule. The result is
// sorted by path.
func Attribute(prev, cur *Snapshot, windows []Window) []Change {
	var out []Change
	for path, e := range cur.Entries {
		if e.Exists {
			if w, ok := windowFor(windows, e.ModTime, cur.Taken); ok {
				out = append(out, Change{Path: path, Kind: kind(e), At: e.ModTime,
					Since: w.Start.Add(-startSlack), Until: closeOf(w, cur.Taken).Add(endSlack)})
				continue
			}
		}
		if prev == nil || !ranBetween(windows, prev.Taken, cur.Taken) {
			continue
		}
		before, seen := prev.Entries[path]
		switch {
		case !e.Exists && (e.X == 'D' || e.Y == 'D') && (!seen || before.Exists):
			out = append(out, Change{Path: path, Kind: "deleted", At: cur.Taken, Since: prev.Taken, Until: cur.Taken})
		case e.Exists && e.added() && !seen && e.ModTime.Before(prev.Taken):
			out = append(out, Change{Path: path, Kind: "added", At: cur.Taken, Since: prev.Taken, Until: cur.Taken})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func windowFor(windows []Window, mtime, now time.Time) (Window, bool) {
	for _, w := range windows {
		if w.contains(mtime, now) {
			return w, true
		}
	}
	return Window{}, false
}

func ranBetween(windows []Window, from, to time.Time) bool {
	for _, w := range windows {
		if w.overlaps(from, to) {
			return true
		}
	}
	return false
}

func closeOf(w Window, now time.Time) time.Time {
	if w.End.IsZero() {
		return now
	}
	return w.End
}

func kind(e Entry) string {
	switch {
	case !e.Exists:
		return "deleted"
	case e.added():
		return "added"
	default:
		return "modified"
	}
}
