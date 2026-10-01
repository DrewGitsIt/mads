package runner

import (
	"context"
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
