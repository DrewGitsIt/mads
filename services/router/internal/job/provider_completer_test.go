package job_test

import (
	"context"
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

// providerCompleterFastModel mirrors production's "provider" mode wiring
// (services/router/main.go's resolveFastModel, FastModelModeProvider tier):
// fastmodel.NewDynamic resolving to a Provider with no explicit BaseURL, so
// dynamicBackend falls through to the Provider's provider.Completer. fake
// stands in for claude-code here — both are plain provider.Provider values
// satisfying the same interface, and Fake.Complete uses the identical
// scripted-rule dialect real jobs exercise via OneShot/ACP elsewhere in this
// suite.
func providerCompleterFastModel(t *testing.T, reply string) *fastmodel.Runner {
	t.Helper()
	f, err := fake.NewWithScenario(fake.Scenario{
		Name:  "provider-completer-test",
		Rules: []fake.Rule{{Match: "", Reply: reply}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fastmodel.NewDynamic(func(context.Context) (fastmodel.DynamicConfig, error) {
		return fastmodel.DynamicConfig{Provider: f}, nil
	})
}

// TestJobKindsRunViaProviderCompleter is the acceptance criterion's "an
// unconfigured mads with claude-code default now has a WORKING Fast Model":
// with no endpoint configured anywhere (Config.FastModelBaseURL empty, no
// -fastmodel-* flags — the same "nothing set" state unconfigured_test.go
// exercises), the provider tier alone is enough for naming, summary, and
// synthesis jobs to actually produce results, not just skip silently.
func TestJobKindsRunViaProviderCompleter(t *testing.T) {
	t.Run("session_title", func(t *testing.T) {
		base := datatest.Start(t)
		data := dataclient.New(base, "router")
		sess := mustCreateSession(t, data, contracts.Session{
			Name: "the deploy script fails...", NameSource: contracts.SessionNameSourceDerived,
		})

		fm := providerCompleterFastModel(t, `{"title": "Fix the deploy script"}`)
		runner := job.New(data, fm, registryclient.New(""))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := runner.Spawn(ctx, job.SessionTitleKind, sess.ID,
			job.GenerateSessionTitle(sess.ID, "user: the deploy script fails\nassistant: looking into it\n")); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		assertNoJobFailed(t, base, sess.ID)

		got, err := data.Session(ctx, sess.ID)
		if err != nil {
			t.Fatalf("Session: %v", err)
		}
		if got.Name != "Fix the deploy script" {
			t.Errorf("session name = %q, want %q (provider Completer must have produced this title)", got.Name, "Fix the deploy script")
		}
		if got.NameSource != contracts.SessionNameSourceGenerated {
			t.Errorf("name source = %q, want %q", got.NameSource, contracts.SessionNameSourceGenerated)
		}
	})

	t.Run("session_summary", func(t *testing.T) {
		base := datatest.Start(t)
		data := dataclient.New(base, "router")
		sess := mustCreateSession(t, data, contracts.Session{Name: "provider summary session"})
		mustCreateSummaryTask(t, data, contracts.Task{
			SessionID: sess.ID, Title: "Ship the release", Source: "user", Status: contracts.StatusDone,
		})

		fm := providerCompleterFastModel(t, `{"accomplished": "Shipped the release", "decisions": "", "remaining": ""}`)
		runner := job.New(data, fm, registryclient.New(""))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := runner.Spawn(ctx, job.SessionSummaryKind, sess.ID, job.GenerateSessionSummary(sess.ID)); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		assertNoJobFailed(t, base, sess.ID)

		got, err := data.Session(ctx, sess.ID)
		if err != nil {
			t.Fatalf("Session: %v", err)
		}
		if got.Summary == "" {
			t.Error("session summary is empty, want a generated summary from the provider Completer")
		}
	})

	t.Run("synthesis", func(t *testing.T) {
		base := datatest.Start(t)
		data := dataclient.New(base, "router")
		sess := mustCreateSession(t, data, contracts.Session{Name: "provider synthesis session"})

		fm := providerCompleterFastModel(t, `{"create_tasks":[{"title":"Add a rate limiter"}]}`)
		runner := job.New(data, fm, registryclient.New(""))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := runner.Spawn(ctx, job.SynthesisKind, sess.ID,
			job.SynthesizeSession(sess.ID, []string{"user: add a rate limiter\ntool: edit login.go\n"})); err != nil {
			t.Fatalf("Spawn: %v", err)
		}
		assertNoJobFailed(t, base, sess.ID)

		proposals, err := data.Proposals(ctx, sess.ID)
		if err != nil {
			t.Fatalf("Proposals: %v", err)
		}
		if len(proposals) != 1 || proposals[0].Task == nil || proposals[0].Task.Title != "Add a rate limiter" {
			t.Fatalf("proposals = %+v, want one create_task proposal titled %q", proposals, "Add a rate limiter")
		}
	})
}
