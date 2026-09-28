package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// TestTaskTerminalSignalWakesDormantPipelineSession is the end-to-end seam
// for a pipeline whose own session has no live child (the Router restarted
// under it): a blocked-task signal over the session's implicit hand-raiser
// wakes a real Session Service child through the wake hook api.New installs
// on the job runner, and the notice, blocker included, lands in that child's
// transcript. Several signals racing for the same dormant session share one
// wake: the tracked child never changes underneath them.
func TestTaskTerminalSignalWakesDormantPipelineSession(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := data.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.FeatureFlags.LinkedSessions = true
	if err := data.UpdateConfig(ctx, &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	sup := supervisor.New(data, []string{sessionBin, "-data-url", base}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	opts := testOptions(t, data, sup)
	jobs := opts.Jobs
	jobs.SetResolver(sup)
	srv := httptest.NewServer(api.New(opts)) // installs the waker on jobs
	t.Cleanup(srv.Close)

	id := createSessionDirect(t, base, contracts.Session{Name: "dormant leader", Provider: "fake"})
	if _, err := data.CreateHandRaiser(ctx, id); err != nil {
		t.Fatalf("CreateHandRaiser: %v", err)
	}
	task := contracts.Task{SessionID: id, Title: "wire the adapter", Source: "test", Status: contracts.StatusInProgress}
	if err := data.CreateTask(ctx, &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	note := contracts.Note{Type: contracts.NoteBlocker, Content: "missing credentials"}
	if err := data.AddNote(ctx, task.ID, &note); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if _, ok := sup.SessionAddr(id); ok {
		t.Fatal("session already has a child; this test needs a dormant one")
	}

	sig := job.Signal{Kind: job.SignalTaskTerminal, TaskID: task.ID, ToStatus: string(contracts.StatusBlocked)}
	const racers = 3
	var wg sync.WaitGroup
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := jobs.Spawn(ctx, job.SignalKind, id, job.DeliverSignal(id, sig)); err != nil {
				t.Errorf("Spawn: %v", err)
			}
		}()
	}
	wg.Wait()

	deadline := time.Now().Add(15 * time.Second)
	for {
		evs, err := data.Events(ctx, 0, id, []string{"job.completed"})
		if err == nil && len(evs) >= racers {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("signal jobs did not all complete: %d/%d", len(evs), racers)
		}
		time.Sleep(50 * time.Millisecond)
	}
	pid, ok := sup.SessionPID(id)
	if !ok {
		t.Fatal("no child tracked after the signal; the dormant session was not woken")
	}

	// Every notice landed in the one tracked child: had a racer spawned a
	// second child, the deliveries made before the swap would be missing
	// from this transcript.
	var notices int
	for time.Now().Before(deadline) {
		notices = 0
		for _, m := range sessionTranscript(t, srv.URL, id) {
			if m.Role == "user" && strings.Contains(m.Text, "Blocker: missing credentials") {
				notices += strings.Count(m.Text, "Blocker: missing credentials")
			}
		}
		if notices >= racers {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if notices != racers {
		t.Fatalf("transcript carries %d blocked-task notices, want %d", notices, racers)
	}
	if again, _ := sup.SessionPID(id); again != pid {
		t.Fatalf("tracked child changed from pid %d to %d; concurrent wakes spawned twice", pid, again)
	}
}

func sessionTranscript(t *testing.T, base, id string) []contracts.TranscriptMessage {
	t.Helper()
	resp, err := http.Get(base + "/sessions/" + id + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	return decodeJSON[[]contracts.TranscriptMessage](t, resp)
}
