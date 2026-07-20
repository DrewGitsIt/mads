package job_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// The golden path (explicit acceptance criterion): a session still on its
// derived first-line name gets renamed from the first exchange's delta, and
// NameSource flips to Generated so a later turn can't retitle it again.
func TestGenerateSessionTitleRenamesDerivedSession(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := scriptedFastModel(t, fake.Scenario{Name: "title-job", Rules: []fake.Rule{{
		Match: "the deploy script fails",
		Reply: `{"title":"Fix deploy script on new Ubuntu runners"}`,
	}}})

	sess := mustCreateSession(t, data, contracts.Session{
		Name: "the deploy script fails on the new ubuntu runners...", NameSource: contracts.SessionNameSourceDerived,
	})

	runner := job.New(data, fm, registryclient.New(""))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.SessionTitleKind, sess.ID, job.GenerateSessionTitle(sess.ID, "user: the deploy script fails\nassistant: looking into it\n")); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	want := "Fix deploy script on new Ubuntu runners"
	eventually(t, 5*time.Second, func() bool {
		got, err := verify.Session(ctx, sess.ID)
		return err == nil && got.Name == want
	})

	got, err := verify.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if got.NameSource != contracts.SessionNameSourceGenerated {
		t.Fatalf("NameSource = %q, want %q", got.NameSource, contracts.SessionNameSourceGenerated)
	}
}

// Generated titles are capped and forced single-line, regardless of what the
// model actually replies with.
func TestGenerateSessionTitleCapsAndSingleLines(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	longTitle := "This is a very long generated title that goes on and on and on and on, way past fifty characters."
	fm := scriptedFastModel(t, fake.Scenario{Name: "title-job-cap", Rules: []fake.Rule{{
		Match: "hi",
		Reply: `{"title":"` + longTitle + `\nSecond line should never appear"}`,
	}}})

	sess := mustCreateSession(t, data, contracts.Session{Name: "hi", NameSource: contracts.SessionNameSourceDerived})

	runner := job.New(data, fm, registryclient.New(""))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.SessionTitleKind, sess.ID, job.GenerateSessionTitle(sess.ID, "user: hi\n")); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		got, err := verify.Session(ctx, sess.ID)
		return err == nil && got.Name != "hi"
	})

	got, err := verify.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if len(got.Name) > 50 {
		t.Errorf("Name length = %d, want <= 50 (%q)", len(got.Name), got.Name)
	}
	if strings.Contains(got.Name, "\n") {
		t.Errorf("Name = %q, want single line", got.Name)
	}
	if strings.Contains(got.Name, "Second line") {
		t.Errorf("Name = %q, a second line leaked through", got.Name)
	}
}

// The core respect-the-human guarantee: a session already renamed away from
// Derived — by a human (User) or by an earlier successful run of this same
// job (Generated) — must never be touched again.
func TestGenerateSessionTitleNeverOverwritesNonDerivedName(t *testing.T) {
	for _, source := range []contracts.SessionNameSource{contracts.SessionNameSourceUser, contracts.SessionNameSourceGenerated} {
		t.Run(string(source), func(t *testing.T) {
			base := datatest.Start(t)
			data := dataclient.New(base, "router")
			fm := scriptedFastModel(t, fake.Scenario{Name: "title-job-respect", Rules: []fake.Rule{{
				Match: "hi",
				Reply: `{"title":"A model-generated title"}`,
			}}})

			sess := mustCreateSession(t, data, contracts.Session{Name: "my custom name", NameSource: source})

			runner := job.New(data, fm, registryclient.New(""))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			if _, err := runner.Spawn(ctx, job.SessionTitleKind, sess.ID, job.GenerateSessionTitle(sess.ID, "user: hi\n")); err != nil {
				t.Fatalf("Spawn: %v", err)
			}

			verify := dataclient.New(base, "test-verify")
			eventually(t, 5*time.Second, func() bool {
				evs, err := verify.Events(ctx, 0, sess.ID, []string{"job.completed"})
				return err == nil && len(evs) > 0
			})

			got, err := verify.Session(ctx, sess.ID)
			if err != nil {
				t.Fatalf("Session: %v", err)
			}
			if got.Name != "my custom name" {
				t.Fatalf("Name = %q, want unchanged %q", got.Name, "my custom name")
			}
			if got.NameSource != source {
				t.Fatalf("NameSource = %q, want unchanged %q", got.NameSource, source)
			}
		})
	}
}

// An unconfigured Fast Model backend (production's default until
// -fastmodel-base-url is set) must complete the job normally, name
// untouched, same best-effort stance as GenerateSessionSummary.
func TestGenerateSessionTitleSkipsWithoutFastModelBackend(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := scriptedFastModel(t, fake.Scenario{Name: "title-job-unconfigured"})

	sess := mustCreateSession(t, data, contracts.Session{
		Name: "the deploy script fails", NameSource: contracts.SessionNameSourceDerived,
	})

	runner := job.New(data, fm, registryclient.New(""))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.SessionTitleKind, sess.ID, job.GenerateSessionTitle(sess.ID, "user: the deploy script fails\n")); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, sess.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	evs, err := verify.Events(ctx, 0, sess.ID, []string{"job.failed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("job.failed events with an unconfigured Fast Model backend: %v, want none", evs)
	}

	got, err := verify.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if got.Name != "the deploy script fails" {
		t.Fatalf("Name = %q, want unchanged with an unconfigured Fast Model backend", got.Name)
	}
	if got.NameSource != contracts.SessionNameSourceDerived {
		t.Fatalf("NameSource = %q, want unchanged %q", got.NameSource, contracts.SessionNameSourceDerived)
	}
}

// A session deleted before the job runs must be a no-op, not a failure.
func TestGenerateSessionTitleNoopForDeletedSession(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	fm := scriptedFastModel(t, fake.Scenario{Name: "title-job-deleted"})

	runner := job.New(data, fm, registryclient.New(""))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.SessionTitleKind, "ses_never_existed", job.GenerateSessionTitle("ses_never_existed", "user: hi\n")); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, "ses_never_existed", []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	evs, err := verify.Events(ctx, 0, "ses_never_existed", []string{"job.failed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("job.failed events for a deleted session: %v, want none", evs)
	}
}
