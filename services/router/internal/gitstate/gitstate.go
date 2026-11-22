// Package gitstate keeps each session's git-derived state in the Router's
// memory (docs/GIT-STATE-DESIGN.md): its repos' branch facts, its change
// set, and its header counts. Reads answer from what is kept; triggers
// recompute it in the background, one single-flight compute per part, and
// a finished triggered recompute is announced as session.git_state_updated.
//
// The computes themselves are the Router's own (Computers): this package
// decides only when they run and what they last said.
package gitstate

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Defaults, per the design.
const (
	// DefaultDebounce and DefaultMaxWait shape a burst of triggers into one
	// recompute: this long after the last, but at least this often.
	DefaultDebounce = 1500 * time.Millisecond
	DefaultMaxWait  = 5 * time.Second
	// DefaultWarmFor is how long a session nobody runs stays kept after its
	// state was last read.
	DefaultWarmFor = 15 * time.Minute
	// DefaultBaselineTTL is how long a change set against base or main (on
	// demand, not kept warm) stays cached after its last read.
	DefaultBaselineTTL = 5 * time.Minute
	// DefaultTimeout bounds one recompute.
	DefaultTimeout = 2 * time.Minute
	// DefaultSweepEvery is how often the warm set is checked against the
	// active sessions and evicted.
	DefaultSweepEvery = time.Minute
	// DefaultMaxConcurrent caps the computes running at once, across every
	// session (the boot warm-up and triggers alike); the rest wait their
	// turn, still "updating".
	DefaultMaxConcurrent = 4
	// DefaultErrorRetryEvery is how often a read may retry a part whose
	// latest compute failed (a trigger or an explicit refresh always may).
	DefaultErrorRetryEvery = time.Minute
	// DefaultOpenMaxAge is how old the kept change set may be before a
	// window opening the session, or a refresh without a body (open,
	// focus), recomputes it.
	DefaultOpenMaxAge = 30 * time.Second
	// DefaultShownFor: a session read this lately is shown in a window,
	// and its computes go ahead of the others (slots).
	DefaultShownFor = time.Minute
	// DefaultAskFloor is the least time between two computes of one part
	// that explicit asks (POST /worktree/refresh, with or without a body)
	// start, whoever asks: a faster ask waits for the floor, folded into
	// one compute. Triggers have their own debounce.
	DefaultAskFloor = 5 * time.Second
)

// emitRetries are the waits before each retry of a failed announcement.
var emitRetries = []time.Duration{250 * time.Millisecond, time.Second}

// Repos is the repos part: GET /sessions/{id}/repos's body, every
// checkout the session works in (each repo's folder and, for an isolated
// one, the user's checkout), for the repo watcher, and every repo root of
// the session's set (State leaves out one whose facts can't be read).
type Repos struct {
	State     []contracts.SessionRepoState
	Checkouts []string
	Roots     []string
	// Stash is each repo's refs/stash value by root ("" none): a stash
	// push or pop moves the working tree without moving HEAD.
	Stash map[string]string
}

// Counts is what a header recount leaves: a fingerprint of the counts (to
// tell a changed result from an unchanged one) and the roots of the repos
// it counted something in (the session record's Repos). A root the repos
// part doesn't hold means the session works in a repo it didn't before:
// the repos part is recomputed.
type Counts struct {
	Print string
	Roots []string
}

// Computers are the Router's computes, unchanged from before the cache.
type Computers struct {
	Repos     func(ctx context.Context, sessionID string) (Repos, error)
	ChangeSet func(ctx context.Context, sessionID, vs string) (contracts.ChangeSet, error)
	// Counts recounts the session's header counts (they land on the session
	// record, and go out as session.updated) and returns what the record
	// says afterwards.
	Counts func(ctx context.Context, sessionID string) (Counts, error)
}

// Events is the Data Service's log, as the service follows it.
type Events interface {
	EventsHead(ctx context.Context) (int64, error)
	Stream(ctx context.Context, since int64, sessionID string, types []string) (<-chan contracts.Envelope, error)
}

// Options configures a Service. Zero durations take the defaults.
type Options struct {
	// Emit appends session.git_state_updated. Nil emits nothing.
	Emit func(ctx context.Context, p contracts.GitStateUpdatedPayload) error
	// Events is followed from its head by Run. Nil: no event triggers.
	Events Events
	// Active lists the active sessions' ids, for the warm set. Nil: none.
	Active func(ctx context.Context) ([]string, error)
	// TargetsChanged is told when the set of checkouts warm sessions use
	// moved (Checkouts has a new answer), so the repo watcher can follow.
	TargetsChanged func()

	Debounce, MaxWait, WarmFor, BaselineTTL, Timeout, SweepEvery  time.Duration
	ErrorRetryEvery, OpenMaxAge, ShownFor, AskFloor, PromoteAfter time.Duration
	// MaxConcurrent caps computes running at once. Zero: the default.
	MaxConcurrent int
	// Now is the clock (tests). Nil: time.Now.
	Now func() time.Time
	// Logf logs (tests). Nil: log.Printf.
	Logf func(format string, args ...any)
}

// slot is one cached part: the part, and for a change set its baseline.
type slot struct {
	part string
	vs   string
}

var (
	slotRepos  = slot{part: contracts.GitPartRepos}
	slotCounts = slot{part: contracts.GitPartCounts}
	slotStart  = slot{part: contracts.GitPartChangeSet, vs: contracts.BaselineSessionStart}
)

// part is one slot's kept answer and its recompute state.
type part struct {
	computed bool
	at       time.Time
	repos    Repos
	set      contracts.ChangeSet
	// err is the latest compute's failure; a previous good answer is still
	// served beside it (hasValue).
	err      error
	hasValue bool
	fp       string

	running bool
	// dirty: a trigger arrived while running; exactly one rerun follows.
	dirty bool
	// queued: waiting in the session's debounce.
	queued bool
	// stale: a trigger arrived since the last compute but didn't recompute
	// it (a change set against base or main): the next read does.
	stale bool
	// owed: a trigger or a waiting read asked for this compute, so its end
	// is announced; owedTrigger: a trigger (or a refresh) did, not only a
	// read.
	owed        bool
	owedTrigger bool
	// deferredAt: when an owed end was first held back because the part
	// was queued again (its queued run announces instead), or zero.
	deferredAt time.Time
	// changedAcc carries "the result changed" over a dirty rerun, which is
	// announced once at its end.
	changedAcc bool
	readAt     time.Time
	// askedAt: when an explicit ask last started a compute; askTimer the
	// one waiting for AskFloor after it, if any.
	askedAt  time.Time
	askTimer *time.Timer
	// refs: for a change set, the refs it was computed against (refsFor),
	// as the repos part knew them then; "" unknown.
	refs string
}

func (p *part) updating() bool { return p.running || p.dirty || p.queued || p.askTimer != nil }

type session struct {
	id         string
	lastRead   time.Time
	generation int64
	parts      map[slot]*part

	pending      map[slot]bool
	firstPending time.Time
	timer        *time.Timer
	timerSeq     int

	checkouts []string
	gitDirs   map[string]bool

	// ann collects the parts whose owed ends wait to go out together: one
	// announcement once no other part of the session is computing (or
	// MaxWait after the first), not one per part.
	ann      *pendingAnn
	annTimer *time.Timer
	// emitMu orders the session's appends with their generations.
	emitMu sync.Mutex
	// refsSeq counts repo changes in the session's repos; refsChecked is
	// the latest one a repos compute that started after it has checked the
	// change sets' refs against.
	refsSeq, refsChecked int
}

type pendingAnn struct {
	since   time.Time
	parts   map[string]bool
	changed map[string]bool
}

// ask is why a compute starts: whether its end is announced, and to whom.
type ask int

const (
	askWarm    ask = iota // the boot warm-up: announced to nobody
	askRead               // a read of a cold or stale part waits on it
	askTrigger            // a trigger or a refresh: the window marked it
)

// Service is the Router's git state. Safe for concurrent use.
type Service struct {
	opts Options
	comp Computers
	// base is what every recompute runs under (with a timeout): never a
	// request's context, so a window going away can't cancel one.
	base   context.Context
	cancel context.CancelFunc

	slots *slots
	// readAt is each session's lastRead, readable without mu (the slots
	// ask it when granting).
	readAt    sync.Map // session id → time.Time
	followLog failLog
	mu        sync.Mutex
	sess      map[string]*session
	active    map[string]bool
	wg        sync.WaitGroup
}

// New is a Service with nothing kept. Bind its computers before the first
// read; Run follows the log and keeps the warm set.
func New(opts Options) *Service {
	if opts.Debounce <= 0 {
		opts.Debounce = DefaultDebounce
	}
	if opts.MaxWait <= 0 {
		opts.MaxWait = DefaultMaxWait
	}
	if opts.WarmFor <= 0 {
		opts.WarmFor = DefaultWarmFor
	}
	if opts.BaselineTTL <= 0 {
		opts.BaselineTTL = DefaultBaselineTTL
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.SweepEvery <= 0 {
		opts.SweepEvery = DefaultSweepEvery
	}
	if opts.ErrorRetryEvery <= 0 {
		opts.ErrorRetryEvery = DefaultErrorRetryEvery
	}
	if opts.OpenMaxAge <= 0 {
		opts.OpenMaxAge = DefaultOpenMaxAge
	}
	if opts.ShownFor <= 0 {
		opts.ShownFor = DefaultShownFor
	}
	if opts.AskFloor <= 0 {
		opts.AskFloor = DefaultAskFloor
	}
	if opts.PromoteAfter <= 0 {
		opts.PromoteAfter = DefaultPromoteAfter
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = DefaultMaxConcurrent
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	base, cancel := context.WithCancel(context.Background())
	s := &Service{
		opts: opts, base: base, cancel: cancel,
		followLog: failLog{logf: opts.Logf, now: opts.Now},
		sess:      map[string]*session{}, active: map[string]bool{},
	}
	s.slots = newSlots(opts.MaxConcurrent, s.shown, opts.Now, opts.PromoteAfter)
	return s
}

// Bind sets the computes. Called once, before any read or trigger.
func (s *Service) Bind(c Computers) {
	s.mu.Lock()
	s.comp = c
	s.mu.Unlock()
}

// Close stops every recompute and waits for them.
func (s *Service) Close() {
	s.cancel()
	s.mu.Lock()
	for _, ss := range s.sess {
		if ss.timer != nil {
			ss.timer.Stop()
		}
		if ss.annTimer != nil {
			ss.annTimer.Stop()
		}
		for _, p := range ss.parts {
			if p.askTimer != nil {
				p.askTimer.Stop()
			}
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// --- reads ---

// ChangeSetAnswer is a change set read: the kept set (zero when cold),
// whether one was ever computed, whether a recompute is queued or running,
// and the latest failure when there is no set to serve.
type ChangeSetAnswer struct {
	Set      contracts.ChangeSet
	Computed bool
	Updating bool
	Err      error
}

// ChangeSet is the session's kept change set against vs. Cold, stale, or
// a baseline not kept warm: answered at once, a recompute started behind it.
func (s *Service) ChangeSet(sessionID, vs string) ChangeSetAnswer {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.readLocked(sessionID)
	sl := slot{part: contracts.GitPartChangeSet, vs: vs}
	p := s.partLocked(ss, sl)
	p.readAt = s.opts.Now()
	s.revalidateLocked(ss, sl, p)
	a := ChangeSetAnswer{Computed: p.computed, Updating: p.updating()}
	if p.hasValue {
		a.Set = p.set
	} else if p.computed {
		a.Err = p.err
	}
	return a
}

// ReposAnswer is a repos read, as ChangeSetAnswer.
type ReposAnswer struct {
	State      []contracts.SessionRepoState
	Computed   bool
	ComputedAt time.Time
	Updating   bool
	Err        error
}

// Repos is the session's kept repo set with its branch facts.
func (s *Service) Repos(sessionID string) ReposAnswer {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.readLocked(sessionID)
	p := s.partLocked(ss, slotRepos)
	p.readAt = s.opts.Now()
	s.revalidateLocked(ss, slotRepos, p)
	a := ReposAnswer{Computed: p.computed, ComputedAt: p.at, Updating: p.updating()}
	if p.hasValue {
		a.State = p.repos.State
	} else if p.computed {
		a.Err = p.err
	}
	return a
}

// revalidateLocked starts the compute a read of a cold or stale part
// needs: the reader (or another window) waits on its announcement.
func (s *Service) revalidateLocked(ss *session, sl slot, p *part) {
	if p.updating() {
		p.owed = true // someone is waiting on it now
		return
	}
	// A failed compute is tried again on a read, as a request used to be —
	// a passing failure mustn't stand until the next trigger — but at most
	// once per ErrorRetryEvery while it keeps failing.
	if !p.computed || p.stale || p.err != nil && s.opts.Now().Sub(p.at) >= s.opts.ErrorRetryEvery {
		s.startLocked(ss, sl, askRead)
	}
}

// --- triggers ---

// Ask is an explicit refresh of some of a session's parts (POST
// /sessions/{id}/worktree/refresh with a body): Parts by name, Baseline
// for the change set (empty: session start), and MaxAge, a kept answer
// computed within which is left as is (zero: recompute regardless).
type Ask struct {
	Parts    []string
	Baseline string
	MaxAge   time.Duration
}

// Refresh is POST /worktree/refresh without a body (a window opening the
// session, or regaining focus): counts now, and repos and the change set
// against session start if older than OpenMaxAge (outside edits send no
// event; repo changes have their own). Answered at once, announced when
// done.
func (s *Service) Refresh(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.readLocked(sessionID)
	s.askLocked(ss, slotCounts, 0)
	s.askLocked(ss, slotRepos, s.opts.OpenMaxAge)
	s.askLocked(ss, slotStart, s.opts.OpenMaxAge)
}

// Revalidate is an explicit refresh of the parts a asks for (a window's
// first read of a session's change set, with MaxAge; Retry, without).
// Answered at once, announced when done.
func (s *Service) Revalidate(sessionID string, a Ask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.readLocked(sessionID)
	for _, name := range a.Parts {
		sl := slot{part: name}
		if name == contracts.GitPartChangeSet {
			sl.vs = a.Baseline
			if sl.vs == "" {
				sl.vs = contracts.BaselineSessionStart
			}
		}
		s.askLocked(ss, sl, a.MaxAge)
	}
}

// askLocked recomputes sl for an explicit refresh, unless its kept answer
// is good and younger than maxAge (maxAge > 0). Never sooner than
// AskFloor after the last compute an ask started: a faster ask waits for
// it, and every ask in between is that one compute.
func (s *Service) askLocked(ss *session, sl slot, maxAge time.Duration) {
	p := s.partLocked(ss, sl)
	now := s.opts.Now()
	p.readAt = now
	if maxAge > 0 && p.computed && p.err == nil && !p.stale && now.Sub(p.at) < maxAge {
		return
	}
	if maxAge > 0 && p.running && !p.dirty {
		// A compute that started moments ago is fresh enough: its end is
		// announced to whoever asked.
		p.owed, p.owedTrigger = true, true
		return
	}
	if p.askTimer != nil {
		return // one is waiting for the floor already
	}
	if wait := s.opts.AskFloor - now.Sub(p.askedAt); !p.askedAt.IsZero() && wait > 0 {
		p.owed, p.owedTrigger = true, true
		var t *time.Timer
		t = time.AfterFunc(wait, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if p.askTimer != t || s.base.Err() != nil {
				return
			}
			p.askTimer = nil
			p.askedAt = s.opts.Now()
			s.startLocked(ss, sl, askTrigger)
		})
		p.askTimer = t
		return
	}
	p.askedAt = now
	s.startLocked(ss, sl, askTrigger)
}

// RepoChanged is the repo watcher's settled change of the repo whose
// shared git dir is gitDir (a commit, branch switch, merge or reset, by
// anyone): repos and counts of every kept session using it, and its
// change sets only where their refs moved. Every mads worktree of a repo
// shares its git dir, so one session's commit reaches them all; most
// change sets don't move with it. The repos compute this queues reads
// each checkout's HEAD and branch facts, and its end compares them with
// the refs each change set was computed against (invalidateByRefsLocked).
func (s *Service) RepoChanged(gitDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.sess {
		if !ss.gitDirs[gitDir] {
			continue
		}
		if s.shown(ss.id) {
			// A window shows it: recompute its change set regardless (a
			// stash, reset or checkout of files moves it without a ref
			// the fingerprint knows).
			s.triggerLocked(ss, []string{contracts.GitPartRepos, contracts.GitPartChangeSet, contracts.GitPartCounts})
			continue
		}
		ss.refsSeq++
		s.triggerLocked(ss, []string{contracts.GitPartRepos, contracts.GitPartCounts})
	}
}

// refsFor is what a change set against vs is computed against, as the
// repos part knows it: each checkout's HEAD and branch for session start
// (an in-place session's checkout is shared, so another session's commit
// there moves it), the base branch's head for base. "" when unknown —
// the repos part has no answer, or main, whose ref it doesn't read —
// which counts as moved.
func refsFor(ss *session, vs string) string {
	rp := ss.parts[slotRepos]
	if rp == nil || !rp.hasValue || vs == contracts.BaselineMain {
		return ""
	}
	var b []byte
	for _, r := range rp.repos.State {
		b = append(b, r.Root...)
		switch vs {
		case contracts.BaselineBase:
			// The session's own branch too: a commit on it moves the diff
			// against its base.
			b = append(b, "|"+r.Worktree.Branch+"|"+r.Worktree.Head+"|"+r.Base+"|"+r.Worktree.Base+"|"+r.Worktree.Fork...)
			if r.BaseWorktree != nil {
				b = append(b, "|"+r.BaseWorktree.Head...)
			}
		default:
			b = append(b, "|"+r.Worktree.Branch+"|"+r.Worktree.Head+"|stash:"+rp.repos.Stash[r.Root]...)
		}
		b = append(b, '\n')
	}
	return "refs:" + string(b)
}

// invalidateByRefsLocked, after a repos compute that followed a repo
// change: the change set against session start is recomputed if the refs
// it was computed against moved, and the others are marked stale (read
// recomputes them) if theirs did.
func (s *Service) invalidateByRefsLocked(ss *session) {
	for sl, p := range ss.parts {
		if sl.part != contracts.GitPartChangeSet || !p.computed {
			continue
		}
		now := refsFor(ss, sl.vs)
		if now != "" && p.refs != "" && now == p.refs {
			continue
		}
		if sl == slotStart {
			s.startLocked(ss, sl, askTrigger)
		} else {
			p.stale = true
		}
	}
}

// noteStartUnmovedLocked: a repo change left the start change set's refs
// where they were, so it isn't recomputed; its part rides in the batch
// as done (unchanged), so a window that marked it updating clears.
func (s *Service) noteStartUnmovedLocked(ss *session, now time.Time) {
	p := ss.parts[slotStart]
	if p == nil || !p.computed || p.updating() {
		return // its own compute announces it
	}
	s.annLocked(ss, now).parts[contracts.GitPartChangeSet] = true
}

// annLocked is the session's waiting batch, started (with its MaxWait
// timer) if there is none.
func (s *Service) annLocked(ss *session, now time.Time) *pendingAnn {
	if ss.ann == nil {
		a := &pendingAnn{since: now, parts: map[string]bool{}, changed: map[string]bool{}}
		ss.ann = a
		// Out by MaxWait even if no other compute ends by then.
		ss.annTimer = time.AfterFunc(s.opts.MaxWait, func() { s.flushAnn(ss, a) })
	}
	return ss.ann
}

// Trigger is an event saying parts of the session may have moved:
// debounced, for a session kept warm (kept, or active). One not kept has
// nothing kept to go stale: its next read is cold and computes.
func (s *Service) Trigger(sessionID string, parts ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sess[sessionID]
	if ss == nil {
		if !s.active[sessionID] {
			return // cold: the next read computes it
		}
		ss = s.newSessionLocked(sessionID)
	}
	s.triggerLocked(ss, parts)
}

func (s *Service) triggerLocked(ss *session, parts []string) {
	for _, name := range parts {
		if name == contracts.GitPartChangeSet {
			// Only session start is kept warm; the other baselines are
			// recomputed when next read.
			for sl, p := range ss.parts {
				if sl.part == contracts.GitPartChangeSet && sl != slotStart {
					p.stale = true
				}
			}
			s.queueLocked(ss, slotStart)
			continue
		}
		s.queueLocked(ss, slot{part: name})
	}
	now := s.opts.Now()
	if ss.firstPending.IsZero() {
		ss.firstPending = now
	}
	wait := s.opts.Debounce
	if rem := s.opts.MaxWait - now.Sub(ss.firstPending); rem < wait {
		wait = max(rem, 0)
	}
	if ss.timer != nil {
		ss.timer.Stop()
	}
	ss.timerSeq++
	seq := ss.timerSeq
	ss.timer = time.AfterFunc(wait, func() { s.fire(ss, seq) })
}

func (s *Service) queueLocked(ss *session, sl slot) {
	if ss.pending == nil {
		ss.pending = map[slot]bool{}
	}
	ss.pending[sl] = true
	s.partLocked(ss, sl).queued = true
}

func (s *Service) fire(ss *session, seq int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq != ss.timerSeq || s.sess[ss.id] != ss || s.base.Err() != nil {
		return
	}
	pending := ss.pending
	ss.pending, ss.firstPending, ss.timer = nil, time.Time{}, nil
	for sl := range pending {
		s.partLocked(ss, sl).queued = false
		s.startLocked(ss, sl, askTrigger)
	}
}

// --- recompute ---

func (s *Service) startLocked(ss *session, sl slot, why ask) {
	p := s.partLocked(ss, sl)
	if why != askWarm {
		p.owed = true
	}
	if why == askTrigger {
		p.owedTrigger = true
	}
	if p.running {
		p.dirty = true
		return
	}
	if s.base.Err() != nil {
		return
	}
	p.running = true
	s.wg.Add(1)
	go s.run(ss, sl)
}

// computed is one compute's outcome.
type computed struct {
	repos  Repos
	set    contracts.ChangeSet
	counts Counts
	fp     string
	err    error
	// refsSeq is the session's refsSeq when the compute started.
	refsSeq int
}

// compute runs sl's compute in one of the slots (the high lane for a
// session shown in a window, as of when the slot is granted).
func (s *Service) compute(ss *session, sl slot) computed {
	g, ok := s.slots.acquire(s.base, ss.id)
	if !ok {
		return computed{fp: "", err: s.base.Err()}
	}
	defer s.slots.release(g)
	ctx, cancel := context.WithTimeout(s.base, s.opts.Timeout)
	defer cancel()
	s.mu.Lock()
	comp := s.comp
	c := computed{refsSeq: ss.refsSeq}
	if p := ss.parts[sl]; p != nil && sl.part == contracts.GitPartChangeSet {
		p.refs = refsFor(ss, sl.vs)
	}
	s.mu.Unlock()
	var (
		repos  Repos
		set    contracts.ChangeSet
		counts Counts
		fp     string
		err    error
	)
	switch sl.part {
	case contracts.GitPartRepos:
		if comp.Repos != nil {
			repos, err = comp.Repos(ctx, ss.id)
			fp = reposPrint(repos.State)
		}
	case contracts.GitPartChangeSet:
		if comp.ChangeSet != nil {
			set, err = comp.ChangeSet(ctx, ss.id, sl.vs)
			fp = changeSetPrint(set)
		}
	case contracts.GitPartCounts:
		if comp.Counts != nil {
			counts, err = comp.Counts(ctx, ss.id)
			fp = counts.Print
		}
	}
	if err != nil {
		fp = "error: " + err.Error()
	}
	c.repos, c.set, c.counts, c.fp, c.err = repos, set, counts, fp, err
	return c
}

func (s *Service) run(ss *session, sl slot) {
	defer s.wg.Done()
	c := s.compute(ss, sl)
	repos, set, counts, fp, err := c.repos, c.set, c.counts, c.fp, c.err

	s.mu.Lock()
	p := ss.parts[sl]
	if s.base.Err() != nil || p == nil {
		s.mu.Unlock()
		return
	}
	now := s.opts.Now()
	p.running = false
	changed := !p.computed || p.fp != fp
	p.computed, p.at, p.fp, p.stale, p.err = true, now, fp, false, err
	if err == nil {
		p.hasValue = true
		p.repos, p.set = repos, set
	}
	targetsMoved := false
	if sl == slotRepos && err == nil {
		targetsMoved = s.setTargetsLocked(ss, repos)
	}
	if sl == slotCounts && err == nil {
		s.checkRootsLocked(ss, counts.Roots)
	}
	if sl == slotRepos && c.refsSeq > ss.refsChecked {
		// Started after a repo change: its refs are current (a failed one
		// says nothing, so every change set counts as moved).
		ss.refsChecked = c.refsSeq
		if err != nil {
			p := ss.parts[slotRepos]
			hv := p.hasValue
			p.hasValue = false
			s.invalidateByRefsLocked(ss)
			p.hasValue = hv
		} else {
			s.invalidateByRefsLocked(ss)
		}
		s.noteStartUnmovedLocked(ss, now)
	}
	p.changedAcc = p.changedAcc || changed
	done := false // this end goes out
	switch {
	case p.dirty:
		p.dirty = false
		p.running = true
		s.wg.Add(1)
		go s.run(ss, sl) // the one rerun; announced at its end
	case !p.owed:
		p.changedAcc = false
	case !p.owedTrigger && err != nil && !changed:
		// Only a read asked, and it failed as it did before: the reader
		// already has that error, and nothing it shows moved.
		p.owed, p.changedAcc = false, false
	case (p.queued || p.askTimer != nil) && (p.deferredAt.IsZero() || now.Sub(p.deferredAt) < s.opts.MaxWait):
		// Queued again (in the debounce, or an ask waiting for its
		// floor): its queued run announces, so the window's mark
		// can't clear while a recompute is still to come. Not for longer
		// than MaxWait, so a steady stream still brings the result.
		if p.deferredAt.IsZero() {
			p.deferredAt = now
		}
	default:
		done = true
	}
	var out []contracts.GitStateUpdatedPayload
	if done {
		var ch []string
		if p.changedAcc {
			ch = []string{sl.part}
		}
		p.owed, p.owedTrigger, p.changedAcc, p.deferredAt = false, false, false, time.Time{}
		if sl.part == contracts.GitPartChangeSet && sl != slotStart {
			// Another baseline: its own announcement, saying which.
			out = append(out, contracts.GitStateUpdatedPayload{SessionID: ss.id, Parts: []string{sl.part}, Baseline: sl.vs, Changed: orEmpty(ch)})
		} else {
			a := s.annLocked(ss, now)
			a.parts[sl.part] = true
			for _, c := range ch {
				a.changed[c] = true
			}
		}
	}
	if ss.ann != nil && (!s.keptRunningLocked(ss) || now.Sub(ss.ann.since) >= s.opts.MaxWait) {
		out = append(out, s.takeAnnLocked(ss))
	}
	s.mu.Unlock()

	if targetsMoved && s.opts.TargetsChanged != nil {
		s.opts.TargetsChanged()
	}
	for _, a := range out {
		s.emit(ss, a)
	}
}

func (s *Service) takeAnnLocked(ss *session) contracts.GitStateUpdatedPayload {
	a := ss.ann.payload(ss.id)
	ss.ann = nil
	if ss.annTimer != nil {
		ss.annTimer.Stop()
		ss.annTimer = nil
	}
	return a
}

// flushAnn sends a's parts MaxWait after they were collected, if they are
// still waiting.
func (s *Service) flushAnn(ss *session, a *pendingAnn) {
	s.mu.Lock()
	if ss.ann != a || s.base.Err() != nil {
		s.mu.Unlock()
		return
	}
	out := s.takeAnnLocked(ss)
	s.mu.Unlock()
	s.emit(ss, out)
}

// keptRunningLocked: a compute of a part kept warm (repos, the change set
// against session start, counts) is running for the session.
func (s *Service) keptRunningLocked(ss *session) bool {
	for _, sl := range []slot{slotRepos, slotStart, slotCounts} {
		if p := ss.parts[sl]; p != nil && p.running {
			return true
		}
	}
	return false
}

// checkRootsLocked recomputes the repos part when a recount counted in a
// repo it doesn't hold: the session works in a new repo (the record's
// Repos, which a live child's turn-end recount may have written first).
func (s *Service) checkRootsLocked(ss *session, roots []string) {
	rp := ss.parts[slotRepos]
	if rp == nil || !rp.hasValue {
		return // never computed: its first compute reads the new set
	}
	held := map[string]bool{}
	for _, r := range rp.repos.Roots {
		held[r] = true
	}
	for _, r := range roots {
		if !held[r] {
			s.startLocked(ss, slotRepos, askTrigger)
			return
		}
	}
}

func (a *pendingAnn) payload(sessionID string) contracts.GitStateUpdatedPayload {
	p := contracts.GitStateUpdatedPayload{SessionID: sessionID, Parts: []string{}, Changed: []string{}}
	for _, name := range allParts {
		if a.parts[name] {
			p.Parts = append(p.Parts, name)
		}
		if a.changed[name] {
			p.Changed = append(p.Changed, name)
		}
	}
	return p
}

var allParts = []string{contracts.GitPartRepos, contracts.GitPartChangeSet, contracts.GitPartCounts}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// emit appends the announcement with the session's next generation; one
// at a time per session, so the log's order is the generations' order. A
// failed append is tried again; if it still fails, the parts it covered
// are owed again, so their next compute announces them.
func (s *Service) emit(ss *session, p contracts.GitStateUpdatedPayload) {
	if s.opts.Emit == nil {
		return
	}
	ss.emitMu.Lock()
	defer ss.emitMu.Unlock()
	s.mu.Lock()
	// Clock-based, so a window that saw a previous Router's generations
	// still sees these as newer.
	g := max(ss.generation+1, s.opts.Now().UnixMicro())
	ss.generation = g
	s.mu.Unlock()
	p.Generation = g
	var err error
	for i := 0; ; i++ {
		ctx, cancel := context.WithTimeout(s.base, 10*time.Second)
		err = s.opts.Emit(ctx, p)
		cancel()
		if err == nil || s.base.Err() != nil {
			return
		}
		if i == len(emitRetries) || !sleep(s.base, emitRetries[i]) {
			break
		}
	}
	if s.base.Err() != nil {
		return
	}
	s.opts.Logf("gitstate: announce %s %v: %v (owed to the next compute)", p.SessionID, p.Parts, err)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range p.Parts {
		sl := slot{part: name}
		if name == contracts.GitPartChangeSet {
			sl.vs = p.Baseline
			if sl.vs == "" {
				sl.vs = contracts.BaselineSessionStart
			}
		}
		if q := ss.parts[sl]; q != nil {
			q.owed, q.owedTrigger = true, true
			q.changedAcc = q.changedAcc || slices.Contains(p.Changed, name)
		}
	}
}

func (s *Service) setTargetsLocked(ss *session, r Repos) bool {
	dirs := map[string]bool{}
	for _, st := range r.State {
		if st.Worktree.GitDir != "" {
			dirs[st.Worktree.GitDir] = true
		}
		if st.BaseWorktree != nil && st.BaseWorktree.GitDir != "" {
			dirs[st.BaseWorktree.GitDir] = true
		}
	}
	ss.gitDirs = dirs
	checkouts := append([]string(nil), r.Checkouts...)
	sort.Strings(checkouts)
	if equalStrings(checkouts, ss.checkouts) {
		return false
	}
	ss.checkouts = checkouts
	return true
}

// --- the warm set ---

// Checkouts lists the checkouts kept sessions work in: the repos for the
// repo watcher to keep current (repostate.Lister). In memory, no git.
func (s *Service) Checkouts(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, ss := range s.sess {
		for _, c := range ss.checkouts {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Service) readLocked(sessionID string) *session {
	ss := s.sess[sessionID]
	if ss == nil {
		ss = s.newSessionLocked(sessionID)
	}
	ss.lastRead = s.opts.Now()
	s.readAt.Store(sessionID, ss.lastRead)
	// Its computes waiting in the low lane may go now.
	s.slots.kick()
	return ss
}

// shown: the session was read within ShownFor (a window shows it).
func (s *Service) shown(sessionID string) bool {
	v, ok := s.readAt.Load(sessionID)
	return ok && s.opts.Now().Sub(v.(time.Time)) < s.opts.ShownFor
}

// newSessionLocked keeps a session; lastRead stays zero until a read
// (readLocked): one kept for the warm-up or a trigger isn't shown.
func (s *Service) newSessionLocked(sessionID string) *session {
	ss := &session{id: sessionID, parts: map[slot]*part{}}
	s.sess[sessionID] = ss
	return ss
}

func (s *Service) partLocked(ss *session, sl slot) *part {
	p := ss.parts[sl]
	if p == nil {
		p = &part{readAt: s.opts.Now()}
		ss.parts[sl] = p
	}
	return p
}

// Sweep brings the warm set up to date: active sessions are kept (one that
// isn't kept yet is warmed in the background), others are dropped once
// unread for WarmFor, and change sets against base or main once unread for
// BaselineTTL. Run calls it at start (the boot warm-up) and every
// SweepEvery.
func (s *Service) Sweep(ctx context.Context) {
	var active []string
	if s.opts.Active != nil {
		var err error
		if active, err = s.opts.Active(ctx); err != nil {
			if ctx.Err() == nil {
				log.Printf("gitstate: list active sessions: %v", err)
			}
			return // keep what is kept; try again next sweep
		}
	}
	s.mu.Lock()
	now := s.opts.Now()
	s.active = map[string]bool{}
	for _, id := range active {
		s.active[id] = true
		if s.sess[id] == nil {
			ss := s.newSessionLocked(id)
			// Warm: computed for whoever opens it, announced to nobody.
			for _, sl := range []slot{slotRepos, slotStart, slotCounts} {
				s.startLocked(ss, sl, askWarm)
			}
		}
	}
	moved := false
	for id, ss := range s.sess {
		busy := ss.timer != nil
		for sl, p := range ss.parts {
			if p.updating() {
				busy = true
				continue
			}
			if sl.part == contracts.GitPartChangeSet && sl != slotStart && now.Sub(p.readAt) > s.opts.BaselineTTL {
				delete(ss.parts, sl)
			}
		}
		if !s.active[id] && !busy && now.Sub(ss.lastRead) > s.opts.WarmFor {
			delete(s.sess, id)
			s.readAt.Delete(id)
			if len(ss.checkouts) > 0 {
				moved = true
			}
		}
	}
	s.mu.Unlock()
	if moved && s.opts.TargetsChanged != nil {
		s.opts.TargetsChanged()
	}
}

// Kept reports whether the session is in the warm set (tests).
func (s *Service) Kept(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sess[sessionID] != nil
}

// TriggerTypes are the events Run follows. Not session.updated: the counts
// write sends it, and following it would loop.
var TriggerTypes = []string{
	string(contracts.FileChanged),
	string(contracts.SessionAgentState),
	string(contracts.CommitCreated),
	string(contracts.CommitUndone),
	string(contracts.MergeCompleted),
}

// Run keeps the warm set (Sweep now, then every SweepEvery) and follows
// the log's triggers from its head — live only, never history — until ctx
// ends; then stops every recompute.
func (s *Service) Run(ctx context.Context) {
	defer s.Close()
	go func() {
		t := time.NewTicker(s.opts.SweepEvery)
		defer t.Stop()
		for {
			s.Sweep(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	if s.opts.Events == nil {
		<-ctx.Done()
		return
	}
	s.follow(ctx)
}

func (s *Service) follow(ctx context.Context) {
	var since int64 = -1 // not yet placed: start at the head
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		if since < 0 {
			head, err := s.opts.Events.EventsHead(ctx)
			if err != nil {
				if ctx.Err() == nil {
					s.followLog.failed("read the log head", err)
				}
				if !sleep(ctx, backoff) {
					return
				}
				backoff = min(backoff*2, 30*time.Second)
				continue
			}
			since = head
		}
		ch, err := s.opts.Events.Stream(ctx, since, "", TriggerTypes)
		if err != nil {
			if ctx.Err() == nil {
				s.followLog.failed("follow the log", err)
			}
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		s.followLog.ok()
		backoff = 500 * time.Millisecond
		for env := range ch {
			if env.Seq > since {
				since = env.Seq
			}
			s.handle(env)
		}
		// Dropped: resume where it left off (a short catch-up), not from 0.
		if !sleep(ctx, backoff) {
			return
		}
	}
}

// failLogEvery is how often a still-failing follow gets another log line.
const failLogEvery = 30 * time.Second

// failLog reports follow's failures the way the Data Service client
// reports it unreachable: one line on the first failure, at most one per
// failLogEvery while it keeps failing (with how many it left out), and one
// when it follows again. Used by follow's goroutine only.
type failLog struct {
	logf       func(format string, args ...any)
	now        func() time.Time
	down       bool
	since      time.Time
	lastLogged time.Time
	suppressed int
}

func (f *failLog) failed(what string, err error) {
	now := f.now()
	switch {
	case !f.down:
		f.down, f.since, f.lastLogged, f.suppressed = true, now, now, 0
		f.logf("gitstate: %s: %v; triggers wait until it is back", what, err)
	case now.Sub(f.lastLogged) >= failLogEvery:
		f.logf("gitstate: %s: still failing after %s (%d more failures): %v", what, now.Sub(f.since).Round(time.Second), f.suppressed, err)
		f.lastLogged, f.suppressed = now, 0
	default:
		f.suppressed++
	}
}

func (f *failLog) ok() {
	if !f.down {
		return
	}
	f.down = false
	f.logf("gitstate: following the log again after %s", f.now().Sub(f.since).Round(time.Second))
}

func (s *Service) handle(env contracts.Envelope) {
	if env.SessionID == "" {
		return
	}
	switch env.Type {
	case contracts.FileChanged:
		s.Trigger(env.SessionID, contracts.GitPartChangeSet, contracts.GitPartCounts)
	case contracts.SessionAgentState:
		var st contracts.AgentState
		if json.Unmarshal(env.Payload, &st) != nil || st.State != contracts.AgentIdle {
			return
		}
		s.Trigger(env.SessionID, contracts.GitPartChangeSet, contracts.GitPartCounts)
	case contracts.CommitCreated, contracts.CommitUndone, contracts.MergeCompleted:
		s.Trigger(env.SessionID, contracts.GitPartRepos, contracts.GitPartChangeSet, contracts.GitPartCounts)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// --- fingerprints: what counts as "changed" ---

func reposPrint(state []contracts.SessionRepoState) string {
	cp := make([]contracts.SessionRepoState, len(state))
	for i, r := range state {
		r.Worktree.ComputedAt = time.Time{}
		if r.BaseWorktree != nil {
			b := *r.BaseWorktree
			b.ComputedAt = time.Time{}
			r.BaseWorktree = &b
		}
		cp[i] = r
	}
	b, _ := json.Marshal(cp)
	return string(b)
}

func changeSetPrint(set contracts.ChangeSet) string {
	set.ComputedAt = time.Time{}
	b, _ := json.Marshal(set)
	return string(b)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
