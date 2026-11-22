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
)

// Repos is the repos part: GET /sessions/{id}/repos's body, and every
// checkout the session works in (each repo's folder and, for an isolated
// one, the user's checkout), for the repo watcher.
type Repos struct {
	State     []contracts.SessionRepoState
	Checkouts []string
}

// Computers are the Router's computes, unchanged from before the cache.
type Computers struct {
	Repos     func(ctx context.Context, sessionID string) (Repos, error)
	ChangeSet func(ctx context.Context, sessionID, vs string) (contracts.ChangeSet, error)
	// Counts recounts the session's header counts (they land on the session
	// record, and go out as session.updated) and returns a fingerprint of
	// them afterwards, to tell a changed result from an unchanged one.
	Counts func(ctx context.Context, sessionID string) (string, error)
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

	Debounce, MaxWait, WarmFor, BaselineTTL, Timeout, SweepEvery time.Duration
	// Now is the clock (tests). Nil: time.Now.
	Now func() time.Time
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
	// is announced.
	owed bool
	// changedAcc carries "the result changed" over a dirty rerun, which is
	// announced once at its end.
	changedAcc bool
	readAt     time.Time
}

func (p *part) updating() bool { return p.running || p.dirty || p.queued }

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
}

// Service is the Router's git state. Safe for concurrent use.
type Service struct {
	opts Options
	comp Computers
	// base is what every recompute runs under (with a timeout): never a
	// request's context, so a window going away can't cancel one.
	base   context.Context
	cancel context.CancelFunc

	emitMu sync.Mutex // orders appends with their generations
	mu     sync.Mutex
	sess   map[string]*session
	active map[string]bool
	wg     sync.WaitGroup
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
	if opts.Now == nil {
		opts.Now = time.Now
	}
	base, cancel := context.WithCancel(context.Background())
	return &Service{opts: opts, base: base, cancel: cancel, sess: map[string]*session{}, active: map[string]bool{}}
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
	// A failed compute is tried again on the next read, as a request used
	// to be: a passing failure mustn't stand until the next trigger.
	if !p.computed || p.stale || p.err != nil {
		s.startLocked(ss, sl, true)
	}
}

// --- triggers ---

// Refresh recomputes the session's counts and repos now (POST
// /worktree/refresh): answered at once, announced when done.
func (s *Service) Refresh(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.readLocked(sessionID)
	s.startLocked(ss, slotCounts, true)
	s.startLocked(ss, slotRepos, true)
}

// RepoChanged is the repo watcher's settled change of the repo whose
// shared git dir is gitDir: repos and counts of every kept session using it.
func (s *Service) RepoChanged(gitDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.sess {
		if ss.gitDirs[gitDir] {
			s.triggerLocked(ss, []string{contracts.GitPartRepos, contracts.GitPartCounts})
		}
	}
}

// Trigger is an event saying parts of the session may have moved:
// debounced, for a session kept warm (kept, or active).
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
		s.startLocked(ss, sl, true)
	}
}

// --- recompute ---

func (s *Service) startLocked(ss *session, sl slot, owed bool) {
	p := s.partLocked(ss, sl)
	if owed {
		p.owed = true
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

func (s *Service) run(ss *session, sl slot) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.base, s.opts.Timeout)
	s.mu.Lock()
	comp := s.comp
	s.mu.Unlock()
	var (
		repos Repos
		set   contracts.ChangeSet
		fp    string
		err   error
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
			fp, err = comp.Counts(ctx, ss.id)
		}
	}
	cancel()
	if err != nil {
		fp = "error: " + err.Error()
	}

	s.mu.Lock()
	p := ss.parts[sl]
	if s.base.Err() != nil || p == nil {
		s.mu.Unlock()
		return
	}
	p.running = false
	changed := !p.computed || p.fp != fp
	p.computed, p.at, p.fp, p.stale, p.err = true, s.opts.Now(), fp, false, err
	if err == nil {
		p.hasValue = true
		p.repos, p.set = repos, set
	}
	targetsMoved := false
	if sl == slotRepos && err == nil {
		targetsMoved = s.setTargetsLocked(ss, repos)
	}
	p.changedAcc = p.changedAcc || changed
	var announce *contracts.GitStateUpdatedPayload
	if p.dirty {
		p.dirty = false
		p.running = true
		s.wg.Add(1)
		go s.run(ss, sl) // the one rerun; announced at its end
	} else if p.owed {
		announce = &contracts.GitStateUpdatedPayload{SessionID: ss.id, Parts: []string{sl.part}, Changed: []string{}}
		if p.changedAcc {
			announce.Changed = []string{sl.part}
		}
		p.owed, p.changedAcc = false, false
	} else {
		p.changedAcc = false
	}
	s.mu.Unlock()

	if targetsMoved && s.opts.TargetsChanged != nil {
		s.opts.TargetsChanged()
	}
	if announce != nil {
		s.emit(ss, *announce)
	}
}

// emit appends the announcement with the session's next generation; one at
// a time, so the log's order is the generations' order.
func (s *Service) emit(ss *session, p contracts.GitStateUpdatedPayload) {
	if s.opts.Emit == nil {
		return
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.mu.Lock()
	// Clock-based, so a window that saw a previous Router's generations
	// still sees these as newer.
	g := max(ss.generation+1, s.opts.Now().UnixMicro())
	ss.generation = g
	s.mu.Unlock()
	p.Generation = g
	ctx, cancel := context.WithTimeout(s.base, 10*time.Second)
	defer cancel()
	if err := s.opts.Emit(ctx, p); err != nil && s.base.Err() == nil {
		log.Printf("gitstate: announce %s %v: %v", p.SessionID, p.Parts, err)
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
	return ss
}

func (s *Service) newSessionLocked(sessionID string) *session {
	ss := &session{id: sessionID, lastRead: s.opts.Now(), parts: map[slot]*part{}}
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
				s.startLocked(ss, sl, false)
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
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
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
