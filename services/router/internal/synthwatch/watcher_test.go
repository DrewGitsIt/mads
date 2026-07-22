package synthwatch_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/synthwatch"
)

func eventually(t *testing.T, budget time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !fn() {
		t.Fatalf("condition not met within %s", budget)
	}
}

func mustCreateSession(t *testing.T, c *dataclient.Client, s contracts.Session) contracts.Session {
	t.Helper()
	if err := c.CreateSession(context.Background(), &s); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return s
}

type stubBackend struct{ reply string }

func (b stubBackend) Complete(ctx context.Context, system, prompt, model string) (string, provider.Usage, error) {
	return b.reply, provider.Usage{}, nil
}

// configuredFastModel resolves to a stand-in endpoint, passing the watcher's
// pre-Spawn dormancy gate exactly as a real configured Config/flag would.
func configuredFastModel(ctx context.Context) (fastmodel.DynamicConfig, error) {
	return fastmodel.DynamicConfig{BaseURL: "stub", Model: "stub-model"}, nil
}

// unconfiguredFastModel mirrors production's "nothing set" state.
func unconfiguredFastModel(ctx context.Context) (fastmodel.DynamicConfig, error) {
	return fastmodel.DynamicConfig{}, nil
}

func setDebounce(t *testing.T, data *dataclient.Client, ms int) {
	t.Helper()
	cfg, err := data.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.SynthesisDebounceMS = ms
	if err := data.UpdateConfig(context.Background(), &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
}

func appendTurnEnded(t *testing.T, data *dataclient.Client, sessionID, delta string) {
	t.Helper()
	if err := data.Append(context.Background(), &contracts.Envelope{
		Type:      contracts.TurnEnded,
		SessionID: sessionID,
		Payload:   json.RawMessage(`{"delta":` + jsonQuote(delta) + `}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func synthesisJobQueuedCount(t *testing.T, data *dataclient.Client, sessionID string) int {
	t.Helper()
	evs, err := data.Events(context.Background(), 0, sessionID, []string{"job.queued"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	n := 0
	for _, ev := range evs {
		var payload struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err == nil && payload.Kind == job.SynthesisKind {
			n++
		}
	}
	return n
}

// A burst of turn_ended envelopes for one session, all landing inside the
// debounce window, collapses into exactly one synthesis job.
func TestWatcherDebouncesTurnEndedBurstIntoOneJob(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	setDebounce(t, data, 150)

	sess := mustCreateSession(t, data, contracts.Session{Name: "burst session"})

	fm := fastmodel.NewWithBackend(stubBackend{reply: "{}"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))

	watcher := synthwatch.NewWatcher(data, jobs, configuredFastModel)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go watcher.Run(ctx)

	for i := 0; i < 5; i++ {
		appendTurnEnded(t, data, sess.ID, "tool: write foo.go\n")
		time.Sleep(20 * time.Millisecond)
	}

	eventually(t, 5*time.Second, func() bool {
		return synthesisJobQueuedCount(t, data, sess.ID) == 1
	})

	// Give the debounce window a further beat to (incorrectly) fire a second
	// job before asserting the count stays at exactly one.
	time.Sleep(300 * time.Millisecond)
	if got := synthesisJobQueuedCount(t, data, sess.ID); got != 1 {
		t.Fatalf("synthesis job.queued count = %d, want exactly 1", got)
	}
}

// Two sessions bursting concurrently debounce independently: each collapses
// into its own single job, on its own schedule.
func TestWatcherDebouncesPerSessionIndependently(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	setDebounce(t, data, 150)

	sessA := mustCreateSession(t, data, contracts.Session{Name: "session a"})
	sessB := mustCreateSession(t, data, contracts.Session{Name: "session b"})

	fm := fastmodel.NewWithBackend(stubBackend{reply: "{}"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))

	watcher := synthwatch.NewWatcher(data, jobs, configuredFastModel)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go watcher.Run(ctx)

	for i := 0; i < 3; i++ {
		appendTurnEnded(t, data, sessA.ID, "tool: write a.go\n")
		time.Sleep(20 * time.Millisecond)
	}
	// sessB starts its burst well after sessA's debounce would have already
	// fired, proving the two sessions' timers don't interfere.
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 3; i++ {
		appendTurnEnded(t, data, sessB.ID, "tool: write b.go\n")
		time.Sleep(20 * time.Millisecond)
	}

	eventually(t, 5*time.Second, func() bool {
		return synthesisJobQueuedCount(t, data, sessA.ID) == 1
	})
	eventually(t, 5*time.Second, func() bool {
		return synthesisJobQueuedCount(t, data, sessB.ID) == 1
	})
}

// With no Fast Model backend configured, the watcher stays completely
// dormant: no job is ever spawned for a substantive turn_ended burst.
func TestWatcherStaysDormantWithoutFastModelBackend(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	setDebounce(t, data, 50)

	sess := mustCreateSession(t, data, contracts.Session{Name: "unconfigured session"})

	fm := fastmodel.NewWithBackend(stubBackend{reply: "should not be called"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))

	watcher := synthwatch.NewWatcher(data, jobs, unconfiguredFastModel)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go watcher.Run(ctx)

	appendTurnEnded(t, data, sess.ID, "tool: write foo.go\n")

	time.Sleep(300 * time.Millisecond)
	if got := synthesisJobQueuedCount(t, data, sess.ID); got != 0 {
		t.Fatalf("synthesis job.queued count = %d, want 0 (no Fast Model backend configured)", got)
	}
	evs, err := data.Events(context.Background(), 0, sess.ID, nil)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == contracts.SynthesisProposalCreated {
			t.Fatalf("unexpected synthesis.proposal_created event with no Fast Model backend configured: %+v", ev)
		}
	}
}
