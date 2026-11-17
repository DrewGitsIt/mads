package digestwatch_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/digestwatch"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// TestWatchersKeepWorkingAcrossARotation (docs/LOCAL-FRONT-DOOR-DESIGN.md
// round 2): against a Data Service behind the front door, a rotation ends
// every stream opened with the old token. The watchers must subscribe again
// — with the new token, from the last seq they saw — and keep working:
// an event before the rotation and one after are each acted on exactly once.
func TestWatchersKeepWorkingAcrossARotation(t *testing.T) {
	g := datatest.StartGuarded(t)
	data := dataclient.New(g.URL, "router", dataclient.WithTokenSource(localauth.FileSource(g.TokenFile)))
	live := mustCreateSession(t, data, contracts.Session{Name: "live"})

	server := newCaptureServer(t)
	fm := fastmodel.NewWithBackend(stubBackend{reply: "unused"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))
	jobs.SetResolver(stubResolver{live.ID: server.addr()})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go digestwatch.NewMergeWatcher(data, jobs).Run(ctx)

	appendMerge := func(title string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"branch": "mads/" + title, "task_title": title, "waited_out": true, "files": []string{title + ".txt"}})
		if err := data.Append(ctx, &contracts.Envelope{Type: contracts.MergeCompleted, SessionID: live.ID, Payload: payload}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	appendMerge("before")
	eventually(t, 5*time.Second, func() bool { return server.count() >= 1 })

	if _, err := localauth.Rotate(g.Home); err != nil {
		t.Fatal(err)
	}
	// This call carries the new token; the Data door notices the rotation
	// and ends the watcher's old stream.
	appendMerge("after")
	eventually(t, 10*time.Second, func() bool { return server.count() >= 2 })
	time.Sleep(500 * time.Millisecond)
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.reqs) != 2 {
		t.Fatalf("deliveries = %d, want 2 (one per merge; none replayed after resubscribing): %+v", len(server.reqs), server.reqs)
	}
}
