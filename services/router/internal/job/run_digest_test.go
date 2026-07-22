package job_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// A task reaching done on a session holding the implicit hand-raiser over
// its own pipeline run produces exactly one digest, delivered back into that
// same session with a fixed "your pipeline run" source name rather than the
// session's own — the core L3 acceptance criterion.
func TestGenerateRunDigestDeliversToImplicitHandRaiser(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	sess := mustCreateSession(t, data, contracts.Session{Name: "leader session"})
	link, err := data.CreateHandRaiser(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("CreateHandRaiser: %v", err)
	}

	task := contracts.Task{SessionID: sess.ID, Title: "seed task", Source: "test", Status: contracts.StatusInProgress}
	if err := data.CreateTask(context.Background(), &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	task.Status = contracts.StatusDone
	if err := data.UpdateTask(context.Background(), &task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	server := newCaptureServer(t, http.StatusAccepted)
	backend := &countingBackend{reply: "compressed run digest"}
	fm := fastmodel.NewWithBackend(backend, fastmodel.Config{})

	runner := job.New(data, fm, registryclient.New(""))
	runner.SetResolver(stubResolver{sess.ID: server.addr()})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.RunDigestKind, sess.ID,
		job.GenerateRunDigest(sess.ID, task.ID, string(contracts.StatusDone))); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, sess.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	if got := server.count(); got != 1 {
		t.Fatalf("hand-raiser session received %d deliveries, want 1", got)
	}
	reqs := server.requests()
	if reqs[0].Text != "compressed run digest" {
		t.Errorf("delivered text = %q, want the compressed digest", reqs[0].Text)
	}
	if reqs[0].Attribution.LinkID != link.ID {
		t.Errorf("attribution link id = %q, want %q", reqs[0].Attribution.LinkID, link.ID)
	}
	if reqs[0].Attribution.SourceName != "your pipeline run" {
		t.Errorf("attribution source name = %q, want %q", reqs[0].Attribution.SourceName, "your pipeline run")
	}
}

// A blocker note on the task is folded into the digest prompt when the task
// lands on blocked.
func TestGenerateRunDigestBlockedIncludesBlockerNote(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	sess := mustCreateSession(t, data, contracts.Session{Name: "leader session"})
	if _, err := data.CreateHandRaiser(context.Background(), sess.ID); err != nil {
		t.Fatalf("CreateHandRaiser: %v", err)
	}

	task := contracts.Task{SessionID: sess.ID, Title: "seed task", Source: "test", Status: contracts.StatusInProgress}
	if err := data.CreateTask(context.Background(), &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	note := contracts.Note{Type: contracts.NoteBlocker, Content: "missing credentials"}
	if err := data.AddNote(context.Background(), task.ID, &note); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	task.Status = contracts.StatusBlocked
	if err := data.UpdateTask(context.Background(), &task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	server := newCaptureServer(t, http.StatusAccepted)
	backend := &countingBackend{reply: "compressed blocker digest"}
	fm := fastmodel.NewWithBackend(backend, fastmodel.Config{})

	runner := job.New(data, fm, registryclient.New(""))
	runner.SetResolver(stubResolver{sess.ID: server.addr()})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.RunDigestKind, sess.ID,
		job.GenerateRunDigest(sess.ID, task.ID, string(contracts.StatusBlocked))); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, sess.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	if got := server.count(); got != 1 {
		t.Fatalf("hand-raiser session received %d deliveries, want 1", got)
	}
	if backend.count() != 1 {
		t.Fatalf("fast model called %d times, want 1", backend.count())
	}
}

// A session with no links row at all — the common case, most sessions are
// never linked — must be treated as "no links" (LinksForSession's documented
// empty-result behavior for an unknown/linkless session), not a job.failed.
// Regression coverage for a reported "load links: not found" job.failed
// spam: LinksForSession already returns an empty slice with a nil error for
// this case (services/data/internal/store/links.go's ForSession), so this
// pins that behavior through the real job path rather than fixing anything.
func TestGenerateRunDigestNoopForLinklessSession(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	sess := mustCreateSession(t, data, contracts.Session{Name: "linkless session"})
	task := contracts.Task{SessionID: sess.ID, Title: "seed task", Source: "test", Status: contracts.StatusInProgress}
	if err := data.CreateTask(context.Background(), &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	task.Status = contracts.StatusDone
	if err := data.UpdateTask(context.Background(), &task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	backend := &countingBackend{reply: "should never be produced"}
	fm := fastmodel.NewWithBackend(backend, fastmodel.Config{})

	runner := job.New(data, fm, registryclient.New(""))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.RunDigestKind, sess.ID,
		job.GenerateRunDigest(sess.ID, task.ID, string(contracts.StatusDone))); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, sess.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	failedEvs, err := verify.Events(ctx, 0, sess.ID, []string{"job.failed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(failedEvs) != 0 {
		t.Fatalf("job.failed events for a linkless session: %v, want none", failedEvs)
	}
	if backend.count() != 0 {
		t.Fatalf("fast model called %d times, want 0 (no links at all)", backend.count())
	}
}

// A session with an ordinary (non-implicit) read link over itself — which
// can't happen via the user-facing route today, but the store guards it
// regardless — must not receive run digests: only Implicit links qualify.
func TestGenerateRunDigestSkipsNonImplicitLinks(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})
	monitor := mustCreateSession(t, data, contracts.Session{Name: "ordinary monitor"})
	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: monitor.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	})

	task := contracts.Task{SessionID: watched.ID, Title: "seed task", Source: "test", Status: contracts.StatusInProgress}
	if err := data.CreateTask(context.Background(), &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	task.Status = contracts.StatusDone
	if err := data.UpdateTask(context.Background(), &task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	server := newCaptureServer(t, http.StatusAccepted)
	backend := &countingBackend{reply: "should never be produced"}
	fm := fastmodel.NewWithBackend(backend, fastmodel.Config{})

	runner := job.New(data, fm, registryclient.New(""))
	runner.SetResolver(stubResolver{monitor.ID: server.addr()})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.RunDigestKind, watched.ID,
		job.GenerateRunDigest(watched.ID, task.ID, string(contracts.StatusDone))); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, watched.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	if got := server.count(); got != 0 {
		t.Fatalf("ordinary monitor received %d deliveries, want 0 (only Implicit links qualify)", got)
	}
	if backend.count() != 0 {
		t.Fatalf("fast model called %d times, want 0 (no implicit hand-raiser present)", backend.count())
	}
}
