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
		Grants: []contracts.Grant{contracts.GrantRead},
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

// The run watcher reacts to a task.status_changed envelope landing on done
// by spawning a run digest job that reaches the session's own implicit
// hand-raiser link — proving the full seam from event append to delivery.
func TestRunWatcherSpawnsRunDigestJobForTaskDone(t *testing.T) {
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

	sess := mustCreateSession(t, data, contracts.Session{Name: "leader session"})
	if _, err := data.CreateHandRaiser(context.Background(), sess.ID); err != nil {
		t.Fatalf("CreateHandRaiser: %v", err)
	}
	task := contracts.Task{SessionID: sess.ID, Title: "seed task", Source: "test", Status: contracts.StatusDone}
	if err := data.CreateTask(context.Background(), &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	server := newCaptureServer(t)
	fm := fastmodel.NewWithBackend(stubBackend{reply: "run digest text"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))
	jobs.SetResolver(stubResolver{sess.ID: server.addr()})

	watcher := digestwatch.NewRunWatcher(data, jobs)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go watcher.Run(ctx)

	if err := data.Append(ctx, &contracts.Envelope{
		Type:      contracts.TaskStatusChanged,
		SessionID: sess.ID,
		TaskID:    task.ID,
		Payload:   json.RawMessage(`{"from":"in_progress","to":"done"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	eventually(t, 5*time.Second, func() bool {
		return server.count() == 1
	})
}

// A non-terminal status transition must not spawn a run digest job.
func TestRunWatcherIgnoresNonTerminalStatus(t *testing.T) {
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

	sess := mustCreateSession(t, data, contracts.Session{Name: "leader session"})
	if _, err := data.CreateHandRaiser(context.Background(), sess.ID); err != nil {
		t.Fatalf("CreateHandRaiser: %v", err)
	}

	fm := fastmodel.NewWithBackend(stubBackend{reply: "should not be called"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))

	watcher := digestwatch.NewRunWatcher(data, jobs)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go watcher.Run(ctx)

	if err := data.Append(ctx, &contracts.Envelope{
		Type:      contracts.TaskStatusChanged,
		SessionID: sess.ID,
		TaskID:    "tsk_nonexistent",
		Payload:   json.RawMessage(`{"from":"pending","to":"in_progress"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	evs, err := data.Events(ctx, 0, sess.ID, []string{"job.queued"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("job.queued events for a non-terminal transition: %v, want none", evs)
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
