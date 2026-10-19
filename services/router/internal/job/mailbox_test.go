package job_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// A turn digest for an on_signal link is queued on the link's mailbox, not
// delivered as a forced turn — and a later signal wake flushes the backlog
// as context in the same delivery the signal itself pays for.
func TestDigestQueuesToOnSignalMailboxAndSignalFlushesIt(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})
	monitor := mustCreateSession(t, data, contracts.Session{Name: "on-signal monitor"})
	link := mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: monitor.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceOnSignal,
	})

	server := newCaptureServer(t, http.StatusAccepted)
	backend := &countingBackend{reply: "compressed digest"}
	fm := fastmodel.NewWithBackend(backend, fastmodel.Config{})

	runner := job.New(data, fm, registryclient.New(""))
	runner.SetResolver(stubResolver{monitor.ID: server.addr()})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// The digest job compresses (fast model IS called — the mailbox stores
	// compressed text) but must not deliver anything to the monitor.
	if _, err := runner.Spawn(ctx, job.DigestKind, watched.ID,
		job.GenerateDigest(watched.ID, []string{"user: hi\nassistant: did stuff\n"})); err != nil {
		t.Fatalf("Spawn digest: %v", err)
	}
	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, watched.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})
	if got := server.count(); got != 0 {
		t.Fatalf("on-signal monitor received %d digest deliveries, want 0 (mailbox only)", got)
	}
	if got := backend.count(); got != 1 {
		t.Fatalf("fast model called %d times, want 1 (mailbox stores compressed text)", got)
	}

	// The signal wake flushes the backlog alongside the notice and clears it.
	if _, err := runner.Spawn(ctx, job.SignalKind, watched.ID,
		job.DeliverSignal(watched.ID, job.Signal{Kind: job.SignalCrash, Reason: "exit status 2"})); err != nil {
		t.Fatalf("Spawn signal: %v", err)
	}
	eventually(t, 5*time.Second, func() bool {
		return server.count() == 1
	})
	text := server.requests()[0].Text
	if !strings.Contains(text, "The session crashed: exit status 2") {
		t.Errorf("wake %q missing the signal notice", text)
	}
	if !strings.Contains(text, "Activity since your last update:") || !strings.Contains(text, "compressed digest") {
		t.Errorf("wake %q missing the flushed backlog", text)
	}

	backlog, err := data.DrainLinkDigests(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(backlog) != 0 {
		t.Fatalf("mailbox still holds %d digests after a delivered wake, want 0", len(backlog))
	}
}

// A digest never wakes an on_signal monitor, whatever it says: the verdict
// request is not even sent to the Fast Model, and a reply that happens to
// look like an "actionable" verdict is just queued. per_turn on the same
// subject still gets its forced-turn delivery.
func TestDigestNeverWakesOnSignalMonitor(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})
	onSignal := mustCreateSession(t, data, contracts.Session{Name: "on-signal monitor"})
	perTurn := mustCreateSession(t, data, contracts.Session{Name: "per-turn monitor"})
	link := mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: onSignal.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceOnSignal,
	})
	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: perTurn.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadencePerTurn,
	})

	onSignalServer := newCaptureServer(t, http.StatusAccepted)
	perTurnServer := newCaptureServer(t, http.StatusAccepted)
	backend := &countingBackend{reply: "ACTIONABLE: yes — blocked waiting on its own subagents\nThe agent is waiting."}
	fm := fastmodel.NewWithBackend(backend, fastmodel.Config{})

	runner := job.New(data, fm, registryclient.New(""))
	runner.SetResolver(stubResolver{onSignal.ID: onSignalServer.addr(), perTurn.ID: perTurnServer.addr()})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.DigestKind, watched.ID,
		job.GenerateDigest(watched.ID, []string{"user: hi\nassistant: waiting\n"})); err != nil {
		t.Fatalf("Spawn digest: %v", err)
	}
	eventually(t, 5*time.Second, func() bool { return perTurnServer.count() == 1 })
	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, watched.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	if got := onSignalServer.count(); got != 0 {
		t.Fatalf("on-signal monitor received %d deliveries from a digest, want 0", got)
	}
	backend.mu.Lock()
	prompts := append([]string(nil), backend.prompts...)
	systems := append([]string(nil), backend.systems...)
	backend.mu.Unlock()
	for i := range prompts {
		if strings.Contains(prompts[i], "ACTIONABLE") || strings.Contains(systems[i], "ACTIONABLE") {
			t.Errorf("fast model request %d asks for an actionability verdict; Classify must be off", i)
		}
	}
	if !strings.Contains(perTurnServer.requests()[0].Text, "reply with exactly NOOP") {
		t.Errorf("per_turn delivery lost its noop instruction")
	}

	backlog, err := data.DrainLinkDigests(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(backlog) != 1 {
		t.Fatalf("mailbox holds %d digests, want 1 queued", len(backlog))
	}
}
