package digestwatch_test

import (
	"context"
	"encoding/json"
	"strings"
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

// A pipeline task merged into a live session's folder mid-turn: the agent
// is told, once, which files changed under it. A Land, a task merge that
// didn't wait the agent out, and a session with no live child get nothing.
func TestMergeWatcherTellsTheAgentWhatChanged(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	live := mustCreateSession(t, data, contracts.Session{Name: "live"})
	dormant := mustCreateSession(t, data, contracts.Session{Name: "dormant"})

	server := newCaptureServer(t)
	fm := fastmodel.NewWithBackend(stubBackend{reply: "should not be called"}, fastmodel.Config{})
	jobs := job.New(data, fm, registryclient.New(""))
	jobs.SetResolver(stubResolver{live.ID: server.addr()})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go digestwatch.NewMergeWatcher(data, jobs).Run(ctx)

	appendMerge := func(sessionID, payload string) {
		t.Helper()
		if err := data.Append(ctx, &contracts.Envelope{
			Type: contracts.MergeCompleted, SessionID: sessionID, Payload: json.RawMessage(payload),
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	appendMerge(live.ID, `{"kind":"land","merge_commit":"abc","waited_out":true,"files":["x"]}`)
	appendMerge(live.ID, `{"branch":"mads/tsk_1","task_title":"quiet","auto_committed":["a.txt"]}`)
	appendMerge(dormant.ID, `{"branch":"mads/tsk_2","task_title":"asleep","waited_out":true,"files":["b.txt"]}`)
	appendMerge(live.ID, `{"branch":"mads/tsk_3","task_title":"Add docs","waited_out":true,"files":["notes.txt","docs/a.md"]}`)

	eventually(t, 5*time.Second, func() bool { return server.count() >= 1 })
	time.Sleep(300 * time.Millisecond)
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.reqs) != 1 {
		t.Fatalf("deliveries = %d, want 1: %+v", len(server.reqs), server.reqs)
	}
	got := server.reqs[0]
	for _, want := range []string{`"Add docs"`, "- notes.txt\n", "- docs/a.md\n", "Re-read them before editing."} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("notice lacks %q:\n%s", want, got.Text)
		}
	}
	if got.Attribution.LinkID != "" || got.Attribution.SourceName == "" {
		t.Errorf("attribution = %+v, want a named system source with no link", got.Attribution)
	}
}
