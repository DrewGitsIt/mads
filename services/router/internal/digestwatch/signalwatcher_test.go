package digestwatch_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/digestwatch"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// The signal watcher reacts to a session.permission_requested envelope by
// spawning a signal job that wakes an on_signal monitor — proving the full
// seam from event append to delivery, with no Fast Model involved.
func TestSignalWatcherWakesOnSignalMonitorForPermissionRequest(t *testing.T) {
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
	monitor := mustCreateSession(t, data, contracts.Session{Name: "on-signal monitor"})
	if _, err := data.CreateLink(context.Background(), contracts.LinkRequest{
		GrantingSessionID: monitor.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceOnSignal,
	}); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	server := newCaptureServer(t)
	fm := fastmodel.NewWithBackend(stubBackend{reply: "should not be called"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))
	jobs.SetResolver(stubResolver{monitor.ID: server.addr()})

	watcher := digestwatch.NewSignalWatcher(data, jobs)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go watcher.Run(ctx)

	if err := data.Append(ctx, &contracts.Envelope{
		Type:      contracts.SessionPermissionRequested,
		SessionID: watched.ID,
		Payload:   json.RawMessage(`{"request_id":"req_1","tool":"Bash"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	eventually(t, 5*time.Second, func() bool {
		return server.count() == 1
	})
}

// A non-terminal task transition must not spawn a signal job, even for a
// session watched by an on_signal monitor.
func TestSignalWatcherIgnoresNonTerminalStatus(t *testing.T) {
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
	monitor := mustCreateSession(t, data, contracts.Session{Name: "on-signal monitor"})
	if _, err := data.CreateLink(context.Background(), contracts.LinkRequest{
		GrantingSessionID: monitor.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceOnSignal,
	}); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	fm := fastmodel.NewWithBackend(stubBackend{reply: "should not be called"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))

	watcher := digestwatch.NewSignalWatcher(data, jobs)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go watcher.Run(ctx)

	if err := data.Append(ctx, &contracts.Envelope{
		Type:      contracts.TaskStatusChanged,
		SessionID: watched.ID,
		TaskID:    "tsk_nonexistent",
		Payload:   json.RawMessage(`{"from":"pending","to":"in_progress"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	evs, err := data.Events(ctx, 0, watched.ID, []string{"job.queued"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("job.queued events for a non-terminal transition: %v, want none", evs)
	}
}
