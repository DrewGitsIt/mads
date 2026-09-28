package digestwatch_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/digestwatch"
	"github.com/drewkidwell/mads/services/router/internal/job"
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

type captureServer struct {
	srv *httptest.Server

	mu   sync.Mutex
	reqs []contracts.InjectRequest
}

func newCaptureServer(t *testing.T) *captureServer {
	t.Helper()
	c := &captureServer{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body contracts.InjectRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.reqs = append(c.reqs, body)
		c.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *captureServer) addr() string {
	return strings.TrimPrefix(c.srv.URL, "http://")
}

func (c *captureServer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

type stubResolver map[string]string

func (s stubResolver) SessionAddr(sessionID string) (string, bool) {
	addr, ok := s[sessionID]
	return addr, ok
}

type stubBackend struct{ reply string }

func (b stubBackend) Complete(ctx context.Context, system, prompt, model string) (string, provider.Usage, error) {
	return b.reply, provider.Usage{}, nil
}

func setDigestDebounce(t *testing.T, data *dataclient.Client, ms int) {
	t.Helper()
	cfg, err := data.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.DigestDebounceMS = ms
	if err := data.UpdateConfig(context.Background(), &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
}

func digestJobQueuedCount(t *testing.T, data *dataclient.Client, sessionID string) int {
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
		if err := json.Unmarshal(ev.Payload, &payload); err == nil && payload.Kind == job.DigestKind {
			n++
		}
	}
	return n
}

// The watcher reacts to a session.turn_ended envelope by spawning a digest
// job that reaches the reading link's monitor — proving the full seam from
// event append to delivery, independent of a real session runner emitting
// the envelope (that's services/session/internal/runner's own test job).
func TestWatcherSpawnsDigestJobForTurnEnded(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")

	cfg, err := data.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.FeatureFlags.LinkedSessions = true
	if err := data.UpdateConfig(context.Background(), &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})
	monitor := mustCreateSession(t, data, contracts.Session{Name: "monitor session"})
	if _, err := data.CreateLink(context.Background(), contracts.LinkRequest{
		GrantingSessionID: monitor.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadencePerTurn,
	}); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	monitorServer := newCaptureServer(t)
	fm := fastmodel.NewWithBackend(stubBackend{reply: "digest text"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))
	jobs.SetResolver(stubResolver{monitor.ID: monitorServer.addr()})

	watcher := digestwatch.NewWatcher(data, jobs)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go watcher.Run(ctx)

	if err := data.Append(ctx, &contracts.Envelope{
		Type:      contracts.TurnEnded,
		SessionID: watched.ID,
		Payload:   json.RawMessage(`{"delta":"user: hi\nassistant: did stuff\n"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	eventually(t, 5*time.Second, func() bool {
		return monitorServer.count() == 1
	})
}

// A burst of a subject's turn_ended envelopes landing inside the debounce
// window collapses into exactly one GenerateDigest job/delivery, mirroring
// synthwatch's own debounce guarantee.
func TestWatcherDebouncesTurnEndedBurstIntoOneDigest(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	setDigestDebounce(t, data, 150)

	cfg, err := data.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.FeatureFlags.LinkedSessions = true
	if err := data.UpdateConfig(context.Background(), &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})
	monitor := mustCreateSession(t, data, contracts.Session{Name: "monitor session"})
	if _, err := data.CreateLink(context.Background(), contracts.LinkRequest{
		GrantingSessionID: monitor.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadencePerTurn,
	}); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	monitorServer := newCaptureServer(t)
	fm := fastmodel.NewWithBackend(stubBackend{reply: "digest text"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))
	jobs.SetResolver(stubResolver{monitor.ID: monitorServer.addr()})

	watcher := digestwatch.NewWatcher(data, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go watcher.Run(ctx)

	for i := 0; i < 5; i++ {
		if err := data.Append(ctx, &contracts.Envelope{
			Type:      contracts.TurnEnded,
			SessionID: watched.ID,
			Payload:   json.RawMessage(`{"delta":"user: hi\nassistant: did stuff\n"}`),
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	eventually(t, 5*time.Second, func() bool {
		return digestJobQueuedCount(t, data, watched.ID) == 1
	})
	eventually(t, 5*time.Second, func() bool {
		return monitorServer.count() == 1
	})

	// Give the debounce window a further beat to (incorrectly) fire a second
	// job/delivery before asserting the counts stay at exactly one.
	time.Sleep(300 * time.Millisecond)
	if got := digestJobQueuedCount(t, data, watched.ID); got != 1 {
		t.Fatalf("digest job.queued count = %d, want exactly 1", got)
	}
	if got := monitorServer.count(); got != 1 {
		t.Fatalf("monitor delivery count = %d, want exactly 1", got)
	}
}

// A turn_ended envelope whose entries were solely digest-attributed
// injections (payload digest_only: true) must never spawn another digest
// job — the loop-hazard guard that keeps a monitor's own forced turn from
// re-triggering digestwatch and self-amplifying a monitor chain.
func TestWatcherSkipsDigestOnlyTurn(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	setDigestDebounce(t, data, 50)

	cfg, err := data.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.FeatureFlags.LinkedSessions = true
	if err := data.UpdateConfig(context.Background(), &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	watched := mustCreateSession(t, data, contracts.Session{Name: "monitor session, forced turn"})

	fm := fastmodel.NewWithBackend(stubBackend{reply: "should not be called"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))

	watcher := digestwatch.NewWatcher(data, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go watcher.Run(ctx)

	if err := data.Append(ctx, &contracts.Envelope{
		Type:      contracts.TurnEnded,
		SessionID: watched.ID,
		Payload:   json.RawMessage(`{"delta":"user: [digest from subject] did stuff\n","digest_only":true}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	time.Sleep(300 * time.Millisecond)
	if got := digestJobQueuedCount(t, data, watched.ID); got != 0 {
		t.Fatalf("digest job.queued count = %d, want 0 for a digest_only turn", got)
	}
}

// An unrelated event type must not spawn a digest job.
func TestWatcherIgnoresOtherEventTypes(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")

	watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})

	fm := fastmodel.NewWithBackend(stubBackend{reply: "should not be called"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))

	watcher := digestwatch.NewWatcher(data, jobs)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go watcher.Run(ctx)

	if err := data.Append(ctx, &contracts.Envelope{
		Type:      contracts.SessionActivityChanged,
		SessionID: watched.ID,
		Payload:   json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// No digest job should ever be queued for this session; give the watcher
	// a beat to (incorrectly) react before checking.
	time.Sleep(200 * time.Millisecond)
	evs, err := data.Events(ctx, 0, watched.ID, []string{"job.queued"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("job.queued events for an unrelated envelope: %v, want none", evs)
	}
}
