package fake_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// drain collects one turn's events through turn end.
func drain(t *testing.T, sess provider.Session) []provider.Event {
	t.Helper()
	var evs []provider.Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			evs = append(evs, ev)
			if ev.Kind == provider.EventTurnEnd {
				return evs
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}
}

func scenarioFake(t *testing.T, rules ...fake.Rule) *fake.Fake {
	t.Helper()
	f, err := fake.NewWithScenario(fake.Scenario{Name: "keys", Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// blocks: one keyed item and one stored row per block, the same key on both
// sides, and SendKeyed's id on the user row.
func TestBlocksAreSeparateKeyedItems(t *testing.T) {
	f := scenarioFake(t, fake.Rule{Match: "go", Blocks: [][]string{{"first", "."}, {"## Findings\n", "- a"}}})
	sess, err := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.(provider.KeyedSender).SendKeyed(context.Background(), "go", "u-1"); err != nil {
		t.Fatal(err)
	}
	live := map[string]string{}
	var order []string
	for _, ev := range drain(t, sess) {
		if ev.Kind == provider.EventText {
			if _, ok := live[ev.Key]; !ok {
				order = append(order, ev.Key)
			}
			live[ev.Key] += ev.Text
		}
	}
	if len(order) != 2 {
		t.Fatalf("live items %v, want 2 blocks", order)
	}
	msgs, _ := f.Transcript(context.Background(), sess.Ref())
	if msgs[0].Key != "user:u-1" {
		t.Errorf("user row key %q, want user:u-1", msgs[0].Key)
	}
	for i, key := range order {
		if m := msgs[i+1]; m.Key != key || m.Text != live[key] {
			t.Errorf("row %d = %q %q, want %q %q", i+1, m.Key, m.Text, key, live[key])
		}
	}
}

// user_lag_ms: the user row lands only after the lag.
func TestUserLag(t *testing.T) {
	f := scenarioFake(t, fake.Rule{Match: "go", Reply: "ok", UserLagMs: 300})
	sess, _ := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	defer func() { _ = sess.Close() }()
	_ = sess.Send(context.Background(), "go")
	if msgs, _ := f.Transcript(context.Background(), sess.Ref()); len(msgs) != 0 {
		t.Fatalf("store has %d rows before the lag, want 0", len(msgs))
	}
	drain(t, sess)
	msgs, _ := f.Transcript(context.Background(), sess.Ref())
	if len(msgs) != 2 || msgs[0].Role != "user" {
		t.Fatalf("after the turn: %+v, want user then agent", msgs)
	}
}

// hold_until_tool_done: the text before the tool and the tool call are
// stored only once the tool's result is in (goose).
func TestHoldUntilToolDone(t *testing.T) {
	f := scenarioFake(t, fake.Rule{
		Match:             "go",
		Blocks:            [][]string{{"Running it. "}, {"Done."}},
		Tools:             []fake.ToolStep{{Name: "sleep", Result: "ok", DurationMs: 400}},
		HoldUntilToolDone: true,
	})
	sess, _ := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, SettleHints: true})
	defer func() { _ = sess.Close() }()
	_ = sess.Send(context.Background(), "go")
	for ev := range sess.Events() {
		if ev.Kind == provider.EventToolUse {
			break
		}
	}
	msgs, _ := f.Transcript(context.Background(), sess.Ref())
	for _, m := range msgs {
		if m.Role != "user" {
			t.Fatalf("mid-tool the store already has %s %q", m.Role, m.Text)
		}
	}
	evs := drain(t, sess)
	msgs, _ = f.Transcript(context.Background(), sess.Ref())
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	if got := strings.Join(roles, ","); got != "user,agent,tool,tool_result,agent" {
		t.Errorf("stored roles %s", got)
	}
	settles := 0
	for _, ev := range evs {
		if ev.Kind == provider.EventSettle {
			settles++
		}
	}
	if settles == 0 {
		t.Error("no EventSettle on a session that asked for them")
	}
}

// Settle hints are opt-in: a session that didn't ask never sees one.
func TestSettleHintsAreOptIn(t *testing.T) {
	f := fake.New()
	sess, _ := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP})
	defer func() { _ = sess.Close() }()
	_ = sess.Send(context.Background(), "tool-text")
	for _, ev := range drain(t, sess) {
		if ev.Kind == provider.EventSettle {
			t.Fatal("EventSettle without StartOptions.SettleHints")
		}
	}
}

// The smoke entry point runs the same probe on its own.
func TestCheckStableKeysAlone(t *testing.T) {
	r := conformance.CheckStableKeys(context.Background(), fake.New(), script())
	if r.Err != nil || r.Declared != provider.Native {
		t.Fatalf("stable_keys = %+v", r)
	}
	t.Log(r.Note)
}
