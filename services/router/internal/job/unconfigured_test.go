package job_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// unconfiguredFastModel mirrors production's real wiring for "nothing set":
// fastmodel.NewDynamic with a resolve func that always reports no endpoint,
// exactly what services/router/main.go's resolveFastModel returns when
// neither Config nor the -fastmodel-* flags select one.
func unconfiguredFastModel() *fastmodel.Runner {
	return fastmodel.NewDynamic(func(context.Context) (fastmodel.DynamicConfig, error) {
		return fastmodel.DynamicConfig{}, nil
	})
}

// assertNoJobFailed waits for the job to reach a terminal completed state
// and asserts zero job.failed events were ever recorded for sessionID.
func assertNoJobFailed(t *testing.T, base, sessionID string) {
	t.Helper()
	verify := dataclient.New(base, "test-verify")
	ctx := context.Background()
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, sessionID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})
	evs, err := verify.Events(ctx, 0, sessionID, []string{"job.failed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("job.failed events with an unconfigured Fast Model backend: %v, want none", evs)
	}
}

// The sweep the acceptance criteria call for: with no Fast Model backend
// configured anywhere (no Config fields, no -fastmodel-* flags — the real
// production "nothing set" path via fastmodel.NewDynamic, not the
// providers/fake stand-in the per-kind tests use), every job kind that
// touches the Fast Model completes without ever emitting job.failed.
func TestAllJobKindsSkipSilentlyWithoutFastModelBackend(t *testing.T) {
	t.Run("session_summary", func(t *testing.T) {
		base := datatest.Start(t)
		data := dataclient.New(base, "router")
		sess := mustCreateSession(t, data, contracts.Session{Name: "unconfigured summary"})
		mustCreateSummaryTask(t, data, contracts.Task{
			SessionID: sess.ID, Title: "Fix the login bug", Source: "user", Status: contracts.StatusDone,
		})

		runner := job.New(data, unconfiguredFastModel(), registryclient.New(""))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := runner.Spawn(ctx, job.SessionSummaryKind, sess.ID, job.GenerateSessionSummary(sess.ID)); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		assertNoJobFailed(t, base, sess.ID)
	})

	t.Run("session_title", func(t *testing.T) {
		base := datatest.Start(t)
		data := dataclient.New(base, "router")
		sess := mustCreateSession(t, data, contracts.Session{
			Name: "the deploy script fails...", NameSource: contracts.SessionNameSourceDerived,
		})

		runner := job.New(data, unconfiguredFastModel(), registryclient.New(""))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := runner.Spawn(ctx, job.SessionTitleKind, sess.ID,
			job.GenerateSessionTitle(sess.ID, "user: the deploy script fails\nassistant: looking into it\n")); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		assertNoJobFailed(t, base, sess.ID)
	})

	t.Run("digest", func(t *testing.T) {
		base := datatest.Start(t)
		data := dataclient.New(base, "router")
		enableLinkedSessions(t, data)

		watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})
		monitor := mustCreateSession(t, data, contracts.Session{Name: "monitor session"})
		mustCreateLink(t, data, contracts.LinkRequest{
			GrantingSessionID: monitor.ID, SubjectSessionID: watched.ID,
			Grants: []contracts.Grant{contracts.GrantRead},
		})
		server := newCaptureServer(t, http.StatusAccepted)

		runner := job.New(data, unconfiguredFastModel(), registryclient.New(""))
		runner.SetResolver(stubResolver{monitor.ID: server.addr()})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := runner.Spawn(ctx, job.DigestKind, watched.ID,
			job.GenerateDigest(watched.ID, []string{"user: hi\nassistant: did stuff\n"})); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		assertNoJobFailed(t, base, watched.ID)
		if got := server.count(); got != 0 {
			t.Fatalf("monitor received %d deliveries, want 0 (no Fast Model backend to compress with)", got)
		}
	})

	t.Run("synthesis", func(t *testing.T) {
		base := datatest.Start(t)
		data := dataclient.New(base, "router")
		sess := mustCreateSession(t, data, contracts.Session{Name: "unconfigured synthesis"})

		runner := job.New(data, unconfiguredFastModel(), registryclient.New(""))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := runner.Spawn(ctx, job.SynthesisKind, sess.ID,
			job.SynthesizeSession(sess.ID, []string{"user: add a rate limiter\ntool: edit login.go\n"})); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		assertNoJobFailed(t, base, sess.ID)
		proposals, err := data.Proposals(context.Background(), sess.ID)
		if err != nil {
			t.Fatalf("Proposals: %v", err)
		}
		if len(proposals) != 0 {
			t.Fatalf("proposals = %+v, want none (no Fast Model backend to synthesize with)", proposals)
		}
	})

	t.Run("retro", func(t *testing.T) {
		base := datatest.Start(t)
		data := dataclient.New(base, "router")
		sess := mustCreateSession(t, data, contracts.Session{Name: "retro session"})
		mustCreateSummaryTask(t, data, contracts.Task{
			SessionID: sess.ID, Title: "Ship the release", Source: "user", Status: contracts.StatusDone,
		})

		runner := job.New(data, unconfiguredFastModel(), registryclient.New(""))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		retroID := job.NewRetroID()
		if _, err := runner.Spawn(ctx, job.RetroKind, "", job.GenerateRetro(retroID, job.RetroRequest{
			SessionIDs: []string{sess.ID},
		})); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		assertNoJobFailed(t, base, "")
	})

	t.Run("agent_generation", func(t *testing.T) {
		base := datatest.Start(t)
		data := dataclient.New(base, "router")

		runner := job.New(data, unconfiguredFastModel(), registryclient.New(""))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		fn := job.GenerateAgentFromTask(agentselect.Request{
			TaskID: "tsk_unconf", SessionID: "ses_unconf", Title: "Fix the flaky login test",
		}, "ses_unconf", "tsk_unconf")
		if _, err := runner.Spawn(ctx, job.AgentGenerationKind, "ses_unconf", fn); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		assertNoJobFailed(t, base, "ses_unconf")
	})
}
