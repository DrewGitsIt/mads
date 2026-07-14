package runner

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
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

// A Runner with SynthesisMode: full, driving a scripted write-tool turn,
// spawns a synthesis pass whose draft task lands through the Data Service
// with draft provenance (spec §4) — the Runner-level counterpart to
// services/session/internal/synth's golden trigger/mode tests, which cover
// the engine in isolation.
func TestSynthesisEndToEnd(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	bootstrap := dataclient.New(base, "test-bootstrap")
	sess := contracts.Session{Name: "test", Provider: "fake", SynthesisMode: contracts.SynthesisFull}
	if err := bootstrap.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	data := dataclient.New(base, sess.ID)

	f, err := fake.NewWithScenario(fake.Scenario{
		Name: "writes",
		Rules: []fake.Rule{
			{Match: "write it", Reply: "done", Tools: []fake.ToolStep{
				{Name: "write_file", Path: "foo.txt", Content: "hello"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("scenario: %v", err)
	}
	reg := provider.NewRegistry()
	if err := reg.Register(f); err != nil {
		t.Fatalf("register fake: %v", err)
	}

	fastModelFake, err := fake.NewWithScenario(fake.Scenario{Name: "synth", Rules: []fake.Rule{
		{Match: "", Reply: `{"tasks":[{"title":"Add retry to the flaky uploader","confidence":0.9}]}`},
	}})
	if err != nil {
		t.Fatalf("fast model scenario: %v", err)
	}

	r := New(sess.ID, data, reg)
	r.Cwd = t.TempDir()
	r.FastModel = fastmodel.New(fastModelFake, fastmodel.Config{Model: "fake-fast"})
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })

	ch, unsubscribe := r.Subscribe()
	defer unsubscribe()

	if err := r.Send(ctx, "write it"); err != nil {
		t.Fatalf("send: %v", err)
	}
	drainText(t, ch)

	eventually(t, 5*time.Second, func() bool {
		tasks, err := data.Tasks(ctx, sess.ID, "")
		if err != nil {
			t.Fatalf("tasks: %v", err)
		}
		return len(tasks) == 1
	})

	tasks, err := data.Tasks(ctx, sess.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := tasks[0]; got.Title != "Add retry to the flaky uploader" || !got.Draft || got.Source != "synthesizer" {
		t.Errorf("draft task = %+v", got)
	}
}
