package gitstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// fakeComp counts and optionally holds each compute; its answers come from
// values a test sets.
type fakeComp struct {
	mu       sync.Mutex
	runs     map[string]int
	hold     map[string]chan struct{} // part → released to let a compute finish
	counts   string
	files    []string
	ctxs     map[string]context.Context
	started  chan string
	checkout []string
	// roots: the repos part's roots; countRoots: what a recount counted in.
	roots      []string
	countRoots []string
	// heads: a session's checkout HEAD (default "h0"); baseHeads: its base
	// branch's head.
	heads, baseHeads map[string]string
	// errs: a part's compute fails with this, while set.
	errs map[string]error
	// running and peak: computes in progress now, and the most at once.
	running, peak int
	// order: "session part" per compute start, in order; stash: a
	// session's refs/stash.
	order []string
	stash map[string]string
}

func newFakeComp() *fakeComp {
	return &fakeComp{runs: map[string]int{}, hold: map[string]chan struct{}{}, ctxs: map[string]context.Context{}, errs: map[string]error{}, heads: map[string]string{}, stash: map[string]string{}, baseHeads: map[string]string{}, counts: "1", roots: []string{"/r"}, started: make(chan string, 256)}
}

func (f *fakeComp) begin(ctx context.Context, id, part string) error {
	f.mu.Lock()
	f.runs[part]++
	f.order = append(f.order, id+" "+part)
	f.ctxs[part] = ctx
	f.running++
	f.peak = max(f.peak, f.running)
	h := f.hold[part]
	f.mu.Unlock()
	f.started <- part
	if h != nil {
		select {
		case <-h:
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running--
	return f.errs[part]
}

func (f *fakeComp) computers() Computers {
	return Computers{
		Repos: func(ctx context.Context, id string) (Repos, error) {
			if err := f.begin(ctx, id, contracts.GitPartRepos); err != nil {
				return Repos{}, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			r := Repos{Checkouts: append([]string(nil), f.checkout...), Roots: append([]string(nil), f.roots...), Stash: map[string]string{}}
			for _, root := range f.roots {
				r.Stash[root] = f.stash[id]
			}
			for _, root := range f.roots {
				st := contracts.SessionRepoState{Root: root, Worktree: contracts.RepoWorktree{Path: root, GitDir: root + "/.git", Branch: "main", Head: f.heads[id], ComputedAt: time.Now()}}
				if bh := f.baseHeads[id]; bh != "" {
					st.Base = "main"
					st.BaseWorktree = &contracts.RepoWorktree{Path: root, GitDir: root + "/.git", Branch: "main", Head: bh}
				}
				r.State = append(r.State, st)
			}
			return r, nil
		},
		ChangeSet: func(ctx context.Context, id, vs string) (contracts.ChangeSet, error) {
			if err := f.begin(ctx, id, contracts.GitPartChangeSet+":"+vs); err != nil {
				return contracts.ChangeSet{}, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			set := contracts.ChangeSet{Baseline: vs, ComputedAt: time.Now()}
			for _, p := range f.files {
				set.Files = append(set.Files, contracts.ChangedFile{Path: p})
			}
			return set, nil
		},
		Counts: func(ctx context.Context, id string) (Counts, error) {
			if err := f.begin(ctx, id, contracts.GitPartCounts); err != nil {
				return Counts{}, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return Counts{Print: f.counts, Roots: append([]string(nil), f.countRoots...)}, nil
		},
	}
}

func (f *fakeComp) runsOf(part string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs[part]
}

func (f *fakeComp) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeComp) holdPart(part string) chan struct{} {
	ch := make(chan struct{})
	f.mu.Lock()
	f.hold[part] = ch
	f.mu.Unlock()
	return ch
}

type emitted struct {
	mu    sync.Mutex
	evs   []contracts.GitStateUpdatedPayload
	times []time.Time
	// fail: the next this many appends fail; tries counts every attempt.
	fail  int
	tries int
}

func (e *emitted) emit(_ context.Context, p contracts.GitStateUpdatedPayload) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tries++
	if e.fail > 0 {
		e.fail--
		return errors.New("data service down")
	}
	e.evs = append(e.evs, p)
	e.times = append(e.times, time.Now())
	return nil
}

func (e *emitted) all() []contracts.GitStateUpdatedPayload {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]contracts.GitStateUpdatedPayload(nil), e.evs...)
}

func (e *emitted) forPart(part string) []contracts.GitStateUpdatedPayload {
	var out []contracts.GitStateUpdatedPayload
	for _, p := range e.all() {
		if slices.Contains(p.Parts, part) {
			out = append(out, p)
		}
	}
	return out
}

func newTestService(t *testing.T, opts Options) (*Service, *fakeComp, *emitted) {
	t.Helper()
	em := &emitted{}
	if opts.Emit == nil {
		opts.Emit = em.emit
	}
	if opts.Debounce == 0 {
		opts.Debounce = 20 * time.Millisecond
	}
	if opts.MaxWait == 0 {
		opts.MaxWait = 100 * time.Millisecond
	}
	if opts.AskFloor == 0 {
		opts.AskFloor = 30 * time.Millisecond
	}
	s := New(opts)
	fc := newFakeComp()
	s.Bind(fc.computers())
	t.Cleanup(s.Close)
	return s, fc, em
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A cold read answers at once with nothing and updating, starts the
// compute, and its end is announced (a window is waiting on it); the next
// read serves the kept answer.
func TestColdReadAnswersAtOnceAndAnnounces(t *testing.T) {
	s, fc, em := newTestService(t, Options{})
	a := s.ChangeSet("ses_1", contracts.BaselineSessionStart)
	if a.Computed || !a.Updating || a.Err != nil {
		t.Fatalf("cold read = %+v, want not computed, updating", a)
	}
	r := s.Repos("ses_1")
	if r.Computed || !r.Updating || r.State != nil {
		t.Fatalf("cold repos = %+v", r)
	}
	eventually(t, "both announced", func() bool {
		return len(em.forPart(contracts.GitPartRepos)) == 1 && len(em.forPart(contracts.GitPartChangeSet)) == 1
	})
	for _, p := range em.all() {
		if p.SessionID != "ses_1" || !slices.Equal(p.Changed, p.Parts) || p.Generation == 0 {
			t.Errorf("announcement = %+v, want its parts changed (a first answer), with a generation", p)
		}
	}
	if got := em.all(); len(got) != 1 {
		t.Errorf("announcements = %+v, want one covering both (computed together)", got)
	}
	a = s.ChangeSet("ses_1", contracts.BaselineSessionStart)
	if !a.Computed || a.Updating || a.Set.Baseline != contracts.BaselineSessionStart {
		t.Errorf("warm read = %+v", a)
	}
	if n := fc.runsOf("changeset:start"); n != 1 {
		t.Errorf("computes = %d, want 1 (the warm read serves the kept set)", n)
	}
}

// One compute per part at a time: triggers during a run mark it dirty for
// exactly one rerun, announced once, at the rerun's end.
func TestSingleFlightWithOneDirtyRerun(t *testing.T) {
	s, fc, em := newTestService(t, Options{})
	release := fc.holdPart(contracts.GitPartCounts)
	s.Refresh("ses_1")
	<-fc.started // repos, counts, the change set (cold)
	<-fc.started
	<-fc.started
	for range 3 {
		s.Refresh("ses_1")
	}
	if n := fc.runsOf(contracts.GitPartCounts); n != 1 {
		t.Fatalf("counts computes while one runs = %d, want 1", n)
	}
	fc.mu.Lock()
	fc.counts = "2"
	fc.hold[contracts.GitPartCounts] = nil
	fc.mu.Unlock()
	close(release)
	eventually(t, "the rerun's announcement", func() bool { return len(em.forPart(contracts.GitPartCounts)) == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := fc.runsOf(contracts.GitPartCounts); n != 2 {
		t.Errorf("counts computes = %d, want 2 (one run, one rerun)", n)
	}
	got := em.forPart(contracts.GitPartCounts)
	if len(got) != 1 || !slices.Contains(got[0].Changed, contracts.GitPartCounts) {
		t.Errorf("counts announcements = %+v, want one, changed", got)
	}
}

// A triggered recompute whose result didn't change still announces its
// end (the window marked the part "updating"), with an empty changed.
func TestUnchangedRecomputeAnnouncesWithEmptyChanged(t *testing.T) {
	s, _, em := newTestService(t, Options{})
	s.ChangeSet("ses_1", contracts.BaselineSessionStart)
	eventually(t, "the first compute", func() bool { return len(em.all()) == 1 })
	s.Trigger("ses_1", contracts.GitPartChangeSet)
	eventually(t, "the second announcement", func() bool { return len(em.all()) == 2 })
	p := em.all()[1]
	if !slices.Equal(p.Parts, []string{contracts.GitPartChangeSet}) || p.Changed == nil || len(p.Changed) != 0 {
		t.Errorf("announcement = %+v, want parts [changeset], changed []", p)
	}
	if p.Generation <= em.all()[0].Generation {
		t.Errorf("generation %d not above %d", p.Generation, em.all()[0].Generation)
	}
	raw, _ := json.Marshal(p)
	if !strings.Contains(string(raw), `"changed":[]`) {
		t.Errorf("payload %s, want changed as an empty array", raw)
	}
}

// Triggers in a burst are one recompute, debounced; a steady stream still
// recomputes within MaxWait.
func TestTriggersDebounceAndCap(t *testing.T) {
	s, fc, _ := newTestService(t, Options{Debounce: 80 * time.Millisecond, MaxWait: 200 * time.Millisecond})
	s.ChangeSet("ses_1", contracts.BaselineSessionStart)
	eventually(t, "the first compute", func() bool { return fc.runsOf("changeset:start") == 1 })
	for range 5 {
		s.Trigger("ses_1", contracts.GitPartChangeSet)
		time.Sleep(10 * time.Millisecond)
	}
	if n := fc.runsOf("changeset:start"); n != 1 {
		t.Errorf("computes during the burst = %d, want 1 (debounced)", n)
	}
	eventually(t, "the burst's one recompute", func() bool { return fc.runsOf("changeset:start") == 2 })
	time.Sleep(150 * time.Millisecond)
	if n := fc.runsOf("changeset:start"); n != 2 {
		t.Errorf("computes after the burst = %d, want 2", n)
	}
	// Steady: every 40 ms for 500 ms; the 80 ms quiet never comes, the cap does.
	stop := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(stop) {
		s.Trigger("ses_1", contracts.GitPartChangeSet)
		time.Sleep(40 * time.Millisecond)
	}
	if n := fc.runsOf("changeset:start"); n < 3 {
		t.Errorf("computes over a steady stream = %d, want the cap to have fired", n)
	}
}

// A trigger reaches only kept (or active) sessions: an unknown one stays
// cold until read.
func TestTriggerIgnoresColdSessions(t *testing.T) {
	s, fc, em := newTestService(t, Options{})
	s.Trigger("ses_cold", contracts.GitPartChangeSet, contracts.GitPartCounts)
	time.Sleep(80 * time.Millisecond)
	if n := fc.runsOf("changeset:start") + fc.runsOf(contracts.GitPartCounts); n != 0 || len(em.all()) != 0 || s.Kept("ses_cold") {
		t.Errorf("a cold session's trigger ran %d computes, %d announcements", n, len(em.all()))
	}
}

// Change sets against base or main are on demand: a trigger marks them
// stale (it doesn't recompute them); the next read serves the kept set,
// updating, and recomputes behind it.
func TestOtherBaselinesRecomputeOnRead(t *testing.T) {
	s, fc, em := newTestService(t, Options{})
	s.ChangeSet("ses_1", contracts.BaselineBase)
	eventually(t, "the base compute", func() bool { return len(em.all()) == 1 })
	s.Trigger("ses_1", contracts.GitPartChangeSet)
	eventually(t, "the start recompute", func() bool { return fc.runsOf("changeset:start") == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := fc.runsOf("changeset:base"); n != 1 {
		t.Fatalf("base computes after a trigger = %d, want 1 (not kept warm)", n)
	}
	a := s.ChangeSet("ses_1", contracts.BaselineBase)
	if !a.Computed || !a.Updating {
		t.Errorf("stale base read = %+v, want the kept set, updating", a)
	}
	eventually(t, "the base recompute", func() bool { return fc.runsOf("changeset:base") == 2 })
}

// The warm set: a session read lately or active is kept; one unread for
// WarmFor and not active is dropped (and recomputed on the next read); a
// base change set unread for BaselineTTL is dropped.
func TestWarmSetEviction(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	active := []string{"ses_active"}
	s, fc, _ := newTestService(t, Options{Now: clock, Active: func(context.Context) ([]string, error) { return active, nil }})

	s.ChangeSet("ses_read", contracts.BaselineSessionStart)
	s.ChangeSet("ses_read", contracts.BaselineBase)
	s.Sweep(context.Background()) // boot: warms the active session
	eventually(t, "the computes", func() bool {
		return fc.runsOf("changeset:start") == 2 && fc.runsOf("changeset:base") == 1 && fc.runsOf(contracts.GitPartCounts) == 1
	})
	time.Sleep(20 * time.Millisecond)

	advance(6 * time.Minute)
	s.Sweep(context.Background())
	s.mu.Lock()
	_, base := s.sess["ses_read"].parts[slot{part: contracts.GitPartChangeSet, vs: contracts.BaselineBase}]
	s.mu.Unlock()
	if base {
		t.Error("base change set kept after 6 min unread, want dropped after 5")
	}
	if !s.Kept("ses_read") {
		t.Error("session dropped after 6 min, want kept for 15")
	}

	advance(10 * time.Minute)
	s.Sweep(context.Background())
	if s.Kept("ses_read") {
		t.Error("session read 16 min ago still kept")
	}
	if !s.Kept("ses_active") {
		t.Error("active session dropped")
	}
	a := s.ChangeSet("ses_read", contracts.BaselineSessionStart)
	if a.Computed || !a.Updating {
		t.Errorf("read after eviction = %+v, want cold", a)
	}
}

// Boot warm-up computes active sessions in the background and announces
// nothing (nobody asked); a read that arrives while it runs is owed one.
func TestBootWarmUpAnnouncesOnlyWhenAsked(t *testing.T) {
	s, fc, em := newTestService(t, Options{Active: func(context.Context) ([]string, error) { return []string{"a", "b"}, nil }})
	release := fc.holdPart("changeset:start")
	s.Sweep(context.Background())
	eventually(t, "warm-up started", func() bool { return fc.runsOf("changeset:start") == 2 })
	if a := s.ChangeSet("a", contracts.BaselineSessionStart); !a.Updating {
		t.Errorf("read during the warm-up = %+v, want updating", a)
	}
	close(release)
	eventually(t, "a's announcement", func() bool { return len(em.forPart(contracts.GitPartChangeSet)) == 1 })
	time.Sleep(50 * time.Millisecond)
	got := em.forPart(contracts.GitPartChangeSet)
	if len(got) != 1 || got[0].SessionID != "a" {
		t.Errorf("announcements = %+v, want only a's (b was asked by nobody)", got)
	}
	if n := len(em.forPart(contracts.GitPartRepos)) + len(em.forPart(contracts.GitPartCounts)); n != 0 {
		t.Errorf("warm-up announced %d repos/counts computes, want 0", n)
	}
}

// The compute runs under the service's context, not a reader's: nothing
// a reader does can cancel it, and Close does.
func TestComputeContextIsTheServices(t *testing.T) {
	s, fc, _ := newTestService(t, Options{})
	release := fc.holdPart(contracts.GitPartCounts)
	s.Refresh("ses_1")
	eventually(t, "the compute", func() bool { return fc.runsOf(contracts.GitPartCounts) == 1 })
	fc.mu.Lock()
	ctx := fc.ctxs[contracts.GitPartCounts]
	fc.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		t.Error("compute has no timeout")
	}
	if ctx.Err() != nil {
		t.Fatal("compute cancelled already")
	}
	close(release)
	s.Close()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("after Close: ctx err = %v", ctx.Err())
	}
}

// repo.changed (in-process) recomputes repos and counts of the kept
// sessions using that git dir, and no others.
func TestRepoChangedReachesSessionsUsingTheGitDir(t *testing.T) {
	moved := make(chan struct{}, 4)
	s, fc, _ := newTestService(t, Options{TargetsChanged: func() { moved <- struct{}{} }})
	fc.checkout = []string{"/r"}
	s.Repos("ses_1")
	select {
	case <-moved:
	case <-time.After(5 * time.Second):
		t.Fatal("no TargetsChanged after the first repos compute")
	}
	if got, _ := s.Checkouts(context.Background()); !slices.Equal(got, []string{"/r"}) {
		t.Errorf("checkouts = %v", got)
	}
	s.RepoChanged("/other/.git")
	time.Sleep(60 * time.Millisecond)
	if n := fc.runsOf(contracts.GitPartCounts); n != 0 {
		t.Errorf("another repo's change recounted %d times", n)
	}
	s.RepoChanged("/r/.git")
	eventually(t, "repos and counts recomputed", func() bool {
		return fc.runsOf(contracts.GitPartRepos) == 2 && fc.runsOf(contracts.GitPartCounts) == 1
	})
}

// fakeEvents is a log head and a stream a test feeds.
type fakeEvents struct {
	head   int64
	mu     sync.Mutex
	sinces []int64
	types  [][]string
	ch     chan contracts.Envelope
}

func (f *fakeEvents) EventsHead(context.Context) (int64, error) { return f.head, nil }

func (f *fakeEvents) Stream(ctx context.Context, since int64, sessionID string, types []string) (<-chan contracts.Envelope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sinces = append(f.sinces, since)
	f.types = append(f.types, types)
	ch := make(chan contracts.Envelope)
	f.ch = ch
	return ch, nil
}

func (f *fakeEvents) send(t *testing.T, env contracts.Envelope) {
	t.Helper()
	eventually(t, "a stream", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.ch != nil })
	f.mu.Lock()
	ch := f.ch
	f.mu.Unlock()
	ch <- env
}

// Run follows the log from its head (never from 0), with the trigger
// types and not session.updated; a dropped stream resumes where it left
// off. Each trigger reaches its parts.
func TestRunFollowsFromTheHead(t *testing.T) {
	ev := &fakeEvents{head: 4242}
	s, fc, _ := newTestService(t, Options{Events: ev, Active: func(context.Context) ([]string, error) { return []string{"ses_1"}, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	eventually(t, "kept", func() bool { return s.Kept("ses_1") })
	eventually(t, "warm-up", func() bool { return fc.runsOf(contracts.GitPartCounts) == 1 })

	ev.send(t, contracts.Envelope{Seq: 4243, Type: contracts.SessionUpdated, SessionID: "ses_1"})
	agentState := func(state string) []byte { b, _ := json.Marshal(contracts.AgentState{State: state}); return b }
	ev.send(t, contracts.Envelope{Seq: 4244, Type: contracts.SessionAgentState, SessionID: "ses_1", Payload: agentState(contracts.AgentWorking)})
	time.Sleep(80 * time.Millisecond)
	if n := fc.runsOf(contracts.GitPartCounts); n != 1 {
		t.Errorf("session.updated / a working agent_state recounted: %d computes, want 1 (the warm-up)", n)
	}
	ev.send(t, contracts.Envelope{Seq: 4245, Type: contracts.SessionAgentState, SessionID: "ses_1", Payload: agentState(contracts.AgentIdle)})
	eventually(t, "turn end recount", func() bool { return fc.runsOf(contracts.GitPartCounts) == 2 })
	if n := fc.runsOf(contracts.GitPartRepos); n != 1 {
		t.Errorf("turn end recomputed repos (%d)", n)
	}
	ev.send(t, contracts.Envelope{Seq: 4246, Type: contracts.FileChanged, SessionID: "ses_1"})
	eventually(t, "file.changed", func() bool { return fc.runsOf(contracts.GitPartCounts) == 3 && fc.runsOf("changeset:start") == 3 })
	ev.send(t, contracts.Envelope{Seq: 4247, Type: contracts.CommitCreated, SessionID: "ses_1"})
	eventually(t, "commit.created: all parts", func() bool { return fc.runsOf(contracts.GitPartRepos) == 2 && fc.runsOf(contracts.GitPartCounts) == 4 })

	ev.mu.Lock()
	close(ev.ch) // the stream drops
	ev.ch = nil
	ev.mu.Unlock()
	eventually(t, "the resubscribe", func() bool { ev.mu.Lock(); defer ev.mu.Unlock(); return len(ev.sinces) == 2 })
	ev.mu.Lock()
	defer ev.mu.Unlock()
	if ev.sinces[0] != 4242 {
		t.Errorf("first subscribe since = %d, want the head 4242", ev.sinces[0])
	}
	if ev.sinces[1] != 4247 {
		t.Errorf("resubscribe since = %d, want the last seen 4247", ev.sinces[1])
	}
	if slices.Contains(ev.types[0], string(contracts.SessionUpdated)) {
		t.Errorf("types %v include session.updated", ev.types[0])
	}
	for _, want := range []contracts.EventType{contracts.FileChanged, contracts.SessionAgentState, contracts.CommitCreated, contracts.CommitUndone, contracts.MergeCompleted} {
		if !slices.Contains(ev.types[0], string(want)) {
			t.Errorf("types %v lack %s", ev.types[0], want)
		}
	}
}

// --- after review ---

// warmStart reads the session's start change set and repos and waits for
// both computes' announcement, so later tests start from a computed state.
func warmStart(t *testing.T, s *Service, fc *fakeComp, em *emitted, id string) {
	t.Helper()
	s.ChangeSet(id, contracts.BaselineSessionStart)
	s.Repos(id)
	eventually(t, "the first computes announced", func() bool {
		return len(em.forPart(contracts.GitPartChangeSet)) >= 1 && len(em.forPart(contracts.GitPartRepos)) >= 1
	})
}

// M1, precise: a repo change recomputes repos and counts of every kept
// session in that repo, but a change set only where its refs moved: the
// session whose checkout HEAD moved recomputes its start change set; a
// base change set is marked stale only when the base branch moved.
func TestRepoChangedRecomputesOnlyMovedChangeSets(t *testing.T) {
	s, fc, em := newTestService(t, Options{ShownFor: 100 * time.Millisecond})
	ids := []string{"s1", "s2", "s3"}
	for _, id := range ids {
		fc.set(func() { fc.heads[id] = "h0"; fc.baseHeads[id] = "b0" })
		s.Repos(id)
		eventually(t, "repos of "+id, func() bool { return s.Repos(id).Computed })
		// Computed once the refs are known (a change set computed before
		// its repos counts as moved on the next repo change).
		s.ChangeSet(id, contracts.BaselineSessionStart)
		eventually(t, "change set of "+id, func() bool { return s.ChangeSet(id, contracts.BaselineSessionStart).Computed })
	}
	s.ChangeSet("s2", contracts.BaselineBase)
	s.ChangeSet("s3", contracts.BaselineBase)
	eventually(t, "the base computes", func() bool { return fc.runsOf("changeset:base") == 2 })
	time.Sleep(150 * time.Millisecond) // nobody shows them now
	starts := fc.runsOf("changeset:start")
	// s1 committed (its HEAD moved); s3's base branch moved; s2 nothing.
	fc.set(func() { fc.heads["s1"] = "h1"; fc.baseHeads["s3"] = "b1" })
	s.RepoChanged("/r/.git")
	eventually(t, "repos and counts of all three", func() bool {
		return fc.runsOf(contracts.GitPartRepos) == 6 && fc.runsOf(contracts.GitPartCounts) == 3
	})
	_ = em
	eventually(t, "s1's change set", func() bool { return fc.runsOf("changeset:start") == starts+1 })
	time.Sleep(80 * time.Millisecond)
	if n := fc.runsOf("changeset:start"); n != starts+1 {
		t.Errorf("start change set computes = %d, want %d (only s1's HEAD moved)", n, starts+1)
	}
	for _, p := range em.all() {
		// s2/s3: their change sets ride along as done, never as changed.
		if p.SessionID != "s1" && slices.Contains(p.Changed, contracts.GitPartChangeSet) && slices.Contains(p.Parts, contracts.GitPartRepos) && len(p.Parts) == 3 {
			t.Errorf("%s announced its change set changed without a recompute: %+v", p.SessionID, p)
		}
	}
	if a := s.ChangeSet("s2", contracts.BaselineBase); a.Updating {
		t.Errorf("s2's base = %+v, want kept as is (its base didn't move)", a)
	}
	if a := s.ChangeSet("s3", contracts.BaselineBase); !a.Updating {
		t.Errorf("s3's base = %+v, want stale, recomputing on this read", a)
	}
	eventually(t, "s3's base recomputed on its read", func() bool { return fc.runsOf("changeset:base") == 3 })
}

// A shown session's compute (read within ShownFor) goes ahead of warm-up
// work, and never waits behind it: the low lane leaves a slot free.
func TestShownSessionIsNotStarvedByWarmUp(t *testing.T) {
	var ids []string
	for i := range 6 {
		ids = append(ids, fmt.Sprintf("w%d", i))
	}
	s, fc, _ := newTestService(t, Options{MaxConcurrent: 4, Active: func(context.Context) ([]string, error) { return ids, nil }})
	release := fc.holdPart("changeset:start")
	s.Sweep(context.Background()) // 18 warm-up computes; change sets hold
	eventually(t, "the low lane full", func() bool { return fc.runsOf("changeset:start") >= 1 })
	time.Sleep(80 * time.Millisecond)
	fc.mu.Lock()
	lowPeak := fc.peak
	fc.mu.Unlock()
	if lowPeak > 3 {
		t.Errorf("warm-up ran %d at once, want ≤ 3 (one slot kept for shown sessions)", lowPeak)
	}
	before := fc.runsOf(contracts.GitPartRepos)
	s.Repos("shown") // a window reads it: high lane
	eventually(t, "the shown session's compute despite the held warm-up", func() bool {
		return fc.runsOf(contracts.GitPartRepos) == before+1
	})
	close(release)
	fc.set(func() { fc.hold["changeset:start"] = nil })
}

// No more than MaxPerSession computes of one session at once.
func TestPerSessionCap(t *testing.T) {
	s, fc, _ := newTestService(t, Options{MaxConcurrent: 4})
	rel := []chan struct{}{fc.holdPart(contracts.GitPartRepos), fc.holdPart("changeset:start"), fc.holdPart(contracts.GitPartCounts), fc.holdPart("changeset:base")}
	s.Refresh("ses_1")
	s.ChangeSet("ses_1", contracts.BaselineBase)
	time.Sleep(80 * time.Millisecond)
	fc.mu.Lock()
	peak := fc.peak
	fc.mu.Unlock()
	if peak != MaxPerSession {
		t.Errorf("one session's concurrent computes = %d, want %d", peak, MaxPerSession)
	}
	// Another session still gets the free slots.
	total := func() int {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		n := 0
		for _, v := range fc.runs {
			n += v
		}
		return n
	}
	before := total()
	s.Repos("ses_2")
	eventually(t, "the other session's compute", func() bool { return total() == before+1 })
	for _, c := range rel {
		close(c)
	}
}

// The batch goes out MaxWait after its first part even if no other compute
// ends by then.
func TestBatchFlushesAfterMaxWaitWithoutAnotherEnd(t *testing.T) {
	s, fc, em := newTestService(t, Options{MaxWait: 150 * time.Millisecond})
	release := fc.holdPart(contracts.GitPartCounts)
	s.ChangeSet("ses_1", contracts.BaselineSessionStart)
	s.Refresh("ses_1") // counts hold
	eventually(t, "the change set", func() bool { return fc.runsOf("changeset:start") == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := len(em.all()); n != 0 {
		t.Fatalf("announced %d while counts run, want the batch to wait", n)
	}
	eventually(t, "the batch by MaxWait", func() bool { return len(em.all()) == 1 })
	if got := em.all()[0]; !slices.Contains(got.Parts, contracts.GitPartChangeSet) || slices.Contains(got.Parts, contracts.GitPartCounts) {
		t.Errorf("batch = %+v, want the change set without the still-running counts", got)
	}
	close(release)
}

// Rapid forced asks (max_age_ms 0, any caller) collapse: one compute now,
// one more once AskFloor has passed, however many asks came in between.
func TestForcedAsksCollapse(t *testing.T) {
	s, fc, _ := newTestService(t, Options{AskFloor: 200 * time.Millisecond})
	for range 20 {
		s.Revalidate("ses_1", Ask{Parts: []string{contracts.GitPartChangeSet}, Baseline: contracts.BaselineMain})
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond)
	if n := fc.runsOf("changeset:main"); n != 2 {
		t.Errorf("computes for 20 forced asks within the floor = %d, want 2", n)
	}
}

// M2a/M2c: a bodiless refresh (open, focus) recomputes the change set only
// when the kept one is older than OpenMaxAge; an Ask with MaxAge too.
func TestRefreshRecomputesAStaleChangeSetOnly(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	s, fc, em := newTestService(t, Options{Now: clock})
	warmStart(t, s, fc, em, "ses_1")
	s.Refresh("ses_1")
	eventually(t, "the counts", func() bool { return fc.runsOf(contracts.GitPartCounts) == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := fc.runsOf("changeset:start"); n != 1 {
		t.Errorf("change set computes after a refresh of a fresh one = %d, want 1", n)
	}
	if n := fc.runsOf(contracts.GitPartRepos); n != 1 {
		t.Errorf("repos computes after a refresh of fresh ones = %d, want 1 (30 s max age)", n)
	}
	s.Revalidate("ses_1", Ask{Parts: []string{contracts.GitPartChangeSet}, MaxAge: 30 * time.Second})
	time.Sleep(50 * time.Millisecond)
	if n := fc.runsOf("changeset:start"); n != 1 {
		t.Errorf("change set computes after an open of a fresh one = %d, want 1", n)
	}
	mu.Lock()
	now = now.Add(31 * time.Second)
	mu.Unlock()
	s.Refresh("ses_1")
	eventually(t, "a focus recomputes an old change set", func() bool { return fc.runsOf("changeset:start") == 2 })
	eventually(t, "and old repos", func() bool { return fc.runsOf(contracts.GitPartRepos) == 2 })
	mu.Lock()
	now = now.Add(31 * time.Second)
	mu.Unlock()
	s.Revalidate("ses_1", Ask{Parts: []string{contracts.GitPartChangeSet}, MaxAge: 30 * time.Second})
	eventually(t, "an open recomputes an old change set", func() bool { return fc.runsOf("changeset:start") == 3 })
}

// M2b: Retry (an Ask without MaxAge) recomputes at once, fresh or not,
// for the baseline it names, and is announced.
func TestRevalidateRecomputesAtOnce(t *testing.T) {
	s, fc, em := newTestService(t, Options{})
	warmStart(t, s, fc, em, "ses_1")
	n := len(em.all())
	fc.set(func() { fc.files = []string{"outside.go"} })
	s.Revalidate("ses_1", Ask{Parts: []string{contracts.GitPartChangeSet}})
	eventually(t, "the recompute", func() bool { return fc.runsOf("changeset:start") == 2 })
	eventually(t, "its announcement", func() bool { return len(em.all()) == n+1 })
	if got := em.all()[n]; !slices.Equal(got.Changed, []string{contracts.GitPartChangeSet}) || got.Baseline != "" {
		t.Errorf("announcement = %+v, want changeset changed, no baseline", got)
	}
	if a := s.ChangeSet("ses_1", contracts.BaselineSessionStart); len(a.Set.Files) != 1 {
		t.Errorf("set = %+v, want outside.go", a.Set)
	}
	s.Revalidate("ses_1", Ask{Parts: []string{contracts.GitPartChangeSet}, Baseline: contracts.BaselineBase})
	eventually(t, "the base compute", func() bool { return fc.runsOf("changeset:base") == 1 })
}

// M3: a recount that counted in a repo the repos part doesn't hold (the
// child's turn-end recount wrote it to the record first, so the record
// before and after this recount agree) recomputes repos, announced with
// the counts in one event; one that counted in held repos doesn't.
func TestRecountInANewRepoRecomputesRepos(t *testing.T) {
	s, fc, em := newTestService(t, Options{})
	warmStart(t, s, fc, em, "ses_1")
	fc.set(func() { fc.countRoots = []string{"/r"} })
	s.Trigger("ses_1", contracts.GitPartCounts)
	eventually(t, "the recount", func() bool { return fc.runsOf(contracts.GitPartCounts) == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := fc.runsOf(contracts.GitPartRepos); n != 1 {
		t.Fatalf("repos computes after a recount in held repos = %d, want 1", n)
	}
	n := len(em.all())
	// The child already recorded /r2 (its turn end ran first); now the
	// Router's recount sees it, and the repos part doesn't hold it.
	fc.set(func() { fc.countRoots = []string{"/r", "/r2"}; fc.roots = []string{"/r", "/r2"} })
	s.Trigger("ses_1", contracts.GitPartCounts)
	eventually(t, "repos recomputed", func() bool { return fc.runsOf(contracts.GitPartRepos) == 2 })
	eventually(t, "the announcement", func() bool { return len(em.all()) == n+1 })
	got := em.all()[n]
	if !slices.Equal(got.Parts, []string{contracts.GitPartRepos, contracts.GitPartCounts}) || !slices.Contains(got.Changed, contracts.GitPartRepos) {
		t.Errorf("announcement = %+v, want repos and counts in one, repos changed", got)
	}
	if r := s.Repos("ses_1"); len(r.State) != 2 {
		t.Errorf("repos = %+v, want /r and /r2", r.State)
	}
	s.mu.Lock()
	watched := s.sess["ses_1"].gitDirs["/r2/.git"]
	s.mu.Unlock()
	if !watched {
		t.Error("the new repo's git dir isn't matched for repo changes")
	}
}

// M4a: a failed announcement is retried; one that keeps failing leaves
// its parts owed, so the next compute announces them (changed carried).
func TestEmitFailureIsRetriedThenOwed(t *testing.T) {
	s, fc, em := newTestService(t, Options{})
	em.fail = 1
	s.ChangeSet("ses_1", contracts.BaselineSessionStart)
	eventually(t, "the retried announcement", func() bool { return len(em.all()) == 1 })
	if em.tries != 2 {
		t.Errorf("tries = %d, want 2 (one failure, one retry)", em.tries)
	}
	em.mu.Lock()
	em.fail = 100
	em.mu.Unlock()
	fc.set(func() { fc.files = []string{"x.go"} })
	s.Trigger("ses_1", contracts.GitPartChangeSet)
	eventually(t, "every try failed", func() bool { em.mu.Lock(); defer em.mu.Unlock(); return em.tries == 2+1+len(emitRetries) })
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	p := s.sess["ses_1"].parts[slotStart]
	owed, acc := p.owed, p.changedAcc
	s.mu.Unlock()
	if !owed || !acc {
		t.Fatalf("after a lost announcement: owed %v, changed carried %v; want both", owed, acc)
	}
	em.mu.Lock()
	em.fail = 0
	em.mu.Unlock()
	// The next compute of the part — a read-started one included — says so.
	s.Trigger("ses_1", contracts.GitPartChangeSet)
	eventually(t, "the owed announcement", func() bool { return len(em.all()) == 2 })
	if got := em.all()[1]; !slices.Equal(got.Changed, []string{contracts.GitPartChangeSet}) {
		t.Errorf("announcement = %+v, want the lost change carried", got)
	}
}

// M4c: an evicted session keeps nothing to serve: a trigger for it does
// nothing, and the next read is cold (updating), recomputes and announces.
func TestEvictedSessionReadsColdAfterATrigger(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	s, fc, em := newTestService(t, Options{Now: clock})
	warmStart(t, s, fc, em, "ses_1")
	mu.Lock()
	now = now.Add(DefaultWarmFor + time.Minute)
	mu.Unlock()
	s.Sweep(context.Background())
	if s.Kept("ses_1") {
		t.Fatal("not evicted")
	}
	n := len(em.all())
	s.Trigger("ses_1", contracts.GitPartChangeSet)
	s.RepoChanged("/r/.git")
	time.Sleep(60 * time.Millisecond)
	if fc.runsOf("changeset:start") != 1 || len(em.all()) != n {
		t.Fatal("an evicted session's trigger computed something")
	}
	a := s.ChangeSet("ses_1", contracts.BaselineSessionStart)
	if a.Computed || !a.Updating {
		t.Errorf("read after eviction = %+v, want cold and updating, never the old set as fresh", a)
	}
	eventually(t, "recomputed and announced", func() bool { return fc.runsOf("changeset:start") == 2 && len(em.all()) == n+1 })
}

// S1: a run that ends while its part is queued again (a trigger in the
// debounce) isn't announced; the queued run is.
func TestQueuedPartAnnouncesAtTheQueuedRunsEnd(t *testing.T) {
	s, fc, em := newTestService(t, Options{Debounce: 150 * time.Millisecond, MaxWait: time.Second})
	warmStart(t, s, fc, em, "ses_1")
	n := len(em.all())
	release := fc.holdPart("changeset:start")
	s.Revalidate("ses_1", Ask{Parts: []string{contracts.GitPartChangeSet}})
	eventually(t, "the run", func() bool { return fc.runsOf("changeset:start") == 2 })
	s.Trigger("ses_1", contracts.GitPartChangeSet) // queued, in the debounce
	fc.set(func() { fc.hold["changeset:start"] = nil })
	close(release)
	time.Sleep(60 * time.Millisecond) // the run ended; the debounce hasn't fired
	if got := len(em.all()); got != n {
		t.Fatalf("announced %d while the part is queued, want none yet", got-n)
	}
	eventually(t, "the queued run", func() bool { return fc.runsOf("changeset:start") == 3 })
	eventually(t, "its announcement", func() bool { return len(em.all()) == n+1 })
	time.Sleep(50 * time.Millisecond)
	if got := len(em.all()); got != n+1 {
		t.Errorf("announcements = %d, want one", got-n)
	}
}

// S2: under a write storm a session announces once per fire (changeset and
// counts together), so no more than one per MaxWait, not one per part.
func TestWriteStormAnnouncesOncePerFire(t *testing.T) {
	const debounce, maxWait = 20 * time.Millisecond, 100 * time.Millisecond
	s, fc, em := newTestService(t, Options{Debounce: debounce, MaxWait: maxWait})
	warmStart(t, s, fc, em, "ses_1")
	n := len(em.all())
	start := time.Now()
	for time.Since(start) < time.Second {
		s.Trigger("ses_1", contracts.GitPartChangeSet, contracts.GitPartCounts) // a file.changed
		time.Sleep(5 * time.Millisecond)
	}
	elapsed := time.Since(start)
	time.Sleep(3 * maxWait)
	got := em.all()[n:]
	limit := int(elapsed/maxWait) + 2
	if len(got) > limit || len(got) == 0 {
		t.Errorf("announcements over %v of writes = %d, want 1..%d", elapsed, len(got), limit)
	}
	// Together, but for a part held back because a trigger landed between
	// the two ends (queued again: its queued run announces it).
	both := 0
	for _, p := range got {
		if slices.Equal(p.Parts, []string{contracts.GitPartChangeSet, contracts.GitPartCounts}) {
			both++
		}
	}
	if both*2 < len(got) {
		t.Errorf("announcements listing changeset and counts together = %d of %d, want most", both, len(got))
	}
	// And the change set is never left unannounced for long: a fire every
	// MaxWait, its compute, and a deferral of at most MaxWait.
	em.mu.Lock()
	var csTimes []time.Time
	for i, p := range em.evs[n:] {
		if slices.Contains(p.Parts, contracts.GitPartChangeSet) {
			csTimes = append(csTimes, em.times[n+i])
		}
	}
	em.mu.Unlock()
	const computeBound = 100 * time.Millisecond
	bound := 2*maxWait + debounce + computeBound
	prev := start
	for _, at := range csTimes {
		if at.Sub(prev) > bound {
			t.Errorf("change set announcements %v apart, want ≤ %v", at.Sub(prev), bound)
		}
		prev = at
	}
	if start.Add(elapsed).Sub(prev) > bound {
		t.Errorf("last change set announcement %v before the storm ended, want ≤ %v", start.Add(elapsed).Sub(prev), bound)
	}
	t.Logf("%d announcements, %d change set computes over %v", len(got), fc.runsOf("changeset:start"), elapsed)
}

// S3 and the retry of a failed compute: a read-started recompute that
// fails as before isn't announced; reads retry a failing part at most once
// per ErrorRetryEvery; a trigger retries regardless, and the recovery is
// announced as changed.
func TestFailedComputeRetry(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	s, fc, em := newTestService(t, Options{Now: clock})
	fc.set(func() { fc.errs["changeset:start"] = errors.New("git: index locked") })
	if a := s.ChangeSet("ses_1", contracts.BaselineSessionStart); a.Computed {
		t.Fatal("first read computed")
	}
	eventually(t, "the failure announced (a first answer)", func() bool { return len(em.all()) == 1 })
	if a := s.ChangeSet("ses_1", contracts.BaselineSessionStart); a.Err == nil || a.Updating {
		t.Errorf("read after the failure = %+v, want the error, not retried yet", a)
	}
	time.Sleep(30 * time.Millisecond)
	if n := fc.runsOf("changeset:start"); n != 1 {
		t.Fatalf("computes = %d, want 1 (a read within ErrorRetryEvery doesn't retry)", n)
	}
	advance(DefaultErrorRetryEvery)
	s.ChangeSet("ses_1", contracts.BaselineSessionStart)
	eventually(t, "the read's retry", func() bool { return fc.runsOf("changeset:start") == 2 })
	time.Sleep(50 * time.Millisecond)
	if n := len(em.all()); n != 1 {
		t.Errorf("announcements = %d, want 1: the read's retry failed the same way", n)
	}
	fc.set(func() { delete(fc.errs, "changeset:start") })
	s.Trigger("ses_1", contracts.GitPartChangeSet)
	eventually(t, "the trigger's retry announced", func() bool { return len(em.all()) == 2 })
	if got := em.all()[1]; !slices.Equal(got.Changed, []string{contracts.GitPartChangeSet}) {
		t.Errorf("recovery announcement = %+v, want changed", got)
	}
	if a := s.ChangeSet("ses_1", contracts.BaselineSessionStart); !a.Computed || a.Err != nil {
		t.Errorf("read after recovery = %+v", a)
	}
}

// S4: no more than MaxConcurrent computes at once, the boot warm-up included.
func TestComputesAreCapped(t *testing.T) {
	ids := []string{"a", "b", "c", "d", "e"}
	s, fc, _ := newTestService(t, Options{MaxConcurrent: 2, Active: func(context.Context) ([]string, error) { return ids, nil }})
	release := fc.holdPart("changeset:start")
	s.Sweep(context.Background()) // 15 computes
	time.Sleep(80 * time.Millisecond)
	fc.mu.Lock()
	peak := fc.peak
	fc.mu.Unlock()
	if peak > 2 {
		t.Errorf("peak concurrent computes = %d, want ≤ 2", peak)
	}
	close(release)
	fc.set(func() { fc.hold["changeset:start"] = nil })
	eventually(t, "every warm-up compute", func() bool {
		return fc.runsOf("changeset:start") == 5 && fc.runsOf(contracts.GitPartRepos) == 5 && fc.runsOf(contracts.GitPartCounts) == 5
	})
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.peak > 2 {
		t.Errorf("peak concurrent computes = %d, want ≤ 2", fc.peak)
	}
}

// S7: a change set against another baseline is announced on its own,
// naming the baseline; it doesn't ride in (or stand for) session start's.
func TestOtherBaselineAnnouncesItsBaseline(t *testing.T) {
	s, fc, em := newTestService(t, Options{})
	warmStart(t, s, fc, em, "ses_1")
	n := len(em.all())
	s.ChangeSet("ses_1", contracts.BaselineMain)
	eventually(t, "main's announcement", func() bool { return len(em.all()) == n+1 })
	got := em.all()[n]
	if got.Baseline != contracts.BaselineMain || !slices.Equal(got.Parts, []string{contracts.GitPartChangeSet}) {
		t.Errorf("announcement = %+v, want baseline main, parts [changeset]", got)
	}
	raw, _ := json.Marshal(em.all()[0])
	if strings.Contains(string(raw), "baseline") {
		t.Errorf("start's payload %s carries a baseline, want it omitted", raw)
	}
}

// fakeHead fails its first headFails reads.
type flakyEvents struct {
	fakeEvents
	headFails int
}

func (f *flakyEvents) EventsHead(ctx context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.headFails > 0 {
		f.headFails--
		return 0, errors.New("connection refused")
	}
	return f.head, nil
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logLines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// S5 and S8: follow logs a failing head once (not per retry) and its
// recovery; after a dropped stream it resumes, and the new stream's
// events trigger.
func TestFollowLogsFailuresAndResumes(t *testing.T) {
	ev := &flakyEvents{fakeEvents: fakeEvents{head: 10}, headFails: 2}
	logs := &logLines{}
	s, fc, _ := newTestService(t, Options{Events: ev, Logf: logs.logf, Active: func(context.Context) ([]string, error) { return []string{"ses_1"}, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	eventually(t, "the first stream", func() bool { ev.mu.Lock(); defer ev.mu.Unlock(); return len(ev.sinces) == 1 })
	eventually(t, "warm-up", func() bool { return fc.runsOf(contracts.GitPartCounts) == 1 })
	lines := logs.all()
	if len(lines) != 2 || !strings.Contains(lines[0], "read the log head") || !strings.Contains(lines[1], "following the log again") {
		t.Errorf("log = %q, want one failure line and one recovery line", lines)
	}

	ev.mu.Lock()
	close(ev.ch)
	ev.ch = nil
	ev.mu.Unlock()
	eventually(t, "the resubscribe", func() bool { ev.mu.Lock(); defer ev.mu.Unlock(); return len(ev.sinces) == 2 })
	ev.send(t, contracts.Envelope{Seq: 11, Type: contracts.FileChanged, SessionID: "ses_1"})
	eventually(t, "the resumed stream's trigger", func() bool { return fc.runsOf(contracts.GitPartCounts) == 2 })
}

// --- round 3 ---

// A shown session (read within ShownFor) recomputes its change set on any
// repo change, even one no fingerprinted ref shows (a stash, say).
func TestRepoChangedRecomputesAShownSessionsChangeSet(t *testing.T) {
	s, fc, em := newTestService(t, Options{})
	warmStart(t, s, fc, em, "ses_1")
	eventually(t, "both computed", func() bool { return fc.runsOf("changeset:start") == 1 && fc.runsOf(contracts.GitPartRepos) == 1 })
	time.Sleep(30 * time.Millisecond)
	s.RepoChanged("/r/.git") // nothing in the fake moved
	eventually(t, "the shown session's change set", func() bool { return fc.runsOf("changeset:start") == 2 })
}

// A background session catches a stash through refs/stash in its start
// fingerprint, HEAD unchanged; and an unmoved one's change set rides in the
// batch as done, so a window's mark clears.
func TestBackgroundSessionCatchesAStash(t *testing.T) {
	s, fc, em := newTestService(t, Options{ShownFor: 50 * time.Millisecond})
	s.Repos("ses_1")
	eventually(t, "repos", func() bool { return s.Repos("ses_1").Computed })
	s.ChangeSet("ses_1", contracts.BaselineSessionStart)
	eventually(t, "change set", func() bool { return s.ChangeSet("ses_1", contracts.BaselineSessionStart).Computed })
	time.Sleep(100 * time.Millisecond)

	n := len(em.all())
	s.RepoChanged("/r/.git") // nothing moved
	eventually(t, "the repos recompute's announcement", func() bool { return len(em.all()) == n+1 })
	if got := em.all()[n]; !slices.Contains(got.Parts, contracts.GitPartChangeSet) || slices.Contains(got.Changed, contracts.GitPartChangeSet) {
		t.Errorf("announcement = %+v, want the change set done, unchanged", got)
	}
	if n := fc.runsOf("changeset:start"); n != 1 {
		t.Fatalf("change set computes = %d, want 1 (nothing moved)", n)
	}
	fc.set(func() { fc.stash["ses_1"] = "abc123" })
	s.RepoChanged("/r/.git")
	eventually(t, "the stash recompute", func() bool { return fc.runsOf("changeset:start") == 2 })
}

// A commit on the session's own branch stales its base change set.
func TestSessionBranchCommitStalesBase(t *testing.T) {
	s, fc, _ := newTestService(t, Options{ShownFor: 50 * time.Millisecond})
	fc.set(func() { fc.baseHeads["ses_1"] = "b0"; fc.heads["ses_1"] = "h0" })
	s.Repos("ses_1")
	eventually(t, "repos", func() bool { return s.Repos("ses_1").Computed })
	s.ChangeSet("ses_1", contracts.BaselineBase)
	eventually(t, "base", func() bool { return s.ChangeSet("ses_1", contracts.BaselineBase).Computed })
	time.Sleep(100 * time.Millisecond)
	fc.set(func() { fc.heads["ses_1"] = "h1" })
	s.RepoChanged("/r/.git")
	eventually(t, "the repos recompute", func() bool { return fc.runsOf(contracts.GitPartRepos) == 2 })
	time.Sleep(30 * time.Millisecond)
	if a := s.ChangeSet("ses_1", contracts.BaselineBase); !a.Updating {
		t.Errorf("base after a commit on the session branch = %+v, want stale", a)
	}
}

// Priority is decided when a slot is granted: a session read while its
// computes wait behind the warm-up goes next.
func TestReadPromotesQueuedWarmUp(t *testing.T) {
	var ids []string
	for i := range 8 {
		ids = append(ids, fmt.Sprintf("w%d", i))
	}
	s, fc, _ := newTestService(t, Options{MaxConcurrent: 2, Active: func(context.Context) ([]string, error) { return ids, nil }})
	release := fc.holdPart("changeset:start")
	s.Sweep(context.Background()) // 24 computes, low lane: one slot
	eventually(t, "the low lane busy", func() bool { return fc.runsOf("changeset:start") >= 1 })
	time.Sleep(50 * time.Millisecond)
	fc.mu.Lock()
	startedBefore := len(fc.order)
	fc.mu.Unlock()
	x := ids[len(ids)-1] // queued last
	s.Repos(x)           // a window opens it
	eventually(t, x+"'s compute", func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		for _, o := range fc.order[startedBefore:] {
			if strings.HasPrefix(o, x+" ") {
				return true
			}
		}
		return false
	})
	fc.mu.Lock()
	if first := fc.order[startedBefore]; !strings.HasPrefix(first, x+" ") {
		t.Errorf("%q started before %s's compute, want %s's first", first, x, x)
	}
	fc.mu.Unlock()
	close(release)
	fc.set(func() { fc.hold["changeset:start"] = nil })
}

// A background compute waiting longer than PromoteAfter is served like a
// shown session's, so steady shown load can't starve it.
func TestSlotsPromoteLongWaiters(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	shown := func(id string) bool { return id != "bg" }
	q := newSlots(2, shown, clock, 30*time.Second)
	ctx := context.Background()
	a, _ := q.acquire(ctx, "A")
	b, _ := q.acquire(ctx, "B")
	got := make(chan string, 2)
	wait := func(id string) {
		g, ok := q.acquire(ctx, id)
		if ok {
			got <- id
			q.release(g)
		}
	}
	go wait("bg")
	time.Sleep(20 * time.Millisecond)
	go wait("C") // shown, arrived after bg
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	now = now.Add(31 * time.Second)
	mu.Unlock()
	q.release(a)
	if first := <-got; first != "bg" {
		t.Errorf("granted %s first, want bg (waited past PromoteAfter)", first)
	}
	<-got
	q.release(b)
}
