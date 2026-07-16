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
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// The golden path (acceptance criterion): a scripted retro over
// providers/fake sessions yields a persisted Retro whose DPAs all have their
// five fields populated.
func TestRetroJobGoldenDPAs(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := scriptedFastModel(t, fake.Scenario{Name: "retro-job-golden", Rules: []fake.Rule{{
		Match: "Ship the release",
		Reply: `{"dpas": [
			{"problem": "The release shipped a day late.",
			 "evidence": "Task 'Ship the release' has retry_count 2.",
			 "root_cause": "The release checklist wasn't automated.",
			 "remediation_layer": "pipeline_template",
			 "suggested_action": "Add a release-checklist pipeline template."}
		]}`,
	}}})

	sess := mustCreateSession(t, data, contracts.Session{Name: "release session"})
	mustCreateSummaryTask(t, data, contracts.Task{
		SessionID: sess.ID, Title: "Ship the release", Source: "user", Status: contracts.StatusDone, RetryCount: 2,
	})

	runner := job.New(data, fm, registryclient.New(""))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	retroID := job.NewRetroID()
	if _, err := runner.Spawn(ctx, job.RetroKind, "", job.GenerateRetro(retroID, job.RetroRequest{
		SessionIDs: []string{sess.ID},
	})); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, "", []string{"retro.completed"})
		return err == nil && len(evs) > 0
	})

	retro, err := verify.Retro(ctx, retroID)
	if err != nil {
		t.Fatalf("Retro: %v", err)
	}
	if len(retro.DPAs) != 1 {
		t.Fatalf("got %d DPAs, want 1", len(retro.DPAs))
	}
	dpa := retro.DPAs[0]
	if dpa.Problem == "" || dpa.Evidence == "" || dpa.RootCause == "" || dpa.SuggestedAction == "" {
		t.Errorf("dpa missing field(s): %+v", dpa)
	}
	if !dpa.RemediationLayer.Valid() {
		t.Errorf("invalid remediation_layer: %q", dpa.RemediationLayer)
	}
	if dpa.Status != contracts.DPAAccepted {
		t.Errorf("status = %q, want accepted (default)", dpa.Status)
	}
}

// The acceptance-criterion test: cancelling between progress events leaves no
// partial state. The Fast Model backend is gated on a channel so the test can
// observe generation having reached the model call (the "generating" progress
// event) before cancelling, well before the job's one and only Data Service
// write.
func TestRetroJobCancelLeavesNoPartialState(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")

	gate := make(chan struct{})
	t.Setenv("MADS_TEST_RETRO_FM_KEY", "sk-test-123")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-gate
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"dpas\":[]}"}}]}`)
	}))
	t.Cleanup(srv.Close)
	fm := fastmodel.New(nil, fastmodel.Config{BaseURL: srv.URL, APIKeyEnv: "MADS_TEST_RETRO_FM_KEY"})

	sess := mustCreateSession(t, data, contracts.Session{Name: "cancel session"})
	mustCreateSummaryTask(t, data, contracts.Task{
		SessionID: sess.ID, Title: "Some task", Source: "user", Status: contracts.StatusDone,
	})

	runner := job.New(data, fm, registryclient.New(""))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	retroID := job.NewRetroID()
	jobID, err := runner.Spawn(ctx, job.RetroKind, "", job.GenerateRetro(retroID, job.RetroRequest{
		SessionIDs: []string{sess.ID},
	}))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, "", []string{"retro.progress"})
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		for _, ev := range evs {
			var payload struct {
				Step string `json:"step"`
			}
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatalf("unmarshal retro.progress payload: %v", err)
			}
			if payload.Step == "generating" {
				return true
			}
		}
		return false
	})

	if !runner.Cancel(jobID) {
		t.Fatal("Cancel: job not found (already finished?)")
	}
	close(gate) // unblock the handler; irrelevant since ctx is already cancelled

	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, "", []string{"retro.cancelled"})
		return err == nil && len(evs) > 0
	})
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, "", []string{"job.cancelled"})
		return err == nil && len(evs) > 0
	})

	// The literal "inspects the Data Service" proof: no Retro row exists.
	if _, err := verify.Retro(ctx, retroID); !errors.Is(err, dataclient.ErrNotFound) {
		t.Fatalf("Retro: got %v, want ErrNotFound (no partial state)", err)
	}

	evs, err := verify.Events(ctx, 0, "", []string{"retro.completed", "job.completed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("completion events emitted despite cancel: %v", evs)
	}
}
