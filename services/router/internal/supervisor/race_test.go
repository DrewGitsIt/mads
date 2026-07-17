package supervisor

import (
	"context"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// White-box (package supervisor, not supervisor_test): grantImplicitHandRaiser
// is unexported, and the race this proves doesn't depend on real subprocess
// timing — it depends only on the run being gone from s.pipelines by the time
// the grant's own HTTP round trips complete, which is exactly the ordering a
// pipeline exiting quickly produces (onPipelineExit's forgetPipeline runs
// synchronously, well before the detached grant goroutine's Config+Create
// calls return).
//
// TestGrantImplicitHandRaiserSelfRevokesIfRunAlreadyExited proves the fix for
// the grant/revoke race: a grant call that lands after the run it was for has
// already exited (and had its own revoke run, or not — either ordering) must
// not leave a permanent link behind. Simulating "already exited" by never
// registering a pipeline entry for sessionID reproduces the exact state
// grantImplicitHandRaiser observes in the real race, without needing to win a
// timing race against a live subprocess.
func TestGrantImplicitHandRaiserSelfRevokesIfRunAlreadyExited(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")

	ctx := context.Background()
	cfg, err := data.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.FeatureFlags.LinkedSessions = true
	if err := data.UpdateConfig(ctx, &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	sess := contracts.Session{Name: "race session"}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sup := New(data, nil, nil, nil)
	// No entry is ever added to sup.pipelines for sess.ID: this is the state
	// onPipelineExit's forgetPipeline leaves behind the instant a run exits,
	// which — for a run that exits quickly — can easily precede this grant
	// call's own Create landing.
	sup.grantImplicitHandRaiser(ctx, sess.ID)

	links, err := data.LinksForSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("LinksForSession: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("implicit link leaked after a grant raced a run that had already exited: %v, want none", links)
	}
}

// TestGrantImplicitHandRaiserKeepsLinkWhilePipelineStillLive is the inverse
// guard: the self-revoke recheck must not fire (and tear down a legitimate
// link) while the run is still tracked.
func TestGrantImplicitHandRaiserKeepsLinkWhilePipelineStillLive(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")

	ctx := context.Background()
	cfg, err := data.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.FeatureFlags.LinkedSessions = true
	if err := data.UpdateConfig(ctx, &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	sess := contracts.Session{Name: "still live session"}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sup := New(data, nil, nil, nil)
	sup.mu.Lock()
	sup.pipelines["run_still_live"] = &pipelineEntry{sessionID: sess.ID}
	sup.mu.Unlock()

	sup.grantImplicitHandRaiser(ctx, sess.ID)

	links, err := data.LinksForSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("LinksForSession: %v", err)
	}
	if len(links) != 1 || !links[0].Implicit {
		t.Fatalf("links for session with a still-live pipeline: %v, want exactly one implicit link", links)
	}
}
