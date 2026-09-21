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
