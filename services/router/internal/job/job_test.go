package job_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
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

func testFastModel(t *testing.T) *fastmodel.Runner {
	t.Helper()
	t.Setenv("MADS_TEST_JOB_FM_KEY", "sk-test-123")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"}}]}`)
	}))
	t.Cleanup(srv.Close)
	return fastmodel.New(nil, fastmodel.Config{BaseURL: srv.URL, APIKeyEnv: "MADS_TEST_JOB_FM_KEY"})
}

func TestNoopSampleJobRunsSpawnWorkExit(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := testFastModel(t)

	runner := job.New(data, fm)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	id, err := runner.Spawn(ctx, job.NoopKind, "ses_test", job.Noop)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if id == "" {
		t.Fatal("Spawn: no job id returned")
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, "ses_test", []string{"job.completed"})
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		return len(evs) > 0
	})

	evs, err := verify.Events(ctx, 0, "ses_test", nil)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	var gotQueued, gotStarted, gotCompleted bool
	var lastSeq int64
	for _, ev := range evs {
		if ev.Seq <= lastSeq {
			t.Fatalf("events out of Seq order: %+v", ev)
		}
		lastSeq = ev.Seq

		switch ev.Type {
		case contracts.JobQueued, contracts.JobStarted, contracts.JobCompleted:
			var payload struct {
				JobID string `json:"job_id"`
				Kind  string `json:"kind"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatalf("unmarshal payload for %s: %v", ev.Type, err)
			}
			if payload.JobID != id {
				t.Errorf("%s: job_id = %q, want %q", ev.Type, payload.JobID, id)
			}
			if payload.Kind != job.NoopKind {
				t.Errorf("%s: kind = %q, want %q", ev.Type, payload.Kind, job.NoopKind)
			}
			switch ev.Type {
			case contracts.JobQueued:
				gotQueued = true
			case contracts.JobStarted:
				gotStarted = true
			case contracts.JobCompleted:
				gotCompleted = true
			}
		}
	}

	if !gotQueued || !gotStarted || !gotCompleted {
		t.Fatalf("missing lifecycle events: queued=%v started=%v completed=%v", gotQueued, gotStarted, gotCompleted)
	}
}

func TestFailingJobEmitsJobFailed(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := testFastModel(t)

	runner := job.New(data, fm)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	id, err := runner.Spawn(ctx, "failing", "ses_fail", func(ctx context.Context, deps job.Deps) error {
		return errors.New("boom")
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, "ses_fail", []string{"job.failed"})
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		return len(evs) > 0
	})

	evs, err := verify.Events(ctx, 0, "ses_fail", nil)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	var gotFailed, gotCompleted bool
	for _, ev := range evs {
		switch ev.Type {
		case contracts.JobFailed:
			gotFailed = true
			var payload struct {
				JobID string `json:"job_id"`
				Kind  string `json:"kind"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if payload.JobID != id {
				t.Errorf("job_id = %q, want %q", payload.JobID, id)
			}
			if payload.Error != "boom" {
				t.Errorf("error = %q, want %q", payload.Error, "boom")
			}
		case contracts.JobCompleted:
			gotCompleted = true
		}
	}

	if !gotFailed {
		t.Fatal("no job.failed event observed")
	}
	if gotCompleted {
		t.Fatal("job.completed emitted for a failing job")
	}
}
