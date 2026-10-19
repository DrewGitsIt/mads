package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/procid"
	"github.com/drewkidwell/mads/providers/fake"
)

// pipeline.started records the engine's own process identity, so the
// Router's lost-run sweep can prove a run's engine dead after a reboot or
// an unwatched death (docs/PIPELINE-LEASES-DESIGN.md B1).
func TestPipelineStartedCarriesIdentity(t *testing.T) {
	own, ok := procid.Current(procid.Default)
	if !ok {
		t.Fatal("procid.Current: could not establish this process's own identity")
	}
	h := newHarness(t)
	h.seed(contracts.Task{Title: "identified run"})
	if _, err := Run(context.Background(), Config{
		Data: h.data, Provider: fake.New(), SessionID: h.sess.ID,
		Template: solitaryTemplate(t, ""), Cwd: h.cwd, Concurrency: 1,
		StageTimeout: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	started := h.events(t, "pipeline.started")
	if len(started) != 1 {
		t.Fatalf("pipeline.started: %+v", started)
	}
	var p struct {
		Identity *contracts.LeaseIdentity `json:"identity"`
	}
	if err := json.Unmarshal(started[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Identity == nil || *p.Identity != own {
		t.Fatalf("pipeline.started identity = %+v, want %+v", p.Identity, own)
	}
}
