package runner

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// turnEndsOn collects the turn ends a subscriber sees within d.
func turnEndsOn(stream <-chan provider.Event, d time.Duration) []provider.Event {
	var ends []provider.Event
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-stream:
			if !ok {
				return ends
			}
			if ev.Kind == provider.EventTurnEnd {
				ends = append(ends, ev)
			}
		case <-deadline:
			return ends
		}
	}
}

// A Stop the harness never answers (it had nothing running: the runner's
// turn was one the harness had already ended unseen, ses_359adef5b1c66ed5)
// still ends the turn: idle, an interrupted turn end, and what queued behind
// the turn goes out.
func TestStopEndsTurnTheHarnessNeverEnds(t *testing.T) {
	ctx := context.Background()
	rig := startPeer(t)
	rig.r.stopGrace = 50 * time.Millisecond
	stream, unsub := rig.r.Subscribe()
	defer unsub()

	if err := rig.r.Send(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.r.SendOrQueue(ctx, "queued"); err != nil {
		t.Fatal(err)
	}
	if err := rig.r.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	if ends := turnEndsOn(stream, time.Second); len(ends) != 1 || !ends[0].Interrupted {
		t.Fatalf("turn ends after an unanswered Stop = %+v, want one, interrupted", ends)
	}
	waitFor(t, "the queued message to go out", func() bool {
		s := rig.sess.sentTexts()
		return len(s) == 2 && s[1] == "queued"
	})
	states := waitAgentStates(t, rig.data, rig.id, 3)
	if got := states[1].State; got != contracts.AgentIdle {
		t.Errorf("agent states = %q, want idle between the stopped turn and the queued one", labelsOf(states))
	}
}

// A Stop the harness answers leaves nothing for the watchdog to do: one turn
// end, not a second forced one behind it.
func TestStopWatchdogQuietWhenTheTurnEnds(t *testing.T) {
	ctx := context.Background()
	rig := startPeer(t)
	rig.r.stopGrace = 50 * time.Millisecond
	stream, unsub := rig.r.Subscribe()
	defer unsub()

	if err := rig.r.Send(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := rig.r.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	rig.sess.events <- provider.Event{Kind: provider.EventTurnEnd, Interrupted: true}
	if ends := turnEndsOn(stream, 300*time.Millisecond); len(ends) != 1 {
		t.Fatalf("turn ends = %d, want the harness's own only", len(ends))
	}
	if rig.r.isBusy() {
		t.Error("busy after the turn ended")
	}
}

// Nor does a Stop whose turn ended and a new one started inside the grace:
// the new turn is not the one Stop was pressed on.
func TestStopWatchdogSparesTheNextTurn(t *testing.T) {
	ctx := context.Background()
	rig := startPeer(t)
	rig.r.stopGrace = 100 * time.Millisecond
	stream, unsub := rig.r.Subscribe()
	defer unsub()

	if err := rig.r.Send(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := rig.r.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	rig.sess.events <- provider.Event{Kind: provider.EventTurnEnd, Interrupted: true}
	waitFor(t, "the stopped turn to end", func() bool { return !rig.r.isBusy() })
	if err := rig.r.Send(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if ends := turnEndsOn(stream, 400*time.Millisecond); len(ends) != 1 {
		t.Fatalf("turn ends = %d, want only the stopped turn's", len(ends))
	}
	if !rig.r.isBusy() {
		t.Error("the second turn was ended by the first turn's Stop")
	}
}

// Activity the runner never started a turn for (a turn the harness ran on its
// own) reads "working" without busy; Stop ends that too.
func TestStopEndsWorkingWithoutATurn(t *testing.T) {
	ctx := context.Background()
	rig := startPeer(t)
	rig.r.stopGrace = 50 * time.Millisecond

	rig.sess.events <- provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{ID: "toolu_1", Name: "Bash"}}
	waitFor(t, "working", func() bool {
		rig.r.agent.mu.Lock()
		defer rig.r.agent.mu.Unlock()
		return rig.r.agent.state == contracts.AgentWorking
	})
	if err := rig.r.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "idle after Stop", func() bool {
		rig.r.agent.mu.Lock()
		defer rig.r.agent.mu.Unlock()
		return rig.r.agent.state == contracts.AgentIdle
	})
}
