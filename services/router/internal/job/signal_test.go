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

// A signal wakes on_signal AND per_turn readers (a permission pause parks
// the subject mid-turn, so a turn digest may never report it), skips
// manual_only, inject-only, and implicit links, composes the notice
// deterministically (no Fast Model call), and attributes it as a digest
// push so the monitor's forced turn can't self-amplify.
func TestDeliverSignalWakesNonManualReaders(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})
	monitorOnSignal := mustCreateSession(t, data, contracts.Session{Name: "on-signal monitor"})
	monitorPerTurn := mustCreateSession(t, data, contracts.Session{Name: "per-turn monitor"})
	monitorManual := mustCreateSession(t, data, contracts.Session{Name: "manual-only monitor"})
	injectOnly := mustCreateSession(t, data, contracts.Session{Name: "inject-only link"})

	linkOnSignal := mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: monitorOnSignal.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceOnSignal,
	})
	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: monitorPerTurn.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadencePerTurn,
	})
	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: monitorManual.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceManualOnly,
	})
	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: injectOnly.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantInject},
	})
	// The watched session's own implicit hand-raiser must not receive
	// signals — task terminals already reach it via GenerateRunDigest.
	if _, err := data.CreateHandRaiser(context.Background(), watched.ID); err != nil {
		t.Fatalf("CreateHandRaiser: %v", err)
	}

	onSignalServer := newCaptureServer(t, http.StatusAccepted)
	perTurnServer := newCaptureServer(t, http.StatusAccepted)
	manualServer := newCaptureServer(t, http.StatusAccepted)
	injectOnlyServer := newCaptureServer(t, http.StatusAccepted)
	watchedServer := newCaptureServer(t, http.StatusAccepted)

	backend := &countingBackend{reply: "should never be called"}
	fm := fastmodel.NewWithBackend(backend, fastmodel.Config{})

	runner := job.New(data, fm, registryclient.New(""))
	runner.SetResolver(stubResolver{
		monitorOnSignal.ID: onSignalServer.addr(),
		monitorPerTurn.ID:  perTurnServer.addr(),
		monitorManual.ID:   manualServer.addr(),
		injectOnly.ID:      injectOnlyServer.addr(),
		watched.ID:         watchedServer.addr(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.SignalKind, watched.ID,
		job.DeliverSignal(watched.ID, job.Signal{Kind: job.SignalPermission, Tool: "Bash"})); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, watched.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	if got := onSignalServer.count(); got != 1 {
		t.Fatalf("on-signal monitor received %d deliveries, want 1", got)
	}
	if got := perTurnServer.count(); got != 1 {
		t.Fatalf("per-turn monitor received %d deliveries, want 1", got)
	}
	if got := manualServer.count(); got != 0 {
		t.Fatalf("manual-only monitor received %d deliveries, want 0", got)
	}
	if got := injectOnlyServer.count(); got != 0 {
		t.Fatalf("inject-only link received %d deliveries, want 0", got)
	}
	if got := watchedServer.count(); got != 0 {
		t.Fatalf("implicit hand-raiser received %d deliveries, want 0", got)
	}
	if got := backend.count(); got != 0 {
		t.Fatalf("fast model called %d times, want 0 (signal notices are deterministic)", got)
	}

	req := onSignalServer.requests()[0]
	if want := "Waiting on permission to use Bash."; !strings.HasPrefix(req.Text, want) {
		t.Errorf("notice = %q, want it to open with %q", req.Text, want)
	}
	if !strings.Contains(req.Text, "reply with exactly NOOP") {
		t.Errorf("notice = %q, want the noop instruction framing", req.Text)
	}
	if req.Attribution.LinkID != linkOnSignal.ID {
		t.Errorf("attribution link = %q, want %q", req.Attribution.LinkID, linkOnSignal.ID)
	}
	if req.Attribution.SourceName != "watched session" {
		t.Errorf("attribution source = %q, want watched session", req.Attribution.SourceName)
	}
	if !req.Attribution.DigestAttributed {
		t.Error("signal delivery not digest-attributed; monitor turns could self-amplify")
	}
}

// A task-terminal signal names the task and, when blocked, carries the most
// recent blocker note.
func TestDeliverSignalTaskTerminalCarriesBlocker(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})
	monitor := mustCreateSession(t, data, contracts.Session{Name: "on-signal monitor"})

	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: monitor.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceOnSignal,
	})

	task := contracts.Task{SessionID: watched.ID, Title: "wire the adapter", Source: "test", Status: contracts.StatusInProgress}
	if err := data.CreateTask(context.Background(), &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	note := contracts.Note{Type: contracts.NoteBlocker, Content: "missing credentials"}
	if err := data.AddNote(context.Background(), task.ID, &note); err != nil {
		t.Fatalf("AddNote: %v", err)
	}

	server := newCaptureServer(t, http.StatusAccepted)
	fm := fastmodel.NewWithBackend(&countingBackend{}, fastmodel.Config{})
	runner := job.New(data, fm, registryclient.New(""))
	runner.SetResolver(stubResolver{monitor.ID: server.addr()})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.SignalKind, watched.ID,
		job.DeliverSignal(watched.ID, job.Signal{
			Kind: job.SignalTaskTerminal, TaskID: task.ID, ToStatus: string(contracts.StatusBlocked),
		})); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, watched.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	if got := server.count(); got != 1 {
		t.Fatalf("monitor received %d deliveries, want 1", got)
	}
	text := server.requests()[0].Text
	if !strings.Contains(text, `"wire the adapter"`) || !strings.Contains(text, "blocked") {
		t.Errorf("notice %q missing task title or status", text)
	}
	if !strings.Contains(text, "missing credentials") {
		t.Errorf("notice %q missing blocker note", text)
	}
}
