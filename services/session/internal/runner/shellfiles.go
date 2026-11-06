package runner

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/session/internal/shellwatch"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// startShellWatch follows the session's shell calls for the files they
// write (services/session/internal/shellwatch). File tools report their own
// paths; without this, `sed -i`, a formatter or a script's writes never
// reach the ledger, so Commit leaves them out. Its folder's repo is
// followed from the start when the session works in place there; an
// isolated session's worktree needs none of it (everything in it is
// already its work, §5, and its header counts those files as Outside).
// Any other repo joins the set once a command names it
// (docs/COMMIT-LAND-PUSH-DESIGN.md D7).
//
// Called once in Start, before the pump runs, so the pump reads r.shell
// without a lock.
func (r *Runner) startShellWatch() {
	r.mu.Lock()
	wt := r.wt
	r.mu.Unlock()
	set := &shellSet{
		r: r, trackers: map[string]*shellwatch.Tracker{}, adopted: map[string]bool{},
		baselined: map[string]bool{}, dirRoot: map[string]string{}, finished: map[string]time.Time{},
	}
	if wt.dir != "" {
		if top, err := sessionwt.Toplevel(context.Background(), wt.dir); err == nil {
			if wt.branch != "" {
				set.exclude = top
			} else {
				set.trackers[top] = shellwatch.New(wt.dir, r.recordShellChanges)
			}
		}
	}
	r.shell = set
}

// observeShell is the pump's hook: a shell call that might write opens a
// window in every repo followed so far, and in any repo it names that isn't
// yet (resolved off the pump); its result closes them, and a turn end
// closes what the main agent left open. at is when the pump took the event
// — stamped before anything else the pump does with it, so the window
// starts as early as it can. Only locks, map writes and a string scan here;
// the looking happens elsewhere.
func (r *Runner) observeShell(ev provider.Event, at time.Time) {
	if r.shell == nil {
		return
	}
	switch ev.Kind {
	case provider.EventToolUse:
		if ev.Tool != nil && ev.Tool.Kind == provider.ToolKindExecute && !shellwatch.ReadOnly(ev.Tool.Input) {
			delegate := ev.ParentToolUseID != ""
			r.shell.Started(ev.Tool.ID, at, delegate)
			if names := shellwatch.Named(ev.Tool.Input); len(names) > 0 {
				r.shell.adopt(names, ev.Tool.ID, at, delegate)
			}
		}
	case provider.EventToolResult:
		if ev.Tool != nil {
			r.shell.Finished(ev.Tool.ID, at)
		}
	case provider.EventTurnEnd:
		r.shell.TurnEnded(at)
	}
}

// maxShellRepos is how many repos a session's shell calls are looked at
// after every command in; repos past it are looked at once per turn.
const maxShellRepos = 8

// shellSet is the session's shell trackers, one per repo.
type shellSet struct {
	r       *Runner
	exclude string // an isolated session's worktree: never followed

	mu        sync.Mutex
	trackers  map[string]*shellwatch.Tracker // repo top → its tracker
	adopted   map[string]bool                // tops found through commands
	baselined map[string]bool                // adopted tops given a shadow baseline
	dirRoot   map[string]string              // named dir → its repo top ("" none)
	finished  map[string]time.Time           // call id → its result's time, for late adoptions
	stopped   bool
	wg        sync.WaitGroup // adoptions in flight
}

func (s *shellSet) each(fn func(*shellwatch.Tracker)) {
	s.mu.Lock()
	ts := make([]*shellwatch.Tracker, 0, len(s.trackers))
	for _, t := range s.trackers {
		ts = append(ts, t)
	}
	s.mu.Unlock()
	for _, t := range ts {
		fn(t)
	}
}

func (s *shellSet) Started(id string, at time.Time, delegate bool) {
	s.each(func(t *shellwatch.Tracker) { t.Started(id, at, delegate) })
}

func (s *shellSet) Finished(id string, at time.Time) {
	s.mu.Lock()
	s.finished[id] = at
	s.mu.Unlock()
	s.each(func(t *shellwatch.Tracker) { t.Finished(id, at) })
}

func (s *shellSet) TurnEnded(at time.Time) {
	s.mu.Lock()
	for id, fin := range s.finished {
		if at.Sub(fin) > time.Minute {
			delete(s.finished, id)
		}
	}
	s.mu.Unlock()
	s.each(func(t *shellwatch.Tracker) { t.TurnEnded(at) })
}

func (s *shellSet) ProcessEnded(at time.Time) {
	s.each(func(t *shellwatch.Tracker) { t.ProcessEnded(at) })
}

// Stop takes no more repos, lets adoptions in flight settle, and stops
// every tracker, as Tracker.Stop does, within ctx.
func (s *shellSet) Stop(ctx context.Context) {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	s.wg.Wait()
	s.each(func(t *shellwatch.Tracker) { t.Stop(ctx) })
}

// adopt resolves the places a call named to their repos, off the pump,
// and starts following each new one, opening the call's window there with
// its original start (and closing it, if its result came meanwhile).
func (s *shellSet) adopt(names []string, id string, at time.Time, delegate bool) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		for _, name := range names {
			top := s.rootOf(name)
			if top == "" {
				continue
			}
			s.mu.Lock()
			if s.stopped || s.trackers[top] != nil || (s.exclude != "" && within(s.exclude, top)) {
				s.mu.Unlock()
				continue
			}
			record := func(ctx context.Context, root string, changes []shellwatch.Change) {
				s.r.recordAdoptedShellChanges(ctx, root, changes)
			}
			var t *shellwatch.Tracker
			if len(s.trackers) < maxShellRepos {
				t = shellwatch.New(top, record)
			} else {
				t = shellwatch.NewPerTurn(top, record)
			}
			s.trackers[top] = t
			s.adopted[top] = true
			fin, done := s.finished[id]
			s.mu.Unlock()
			t.Started(id, at, delegate)
			if done {
				t.Finished(id, fin)
			}
		}
	}()
}

// rootOf is the top of the repo holding name (a file or directory, which
// may not exist yet: its nearest existing ancestor stands in), cached per
// directory; "" outside every repo.
func (s *shellSet) rootOf(name string) string {
	dir := name
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	s.mu.Lock()
	top, ok := s.dirRoot[dir]
	s.mu.Unlock()
	if ok {
		return top
	}
	top, err := sessionwt.Toplevel(context.Background(), dir)
	if err != nil {
		top = ""
	}
	s.mu.Lock()
	s.dirRoot[dir] = top
	s.mu.Unlock()
	return top
}

// within reports whether p is root or below it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// recordAdoptedShellChanges records a repo's findings like the folder's,
// and first gives the shadow store a baseline for a repo that has none:
// the files the command changed as HEAD has them, the rest as they are
// (D7, D8). Without it, a later capture's `add -A` would take the shell's
// edits as the starting point and Changes would lose them.
func (r *Runner) recordAdoptedShellChanges(ctx context.Context, top string, changes []shellwatch.Change) {
	if r.shadowStore != nil && r.shell != nil {
		r.shell.mu.Lock()
		first := !r.shell.baselined[top]
		r.shell.baselined[top] = true
		r.shell.mu.Unlock()
		if first {
			r.shadowStore.BaselineBefore(top, headBefore(ctx, top, changes))
		}
	}
	r.recordShellChanges(ctx, top, changes)
}

// headBefore is each changed file as top's HEAD has it — the best "before"
// for a change found after the fact. A file HEAD lacks counts as absent.
func headBefore(ctx context.Context, top string, changes []shellwatch.Change) map[string]shadow.Before {
	out := make(map[string]shadow.Before, len(changes))
	for _, c := range changes {
		ls, err := sessionwt.Git(ctx, top, "ls-tree", "HEAD", "--", c.Path)
		mode, rest, _ := strings.Cut(strings.TrimSpace(ls), " ")
		fields := strings.Fields(rest)
		if err != nil || len(fields) < 2 || fields[0] != "blob" {
			out[c.Path] = shadow.Before{Absent: true}
			continue
		}
		content, err := sessionwt.Git(ctx, top, "cat-file", "blob", fields[1])
		if err != nil {
			out[c.Path] = shadow.Before{Absent: true}
			continue
		}
		out[c.Path] = shadow.Before{Content: []byte(content), Executable: mode == "100755"}
	}
	return out
}

// recordShellChanges is the tracker's Recorder: one look's findings, sent
// to the Data Service as one batch. A file the session's own file tool
// recorded inside the same window is left out — that edit is already in the
// ledger. A new header count follows when anything was recorded.
func (r *Runner) recordShellChanges(ctx context.Context, top string, changes []shellwatch.Change) {
	r.mu.Lock()
	base := r.touchBaseLocked()
	batch := make([]contracts.ShellFileChange, 0, len(changes))
	for _, c := range changes {
		abs := filepath.Join(top, filepath.FromSlash(c.Path))
		if at, ok := r.toolTouchedAt[abs]; ok && !at.Before(c.Since) && !at.After(c.Until) {
			continue
		}
		// Same convention as recordFileChange: only a path's first touch
		// (or its first since a recorded deletion) reads as created.
		change := c.Kind
		prev, seen := r.touched[abs]
		if change == "deleted" && prev == "deleted" {
			continue // the turn-end sweep (sweepDeletedFiles) got there first
		}
		if change == "added" && seen && prev != "deleted" {
			change = "modified"
		}
		batch = append(batch, contracts.ShellFileChange{
			Repo: base, Path: abs, Change: change, Timestamp: c.At, Since: c.Since, Until: c.Until,
		})
	}
	r.mu.Unlock()
	if len(batch) == 0 {
		return
	}

	res, err := r.data.RecordShellFileChanges(ctx, r.sessionID, batch)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("runner: record shell file changes: %v", err)
		}
		return
	}
	r.mu.Lock()
	for _, fc := range res.Recorded {
		r.touched[fc.Path] = fc.Change
	}
	r.mu.Unlock()
	if len(res.Recorded) > 0 {
		r.refreshCountsAsync()
	}
}
