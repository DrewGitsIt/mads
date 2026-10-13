package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// waitAgentStates polls the event log until at least n session.agent_state
// events have landed — the coalescing timer's trailing flush is asynchronous
// by construction, so there is nothing to synchronize on but the log itself.
func waitAgentStates(t *testing.T, data *dataclient.Client, sessionID string, n int) []contracts.AgentState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := eventsOfType[contracts.AgentState](t, data, sessionID, contracts.SessionAgentState)
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d agent_state events, got %d: %+v", n, len(got), got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func labelsOf(states []contracts.AgentState) []string {
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = s.State + "/" + s.Label
	}
	return out
}

func wantStates(t *testing.T, got []contracts.AgentState, want ...string) {
	t.Helper()
	gotLabels := labelsOf(got)
	if len(gotLabels) != len(want) {
		t.Fatalf("agent states = %q, want %q", gotLabels, want)
	}
	for i := range want {
		if gotLabels[i] != want[i] {
			t.Fatalf("agent states = %q, want %q", gotLabels, want)
		}
	}
}

// TestAgentStateTurnLifecycle: a real turn through the pump reports working
// on send and idle at turn end, and Since spans the whole turn rather than
// being restamped.
func TestAgentStateTurnLifecycle(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()
	if err := r.Send(ctx, "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)

	// The pump appends idle inside onTurnEnd, which runs before turn_end is
	// broadcast, so drainText returning means both events have landed.
	states := waitAgentStates(t, data, sess.ID, 2)
	wantStates(t, states, "working/"+agentLabelWorking, "idle/")
	if states[0].Since.IsZero() || states[1].Since.Before(states[0].Since) {
		t.Errorf("since: working %v, idle %v", states[0].Since, states[1].Since)
	}
}

// TestAgentStateInterruptedTurn: the interrupted path ends the state too —
// an interrupt must never leave a session stuck reporting "working".
func TestAgentStateInterruptedTurn(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()
	if err := r.Send(ctx, "slow"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := r.Interrupt(ctx); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for interrupted := false; !interrupted; {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before turn_end")
			}
			if ev.Kind == provider.EventTurnEnd {
				if !ev.Interrupted {
					t.Fatalf("turn_end: interrupted = %v, want true", ev.Interrupted)
				}
				interrupted = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for the interrupted turn_end")
		}
	}

	states := waitAgentStates(t, data, sess.ID, 2)
	if last := states[len(states)-1]; last.State != contracts.AgentIdle || last.Label != "" {
		t.Errorf("state after an interrupt = %+v, want idle", last)
	}
}

// TestAgentStateLabelTransitions: the label follows the main agent's tool
// calls and thinking, built from the same enrichment ToolUse.Title carries —
// and a subagent's parent-tagged events, plus a repeated label, churn
// nothing. Coalescing is disabled here (a negative floor) so every
// transition is observable; TestAgentStateCoalesces covers the real floor.
func TestAgentStateLabelTransitions(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, nil)
	r.agent.floor = -1

	r.markAgentWorking("")
	for _, ev := range []provider.Event{
		{Kind: provider.EventToolUse, Tool: &provider.ToolUse{
			ID: "toolu_bash", Name: "Bash", Kind: provider.ToolKindExecute, Title: "make check",
		}},
		{Kind: provider.EventThinking, Text: "weighing the two fixes"},
		{Kind: provider.EventThinking, ThinkingTokens: 512}, // same label: no churn
		// A subagent's own tools are its activity, not the main agent's.
		{Kind: provider.EventToolUse, ParentToolUseID: "toolu_agent", SubagentType: "general-purpose",
			Tool: &provider.ToolUse{ID: "toolu_sub", Name: "Grep", Kind: provider.ToolKindSearch, Title: "TODO in ui/src"}},
		{Kind: provider.EventText, Text: "here is what I found"}, // text is not an activity
		{Kind: provider.EventToolUse, Tool: &provider.ToolUse{
			ID: "toolu_read", Name: "Read", Path: "/repo/main.go",
			Kind: provider.ToolKindRead, Title: "/repo/main.go",
		}},
	} {
		r.observeConversation(ev)
	}
	r.markAgentIdle()

	states := eventsOfType[contracts.AgentState](t, data, sess.ID, contracts.SessionAgentState)
	wantStates(t, states,
		"working/"+agentLabelWorking,
		"working/Running bash · make check",
		"working/"+agentLabelThinking,
		"working/Reading · /repo/main.go",
		"idle/",
	)
	// Since is the turn's start, not each label's — the rail's elapsed
	// counter spans the whole turn (WorkingIndicator's own rule).
	for i, st := range states[:4] {
		if !st.Since.Equal(states[0].Since) {
			t.Errorf("state %d restamped since: %v, want %v", i, st.Since, states[0].Since)
		}
	}
}

// TestAgentStateUnpromptedTurnEnds: a turn the provider runs on its own (the
// claude CLI waking to read a background task that finished after the user's
// turn) goes "working" from its first tool call with no Send behind it, and
// must go idle at its turn end all the same. Before the claudecode provider
// emitted that end, nothing did: the session read "Working…" indefinitely.
func TestAgentStateUnpromptedTurnEnds(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, nil)
	r.agent.floor = -1

	r.observeConversation(provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{
		ID: "toolu_late", Name: "Bash", Kind: provider.ToolKindExecute, Title: "git worktree remove",
	}})
	r.onTurnEnd() // what pump runs for the provider's EventTurnEnd

	wantStates(t, waitAgentStates(t, data, sess.ID, 2),
		"working/Running bash · git worktree remove",
		"idle/",
	)
}

// TestAgentStateCoalesces: a tool-heavy burst under the production floor
// appends once, and the label that lands is the LAST one — dropping the
// trailing flush would leave the rail naming a tool that already finished.
func TestAgentStateCoalesces(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, nil) // production floor, deliberately not overridden

	r.markAgentWorking("")
	for _, title := range []string{"go build ./…", "go vet ./…", "gofmt -l .", "go test ./…"} {
		r.observeConversation(provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{
			Name: "Bash", Kind: provider.ToolKindExecute, Title: title,
		}})
	}
	if got := eventsOfType[contracts.AgentState](t, data, sess.ID, contracts.SessionAgentState); len(got) != 1 {
		t.Fatalf("burst was not coalesced: %q", labelsOf(got))
	}

	states := waitAgentStates(t, data, sess.ID, 2)
	wantStates(t, states, "working/"+agentLabelWorking, "working/Running bash · go test ./…")
	if gap := states[1].Since; !gap.Equal(states[0].Since) {
		t.Errorf("coalesced label restamped since: %v, want %v", gap, states[0].Since)
	}
}

// TestAgentStateStreamClosed: a provider stream closing mid-turn with no
// turn_end reports idle anyway (flushTurnState), and a redundant sweep —
// turn end, then the stream closing behind it — appends nothing.
func TestAgentStateStreamClosed(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, nil)
	r.agent.floor = -1

	r.markAgentWorking("")
	r.observeConversation(provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{
		ID: "toolu_bash", Name: "Bash", Kind: provider.ToolKindExecute, Title: "sleep 600",
	}})
	r.flushTurnState()
	r.flushTurnState()

	states := eventsOfType[contracts.AgentState](t, data, sess.ID, contracts.SessionAgentState)
	wantStates(t, states,
		"working/"+agentLabelWorking,
		"working/Running bash · sleep 600",
		"idle/",
	)
}

// TestAgentStateNilDataService: the whole path is a no-op without a Data
// Service, matching every other pump-side append.
func TestAgentStateNilDataService(t *testing.T) {
	r := New("ses_nodata", nil, nil)
	r.markAgentWorking("")
	r.observeConversation(provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{
		Name: "Bash", Kind: provider.ToolKindExecute, Title: "make check",
	}})
	r.observeConversation(provider.Event{Kind: provider.EventThinking, Text: "hm"})
	r.markAgentIdle()
}

func TestAgentToolLabel(t *testing.T) {
	cases := []struct {
		name string
		tool provider.ToolUse
		want string
	}{
		{"bash", provider.ToolUse{Name: "Bash", Kind: provider.ToolKindExecute, Title: "make check"}, "Running bash · make check"},
		{"read", provider.ToolUse{Name: "Read", Kind: provider.ToolKindRead, Title: "/repo/main.go"}, "Reading · /repo/main.go"},
		{"search", provider.ToolUse{Name: "Grep", Kind: provider.ToolKindSearch, Title: "TODO in ui/src"}, "Searching · TODO in ui/src"},
		{"agent", provider.ToolUse{Name: "Agent", Kind: provider.ToolKindAgent, Title: "Audit error handling"}, "Delegating · Audit error handling"},
		{"mcp", provider.ToolUse{Name: "mcp__docs__read", Kind: provider.ToolKindMCP, Server: "docs", Title: "docs:read — topic.index"}, "Calling docs · docs:read — topic.index"},
		{"no title", provider.ToolUse{Name: "TodoWrite", Kind: provider.ToolKindPlan}, "Planning"},
		// Events persisted before enrichment landed carry neither kind nor
		// title: name the tool rather than guess at what it does.
		{"unenriched", provider.ToolUse{Name: "SomeTool"}, "Using SomeTool"},
		{"nothing known", provider.ToolUse{}, agentLabelWorking},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentToolLabel(&tc.tool); got != tc.want {
				t.Errorf("agentToolLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAgentStateMirroredOnRecord: the cold-load path. A session listed while
// a turn is in flight must report the live state from its record alone — a
// page load or an SSE reconnect mid-turn has no event replay to fall back
// on, and would otherwise render every card idle until the next transition.
// The turn-end stamp is checked here too, on the same real turn.
func TestAgentStateMirroredOnRecord(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	r := New(sess.ID, data, reg)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	if cur, err := data.Session(ctx, sess.ID); err != nil {
		t.Fatal(err)
	} else if cur.LastTurnAt != nil {
		t.Errorf("LastTurnAt = %v before any turn, want nil", cur.LastTurnAt)
	}

	// Mid-turn: read the record, not the log. Send returns once the turn is
	// in flight and markAgentWorking has appended, both synchronous.
	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()
	if err := r.Send(ctx, "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	inflight, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inflight.AgentState == nil {
		t.Fatal("session record carries no agent state mid-turn")
	}
	if inflight.AgentState.State != contracts.AgentWorking {
		t.Errorf("mid-turn record state = %+v, want working", *inflight.AgentState)
	}
	if inflight.AgentState.Label == "" || inflight.AgentState.Since.IsZero() {
		t.Errorf("mid-turn record state = %+v, want a label and a since", *inflight.AgentState)
	}

	drainText(t, ch)
	waitAgentStates(t, data, sess.ID, 2)

	// persistTurnEnd runs after onTurnEnd in the pump, so the idle event can
	// land a hair before the stamp.
	deadline := time.Now().Add(5 * time.Second)
	for {
		cur, err := data.Session(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cur.LastTurnAt != nil {
			if cur.LastTurnAt.Before(inflight.AgentState.Since) {
				t.Errorf("LastTurnAt %v predates the turn's start %v", cur.LastTurnAt, inflight.AgentState.Since)
			}
			if cur.AgentState == nil || cur.AgentState.State != contracts.AgentIdle {
				t.Errorf("record state after the turn = %+v, want idle", cur.AgentState)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for LastTurnAt to be stamped at turn end")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAgentStateCompacting: a /compact turn reads "Compacting…" from its
// first moment, a compaction that starts inside an ordinary turn (the
// near-limit trigger) relabels it, and one that starts outside any turn
// ("Compact now") leaves the session idle — nothing would ever mark it idle
// again.
func TestAgentStateCompacting(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	data := dataclient.New(base, sess.ID)
	r := New(sess.ID, data, nil)
	r.agent.floor = -1
	compactStart := provider.Event{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "start"}}

	r.observeConversation(compactStart) // no turn in flight: no state at all
	r.markAgentWorking("  /compact focus on the queue work ")
	r.markAgentIdle()
	r.markAgentWorking("fix the rail")
	r.observeConversation(compactStart)
	r.markAgentIdle()

	wantStates(t, eventsOfType[contracts.AgentState](t, data, sess.ID, contracts.SessionAgentState),
		"working/"+agentLabelCompacting,
		"idle/",
		"working/"+agentLabelWorking,
		"working/"+agentLabelCompacting,
		"idle/",
	)
	if isCompactCommand("/compactor") {
		t.Error(`isCompactCommand("/compactor") = true`)
	}
}

// flakyData proxies a Data Service, failing agent-state writes while its
// gate is closed. Everything else passes through untouched, so the Runner
// under test is otherwise fully live — the point is to drop exactly the one
// request whose loss used to be permanent.
type flakyData struct {
	mu      sync.Mutex
	failing bool
	writes  int
}

func (f *flakyData) set(failing bool) {
	f.mu.Lock()
	f.failing = failing
	f.mu.Unlock()
}

func (f *flakyData) agentStateWrites() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

// startFlakyData returns a base URL that forwards to upstream, plus the gate.
func startFlakyData(t *testing.T, upstream string) (string, *flakyData) {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	gate := &flakyData{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/agent-state") {
			gate.mu.Lock()
			gate.writes++
			failing := gate.failing
			gate.mu.Unlock()
			if failing {
				// The shape a restarting Data Service presents.
				http.Error(w, "data service unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		proxy.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, gate
}

// TestAgentStateSurvivesAFailedWrite: a turn whose "idle" write is refused
// must not leave the record asserting a turn in flight. The write is marked
// sent only once it lands, and the trailing flush retries until it does —
// otherwise one dropped request (a Data Service restart, a lock timeout) was
// permanent, because agent_state is written on transitions only and "idle" is
// the last transition a turn makes.
func TestAgentStateSurvivesAFailedWrite(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	proxyURL, gate := startFlakyData(t, base)

	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatalf("register fake: %v", err)
	}
	// The Runner writes through the proxy; the test reads the truth directly.
	r := New(sess.ID, dataclient.New(proxyURL, sess.ID), reg)
	truth := dataclient.New(base, sess.ID)
	r.agent.floor = -1 // every transition is its own write, so the gate is exact
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()
	gate.set(true)
	if err := r.Send(ctx, "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)

	// Both edges were refused, so the log has nothing — and before the fix
	// this was the terminal state: the Runner believed it had already said
	// idle and would never say it again.
	if got := eventsOfType[contracts.AgentState](t, truth, sess.ID, contracts.SessionAgentState); len(got) != 0 {
		t.Fatalf("agent states while the write was refused = %+v, want none", got)
	}
	refused := gate.agentStateWrites()
	if refused == 0 {
		t.Fatal("the gate saw no agent-state write at all; the test proves nothing")
	}

	// The Data Service comes back. The armed retry publishes the state the
	// turn actually ended in, with no further provider activity at all.
	gate.set(false)
	states := waitAgentStates(t, truth, sess.ID, 1)
	if last := states[len(states)-1]; last.State != contracts.AgentIdle {
		t.Fatalf("recovered state = %q (%+v), want idle — the turn had ended", last.State, states)
	}
}

// TestAgentStateRetryPublishesTheCurrentState: a retry must publish what the
// session is doing when the flush fires, never the value whose write failed.
// Driven directly rather than through the pump so the sequence is exact: three
// refused transitions, then the Data Service returns while the LAST of them
// (working) is current. Republishing the captured "idle" here would park a
// live turn as finished — the same lie this change exists to remove, pointing
// the other way.
func TestAgentStateRetryPublishesTheCurrentState(t *testing.T) {
	base := datatest.Start(t)
	sess := newSession(t, base)
	proxyURL, gate := startFlakyData(t, base)

	r := New(sess.ID, dataclient.New(proxyURL, sess.ID), provider.NewRegistry())
	truth := dataclient.New(base, sess.ID)
	r.agent.floor = -1                    // every transition is its own write
	r.agent.retry = 50 * time.Millisecond // don't make the test wait out the real delay

	gate.set(true)
	r.markAgentWorking("one")
	r.markAgentIdle()
	r.markAgentWorking("two")
	if got := eventsOfType[contracts.AgentState](t, truth, sess.ID, contracts.SessionAgentState); len(got) != 0 {
		t.Fatalf("agent states while refused = %+v, want none", got)
	}

	gate.set(false)
	states := waitAgentStates(t, truth, sess.ID, 1)
	if states[0].State != contracts.AgentWorking {
		t.Fatalf("first recovered state = %q (%+v), want working — the turn is live", states[0].State, labelsOf(states))
	}

	// And it converges: the turn ends, and the record says so.
	r.markAgentIdle()
	final := waitAgentStates(t, truth, sess.ID, 2)
	if last := final[len(final)-1]; last.State != contracts.AgentIdle {
		t.Fatalf("final state = %q (%+v), want idle", last.State, labelsOf(final))
	}
}

// TestAgentStateRetryStopsAtShutdown: the retry loop is unbounded by design
// (a Data Service down for a minute must not leave the record wrong for the
// session's life), so Shutdown has to be what ends it — otherwise a Runner
// whose Data Service never comes back re-arms a timer forever.
func TestAgentStateRetryStopsAtShutdown(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	sess := newSession(t, base)
	proxyURL, gate := startFlakyData(t, base)

	r := New(sess.ID, dataclient.New(proxyURL, sess.ID), provider.NewRegistry())
	r.agent.floor = -1
	r.agent.retry = 20 * time.Millisecond

	gate.set(true)
	r.markAgentWorking("one")
	r.agent.mu.Lock()
	armed := r.agent.timer != nil
	r.agent.mu.Unlock()
	if !armed {
		t.Fatal("a refused write armed no retry")
	}

	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	r.agent.mu.Lock()
	stillArmed := r.agent.timer != nil
	r.agent.mu.Unlock()
	if stillArmed {
		t.Fatal("the retry timer outlived Shutdown")
	}
	before := gate.agentStateWrites()
	time.Sleep(100 * time.Millisecond) // several retry periods
	if after := gate.agentStateWrites(); after != before {
		t.Fatalf("agent-state writes went from %d to %d after Shutdown; the retry is still running", before, after)
	}
}

// TestAgentStateCountsBackgroundProcesses: a background process outliving
// its turn keeps the idle state saying so, published on each change of the
// count and cleared when the harness process exits — so a rail can tell
// "waiting on a process" apart from idle, cold or live.
func TestAgentStateCountsBackgroundProcesses(t *testing.T) {
	r, data, sessionID := newDelegateRunner(t)
	r.agent.floor = -1
	sess := &gsSession{events: make(chan provider.Event, 8)}
	r.mu.Lock()
	r.sess = sess
	r.mu.Unlock()

	r.markAgentWorking("")
	r.markAgentIdle()
	two := []provider.BackgroundTask{{TaskID: "b1"}, {TaskID: "b2"}}
	sess.events <- provider.Event{Kind: provider.EventBackground, Background: two}
	sess.events <- provider.Event{Kind: provider.EventBackground, Background: two} // unchanged: nothing new
	sess.events <- provider.Event{Kind: provider.EventBackground, Background: two[:1]}
	close(sess.events)
	r.pump(sess)

	states := eventsOfType[contracts.AgentState](t, data, sessionID, contracts.SessionAgentState)
	var got []string
	for _, s := range states {
		got = append(got, fmt.Sprintf("%s/%d", s.State, s.Background))
	}
	want := []string{"working/0", "idle/0", "idle/2", "idle/1", "idle/0"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("agent states = %v, want %v", got, want)
	}
	// The idle edge's own clock is kept: a count change is not a new state.
	if !states[2].Since.Equal(states[1].Since) {
		t.Fatalf("since moved on a count change: %v → %v", states[1].Since, states[2].Since)
	}

	rec, err := data.Session(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.AgentState == nil || rec.AgentState.Background != 0 {
		t.Fatalf("record agent state = %+v, want background 0 after exit", rec.AgentState)
	}
}

// TestAgentStateBackgroundBeforeAnyTurn: with no state yet there is nothing
// to publish a count with, and no event appears.
func TestAgentStateBackgroundBeforeAnyTurn(t *testing.T) {
	r, data, sessionID := newDelegateRunner(t)
	r.setAgentBackground(1)
	if got := eventsOfType[contracts.AgentState](t, data, sessionID, contracts.SessionAgentState); len(got) != 0 {
		t.Fatalf("agent states = %+v, want none", got)
	}
}
