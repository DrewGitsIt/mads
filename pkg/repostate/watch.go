package repostate

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/drewkidwell/mads/pkg/sessionwt"
)

// Git records where every branch points in a few small files inside a
// repo's shared git dir, and rewrites one of them — through a lock file
// renamed into place — on every commit, merge, reset, rebase step, fetch
// and push:
//
//	HEAD, worktrees/<name>/HEAD  what each checkout is on
//	refs/…                       one file per branch, remote branch, tag
//	packed-refs                  many refs in one file, after git tidies up
//	reftable/                    all of the above, in git's newer ref format
//
// Watcher watches those — never a working tree — for a set of repos, and
// reports each repo once per settled burst of changes. A repo it can't
// watch is polled instead, and every repo is polled now and then to catch
// a change the watch missed.

// Options tunes a Watcher; zero fields take the defaults.
type Options struct {
	// Quiet is how long a repo's changes must pause before they're
	// reported; MaxWait bounds how long a steady stream of them can delay
	// the report.
	Quiet, MaxWait time.Duration
	// Poll is how often a repo that couldn't be watched is checked; Safety
	// how often a watched one is.
	Poll, Safety time.Duration
	// pollOnly starts without a file watch, as when one can't be had.
	pollOnly bool
}

func (o Options) withDefaults() Options {
	if o.Quiet == 0 {
		o.Quiet = time.Second
	}
	if o.MaxWait == 0 {
		o.MaxWait = 5 * time.Second
	}
	if o.Poll == 0 {
		o.Poll = time.Minute
	}
	if o.Safety == 0 {
		o.Safety = 5 * time.Minute
	}
	return o
}

// Spec is one repo to watch: its shared git dir, and the git dirs of the
// linked worktrees whose HEAD matters (the ones sessions work in).
type Spec struct {
	GitDir    string
	Worktrees []string
}

// Watcher reports changes to repos' branches. Safe for concurrent use.
type Watcher struct {
	opts     Options
	onChange func(gitDir string)
	fs       *fsnotify.Watcher // nil: couldn't start; every repo is polled

	mu    sync.Mutex
	repos map[string]*watched // shared git dir → state
	dirs  map[string]string   // watched directory → its repo's shared git dir
	stop  chan struct{}
	done  sync.WaitGroup
}

type watched struct {
	spec     Spec
	reftable bool
	ok       bool      // the file watch is in place
	sig      string    // the refs as of the last report or poll
	lastPoll time.Time // when the refs were last compared
	timer    *time.Timer
	first    time.Time // first change of the burst in progress
}

var errPollOnly = errors.New("poll only")

// NewWatcher starts a Watcher calling onChange (from its own goroutine)
// with the shared git dir of each repo that changed.
func NewWatcher(onChange func(gitDir string), opts Options) *Watcher {
	w := &Watcher{opts: opts.withDefaults(), onChange: onChange,
		repos: map[string]*watched{}, dirs: map[string]string{}, stop: make(chan struct{})}
	fw, err := fsnotify.NewWatcher()
	if err == nil && opts.pollOnly {
		_ = fw.Close()
		fw, err = nil, errPollOnly
	}
	if err != nil {
		log.Printf("repostate: can't watch files (%v); polling every %s instead", err, w.opts.Poll)
	} else {
		w.fs = fw
		w.done.Add(1)
		go w.events()
	}
	w.done.Add(1)
	go w.polls()
	return w
}

// Close stops watching and polling.
func (w *Watcher) Close() {
	close(w.stop)
	if w.fs != nil {
		_ = w.fs.Close()
	}
	w.done.Wait()
	w.mu.Lock()
	for _, r := range w.repos {
		if r.timer != nil {
			r.timer.Stop()
		}
	}
	w.mu.Unlock()
}

// Set makes specs the repos watched: new ones start, gone ones stop.
func (w *Watcher) Set(specs []Spec) {
	want := map[string]Spec{}
	for _, s := range specs {
		if cur, ok := want[s.GitDir]; ok {
			s.Worktrees = append(cur.Worktrees, s.Worktrees...)
		}
		want[s.GitDir] = s
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for gitDir, r := range w.repos {
		if spec, ok := want[gitDir]; !ok {
			w.dropLocked(gitDir, r)
		} else if !sameSet(spec.Worktrees, r.spec.Worktrees) {
			w.dropLocked(gitDir, r) // re-added below with its new worktrees
		}
	}
	for gitDir, spec := range want {
		if _, ok := w.repos[gitDir]; !ok {
			w.addLocked(spec)
		}
	}
}

func sameSet(a, b []string) bool {
	ma, mb := map[string]bool{}, map[string]bool{}
	for _, x := range a {
		ma[x] = true
	}
	for _, x := range b {
		mb[x] = true
	}
	if len(ma) != len(mb) {
		return false
	}
	for x := range ma {
		if !mb[x] {
			return false
		}
	}
	return true
}

func (w *Watcher) addLocked(spec Spec) {
	r := &watched{spec: spec, reftable: refFormat(spec.GitDir) == "reftable"}
	r.sig = signature(spec)
	r.lastPoll = time.Now()
	w.repos[spec.GitDir] = r
	if w.fs == nil {
		return
	}
	var paths []string
	if r.reftable {
		paths = append(paths, filepath.Join(spec.GitDir, "reftable"))
		for _, wt := range spec.Worktrees {
			paths = append(paths, filepath.Join(wt, "reftable"))
		}
	} else {
		paths = append(paths, spec.GitDir)
		paths = append(paths, spec.Worktrees...)
		_ = filepath.WalkDir(filepath.Join(spec.GitDir, "refs"), func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				paths = append(paths, p)
			}
			return nil
		})
	}
	r.ok = true
	for _, p := range paths {
		if err := w.fs.Add(p); err != nil {
			if os.IsNotExist(err) {
				continue // a worktree since removed; the poll covers the rest
			}
			log.Printf("repostate: watch %s: %v; polling %s every %s", p, err, spec.GitDir, w.opts.Poll)
			r.ok = false
			continue
		}
		w.dirs[p] = spec.GitDir
	}
}

func (w *Watcher) dropLocked(gitDir string, r *watched) {
	if r.timer != nil {
		r.timer.Stop()
	}
	for p, g := range w.dirs {
		if g == gitDir {
			if w.fs != nil {
				_ = w.fs.Remove(p)
			}
			delete(w.dirs, p)
		}
	}
	delete(w.repos, gitDir)
}

// events turns file events into settled per-repo reports.
func (w *Watcher) events() {
	defer w.done.Done()
	for {
		select {
		case <-w.stop:
			return
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			log.Printf("repostate: file watch: %v", err)
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			w.handle(ev)
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	dir, name := filepath.Dir(ev.Name), filepath.Base(ev.Name)
	w.mu.Lock()
	defer w.mu.Unlock()
	gitDir, ok := w.dirs[dir]
	if !ok {
		return
	}
	r := w.repos[gitDir]
	if r == nil || strings.HasSuffix(name, ".lock") {
		return
	}
	switch {
	case r.reftable:
		// Every table write is a ref change.
	case dir == gitDir:
		if name != "HEAD" && name != "packed-refs" {
			return // the index, FETCH_HEAD, config, logs: not where branches point
		}
	case strings.HasPrefix(dir, filepath.Join(gitDir, "refs")):
		if ev.Has(fsnotify.Create) {
			if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
				// A new folder of refs (refs/heads/fix/, refs/remotes/origin/):
				// watch it and anything already in it.
				_ = filepath.WalkDir(ev.Name, func(p string, d fs.DirEntry, err error) error {
					if err == nil && d.IsDir() && w.fs.Add(p) == nil {
						w.dirs[p] = gitDir
					}
					return nil
				})
			}
		}
	default: // a linked worktree's git dir
		if name != "HEAD" {
			return
		}
	}
	w.scheduleLocked(gitDir, r)
}

// scheduleLocked reports gitDir once its changes pause for Quiet, or at
// most MaxWait after the first.
func (w *Watcher) scheduleLocked(gitDir string, r *watched) {
	now := time.Now()
	if r.timer == nil {
		r.first = now
		r.timer = time.AfterFunc(w.opts.Quiet, func() { w.fire(gitDir) })
		return
	}
	if now.Sub(r.first) >= w.opts.MaxWait {
		r.timer.Stop()
		go w.fire(gitDir)
		return
	}
	r.timer.Reset(w.opts.Quiet)
}

func (w *Watcher) fire(gitDir string) {
	w.mu.Lock()
	r := w.repos[gitDir]
	if r == nil {
		w.mu.Unlock()
		return
	}
	r.timer = nil
	spec := r.spec
	w.mu.Unlock()
	sig := signature(spec)
	w.mu.Lock()
	if r := w.repos[gitDir]; r != nil {
		r.sig, r.lastPoll = sig, time.Now()
	}
	w.mu.Unlock()
	w.onChange(gitDir)
}

// polls checks repos the watch can't cover every Poll, and every repo
// every Safety, by comparing their refs with the last look.
func (w *Watcher) polls() {
	defer w.done.Done()
	t := time.NewTicker(w.opts.Poll)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
		}
		now := time.Now()
		w.mu.Lock()
		var due []Spec
		for _, r := range w.repos {
			if !r.ok || now.Sub(r.lastPoll) >= w.opts.Safety {
				due = append(due, r.spec)
			}
		}
		w.mu.Unlock()
		for _, spec := range due {
			sig := signature(spec)
			w.mu.Lock()
			r := w.repos[spec.GitDir]
			changed := r != nil && sig != r.sig
			if r != nil {
				r.sig, r.lastPoll = sig, now
			}
			w.mu.Unlock()
			if changed {
				w.onChange(spec.GitDir)
			}
		}
	}
}

// signature is a repo's refs and checkouts' HEADs, for comparing: every
// branch, remote branch and tag with its commit (29 ms for mads's 134), and
// what each watched checkout is on.
func signature(spec Spec) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sessionwt.Git(ctx, spec.GitDir, "--git-dir="+spec.GitDir, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(out)
	for _, d := range append([]string{spec.GitDir}, spec.Worktrees...) {
		head, _ := os.ReadFile(filepath.Join(d, "HEAD"))
		b.WriteString(d)
		b.Write(head)
	}
	return b.String()
}

// refFormat is "files" or "reftable" (git 2.45+); "files" when git can't
// say.
func refFormat(gitDir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sessionwt.Git(ctx, gitDir, "--git-dir="+gitDir, "rev-parse", "--show-ref-format")
	if err != nil {
		return "files"
	}
	return strings.TrimSpace(out)
}
