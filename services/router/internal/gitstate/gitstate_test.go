package gitstate

import (
	"context"
	"encoding/json"
	"errors"
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
}

func newFakeComp() *fakeComp {
	return &fakeComp{runs: map[string]int{}, hold: map[string]chan struct{}{}, ctxs: map[string]context.Context{}, counts: "1", started: make(chan string, 64)}
}

func (f *fakeComp) begin(ctx context.Context, part string) {
	f.mu.Lock()
	f.runs[part]++
	f.ctxs[part] = ctx
	h := f.hold[part]
	f.mu.Unlock()
	f.started <- part
	if h != nil {
		select {
		case <-h:
		case <-ctx.Done():
		}
	}
}

func (f *fakeComp) computers() Computers {
	return Computers{
		Repos: func(ctx context.Context, id string) (Repos, error) {
			f.begin(ctx, contracts.GitPartRepos)
			f.mu.Lock()
			defer f.mu.Unlock()
			return Repos{
				State:     []contracts.SessionRepoState{{Root: "/r", Worktree: contracts.RepoWorktree{Path: "/r", GitDir: "/r/.git", ComputedAt: time.Now()}}},
				Checkouts: append([]string(nil), f.checkout...),
			}, nil
		},
		ChangeSet: func(ctx context.Context, id, vs string) (contracts.ChangeSet, error) {
			f.begin(ctx, contracts.GitPartChangeSet+":"+vs)
			f.mu.Lock()
			defer f.mu.Unlock()
			set := contracts.ChangeSet{Baseline: vs, ComputedAt: time.Now()}
			for _, p := range f.files {
				set.Files = append(set.Files, contracts.ChangedFile{Path: p})
			}
			return set, nil
		},
		Counts: func(ctx context.Context, id string) (string, error) {
			f.begin(ctx, contracts.GitPartCounts)
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.counts, nil
		},
	}
}

func (f *fakeComp) runsOf(part string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs[part]
}

func (f *fakeComp) holdPart(part string) chan struct{} {
	ch := make(chan struct{})
	f.mu.Lock()
	f.hold[part] = ch
	f.mu.Unlock()
	return ch
}

type emitted struct {
	mu  sync.Mutex
	evs []contracts.GitStateUpdatedPayload
}

func (e *emitted) emit(_ context.Context, p contracts.GitStateUpdatedPayload) error {
	e.mu.Lock()
	e.evs = append(e.evs, p)
	e.mu.Unlock()
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
	eventually(t, "both announced", func() bool { return len(em.all()) == 2 })
	for _, p := range em.all() {
		if p.SessionID != "ses_1" || len(p.Parts) != 1 || !slices.Equal(p.Changed, p.Parts) || p.Generation == 0 {
			t.Errorf("announcement = %+v, want one part, changed (a first answer), with a generation", p)
		}
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
	<-fc.started // repos or counts
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
	if len(got) != 1 || !slices.Equal(got[0].Changed, []string{contracts.GitPartCounts}) {
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
