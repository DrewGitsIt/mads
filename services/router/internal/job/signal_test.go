package job_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
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
// manual_only and inject-only links, skips the implicit hand-raiser for a
// permission signal (it only receives task terminals), composes the notice
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
	// The watched session's own implicit hand-raiser must not receive a
	// permission signal: a session can't usefully be told about its own
	// parked permission. (Task terminals do reach it — see
	// TestDeliverSignalTaskTerminalReachesImplicitHandRaiser.)
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
	if req.Attribution.Kind != "signal" || req.Attribution.Relation != "a session you monitor" {
		t.Errorf("attribution = %+v, want a signal from a session you monitor", req.Attribution)
	}
	if !req.Attribution.DigestAttributed {
		t.Error("signal delivery not digest-attributed; monitor turns could self-amplify")
	}
}

// A session's implicit hand-raiser never receives turn digests of the
// session's own activity — the self-echo burns a forced turn re-reading a
// summary of what the session itself just did. With no other reader, the
// fast model isn't even called.
func TestGenerateDigestSkipsSelfLink(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	sess := mustCreateSession(t, data, contracts.Session{Name: "pipeline leader"})
	if _, err := data.CreateHandRaiser(context.Background(), sess.ID); err != nil {
		t.Fatalf("CreateHandRaiser: %v", err)
	}

	server := newCaptureServer(t, http.StatusAccepted)
	backend := &countingBackend{reply: "should not be called"}
	fm := fastmodel.NewWithBackend(backend, fastmodel.Config{})
	runner := job.New(data, fm, registryclient.New(""))
	runner.SetResolver(stubResolver{sess.ID: server.addr()})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.DigestKind, sess.ID,
		job.GenerateDigest(sess.ID, []string{"user: hi\nassistant: did stuff\n"})); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, sess.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	if got := server.count(); got != 0 {
		t.Fatalf("self-link received %d turn-digest deliveries, want 0", got)
	}
	if got := backend.count(); got != 0 {
		t.Fatalf("fast model called %d times, want 0 (no non-self readers)", got)
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

// stubWaker stands in for the api layer's wake path: it records which
// sessions were woken and "starts" each at a fixed address.
type stubWaker struct {
	mu    sync.Mutex
	addrs map[string]string
	woken []string
}

func (w *stubWaker) WakeSession(_ context.Context, sessionID string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.woken = append(w.woken, sessionID)
	addr, ok := w.addrs[sessionID]
	if !ok {
		return "", errors.New("stub waker: unknown session")
	}
	return addr, nil
}

func (w *stubWaker) wokenIDs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.woken...)
}

// seedBlockedTask creates a task on sessionID carrying a blocker note.
func seedBlockedTask(t *testing.T, data *dataclient.Client, sessionID, title, blocker string) contracts.Task {
	t.Helper()
	task := contracts.Task{SessionID: sessionID, Title: title, Source: "test", Status: contracts.StatusInProgress}
	if err := data.CreateTask(context.Background(), &task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	note := contracts.Note{Type: contracts.NoteBlocker, Content: blocker}
	if err := data.AddNote(context.Background(), task.ID, &note); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	return task
}

func runSignal(t *testing.T, base string, runner *job.Runner, sessionID string, sig job.Signal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := runner.Spawn(ctx, job.SignalKind, sessionID, job.DeliverSignal(sessionID, sig)); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, sessionID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})
}

// A blocked task on a session holding the implicit hand-raiser over its own
// pipeline run reaches that session's live child: the notice names the task
// and carries the blocker note, attributed to "your pipeline run", and stays
// digest-attributed with the noop instruction so the forced turn can't
// self-amplify. A live child is never re-woken.
func TestDeliverSignalTaskTerminalReachesImplicitHandRaiser(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	sess := mustCreateSession(t, data, contracts.Session{Name: "pipeline leader"})
	link, err := data.CreateHandRaiser(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("CreateHandRaiser: %v", err)
	}
	task := seedBlockedTask(t, data, sess.ID, "wire the adapter", "missing credentials")

	server := newCaptureServer(t, http.StatusAccepted)
	waker := &stubWaker{}
	runner := job.New(data, fastmodel.NewWithBackend(&countingBackend{}, fastmodel.Config{}), registryclient.New(""))
	runner.SetResolver(stubResolver{sess.ID: server.addr()})
	runner.SetWaker(waker)

	runSignal(t, base, runner, sess.ID, job.Signal{
		Kind: job.SignalTaskTerminal, TaskID: task.ID, ToStatus: string(contracts.StatusBlocked),
	})

	if got := server.count(); got != 1 {
		t.Fatalf("implicit hand-raiser received %d deliveries, want 1", got)
	}
	if woken := waker.wokenIDs(); len(woken) != 0 {
		t.Errorf("woke %v, want no wake for a live child", woken)
	}
	req := server.requests()[0]
	if !strings.Contains(req.Text, `"wire the adapter"`) || !strings.Contains(req.Text, "blocked") {
		t.Errorf("notice %q missing task title or status", req.Text)
	}
	if !strings.Contains(req.Text, "Blocker: missing credentials") {
		t.Errorf("notice %q missing blocker note", req.Text)
	}
	if strings.Contains(req.Text, "NOOP") {
		t.Errorf("blocked notice %q offers NOOP; it needs the leader's decision", req.Text)
	}
	if req.Attribution.LinkID != link.ID {
		t.Errorf("attribution link = %q, want %q", req.Attribution.LinkID, link.ID)
	}
	if req.Attribution.SourceName != "your pipeline run" {
		t.Errorf("attribution source = %q, want your pipeline run", req.Attribution.SourceName)
	}
	if req.Attribution.Kind != "signal" || req.Attribution.Relation != "your pipeline run" {
		t.Errorf("attribution = %+v, want a signal from your pipeline run", req.Attribution)
	}
	if !req.Attribution.DigestAttributed {
		t.Error("implicit task-terminal delivery not digest-attributed; the forced turn could self-amplify")
	}
}

// When the run's own session has no live child (e.g. the Router restarted
// under a running pipeline), a task terminal wakes it through the waker and
// delivers to the woken child rather than being dropped.
func TestDeliverSignalTaskTerminalWakesDormantImplicitHandRaiser(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	sess := mustCreateSession(t, data, contracts.Session{Name: "dormant leader"})
	if _, err := data.CreateHandRaiser(context.Background(), sess.ID); err != nil {
		t.Fatalf("CreateHandRaiser: %v", err)
	}
	task := seedBlockedTask(t, data, sess.ID, "migrate the store", "schema conflict")

	server := newCaptureServer(t, http.StatusAccepted)
	waker := &stubWaker{addrs: map[string]string{sess.ID: server.addr()}}
	runner := job.New(data, fastmodel.NewWithBackend(&countingBackend{}, fastmodel.Config{}), registryclient.New(""))
	runner.SetResolver(stubResolver{}) // nothing is running
	runner.SetWaker(waker)

	runSignal(t, base, runner, sess.ID, job.Signal{
		Kind: job.SignalTaskTerminal, TaskID: task.ID, ToStatus: string(contracts.StatusBlocked),
	})

	if woken := waker.wokenIDs(); len(woken) != 1 || woken[0] != sess.ID {
		t.Fatalf("woken = %v, want exactly [%s]", woken, sess.ID)
	}
	if got := server.count(); got != 1 {
		t.Fatalf("woken session received %d deliveries, want 1", got)
	}
	if text := server.requests()[0].Text; !strings.Contains(text, "Blocker: schema conflict") {
		t.Errorf("notice %q missing blocker note", text)
	}
}

// The implicit hand-raiser stays excluded from permission and crash signals
// even when its child is dormant: nothing is delivered and nothing is woken.
// An ordinary action-needing signal does wake a dormant monitor.
func TestDeliverSignalImplicitExcludedForPermissionAndCrash(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	sess := mustCreateSession(t, data, contracts.Session{Name: "pipeline leader"})
	monitor := mustCreateSession(t, data, contracts.Session{Name: "dormant monitor"})
	if _, err := data.CreateHandRaiser(context.Background(), sess.ID); err != nil {
		t.Fatalf("CreateHandRaiser: %v", err)
	}
	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: monitor.ID, SubjectSessionID: sess.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceOnSignal,
	})

	server := newCaptureServer(t, http.StatusAccepted)
	waker := &stubWaker{addrs: map[string]string{sess.ID: server.addr(), monitor.ID: server.addr()}}
	runner := job.New(data, fastmodel.NewWithBackend(&countingBackend{}, fastmodel.Config{}), registryclient.New(""))
	runner.SetResolver(stubResolver{})
	runner.SetWaker(waker)

	runSignal(t, base, runner, sess.ID, job.Signal{Kind: job.SignalPermission, Tool: "Bash"})
	runSignal(t, base, runner, sess.ID, job.Signal{Kind: job.SignalCrash, Reason: "exit 1"})

	// The ordinary monitor is woken for each action signal (delivered once
	// each); the implicit hand-raiser is never woken or delivered to.
	// (runSignal's completion wait can be satisfied by the first job's
	// event, so wait for the second delivery explicitly.)
	eventually(t, 5*time.Second, func() bool { return server.count() == 2 })
	if woken := waker.wokenIDs(); len(woken) != 2 || woken[0] != monitor.ID || woken[1] != monitor.ID {
		t.Errorf("woken = %v, want only the ordinary monitor, once per signal (implicit is excluded)", woken)
	}
	if got := server.count(); got != 2 {
		t.Errorf("received %d deliveries, want 2 (the monitor's only)", got)
	}
}

// trackingResolver is a stubResolver that also reports which sessions have
// a supervised pipeline run (job.PipelineTracker), as the Supervisor does.
type trackingResolver struct {
	stubResolver
	pipelines map[string]bool
}

func (r trackingResolver) HasPipeline(sessionID string) bool { return r.pipelines[sessionID] }

// lockedBuffer is a log sink safe to read while job goroutines write to it.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(prevFlags)
	})
	return buf
}

// A task terminal that reaches no one on a session with a pipeline run in
// flight is logged, naming the session, task and status: the silent
// blocked-task stall this guards against. Both flavors log: no reader at
// all, and a reader whose child is dormant with no way to wake it.
func TestDeliverSignalWarnsWhenTerminalGoesUnheardDuringRun(t *testing.T) {
	for _, tc := range []struct {
		name       string
		handRaiser bool
		why        string
	}{
		{name: "no reader", handRaiser: false, why: "no reader"},
		{name: "reader unreachable", handRaiser: true, why: "1 reader(s), none reachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := datatest.Start(t)
			data := dataclient.New(base, "router")
			enableLinkedSessions(t, data)

			sess := mustCreateSession(t, data, contracts.Session{Name: "stalled run"})
			if tc.handRaiser {
				if _, err := data.CreateHandRaiser(context.Background(), sess.ID); err != nil {
					t.Fatalf("CreateHandRaiser: %v", err)
				}
			}
			task := seedBlockedTask(t, data, sess.ID, "wire the adapter", "missing credentials")

			logs := captureLog(t)
			runner := job.New(data, fastmodel.NewWithBackend(&countingBackend{}, fastmodel.Config{}), registryclient.New(""))
			// No waker: the dormant hand-raiser can't be reached.
			runner.SetResolver(trackingResolver{pipelines: map[string]bool{sess.ID: true}})

			runSignal(t, base, runner, sess.ID, job.Signal{
				Kind: job.SignalTaskTerminal, TaskID: task.ID, ToStatus: string(contracts.StatusBlocked),
			})

			out := logs.String()
			for _, want := range []string{"no reader for terminal task " + task.ID, "now blocked", "session " + sess.ID, tc.why, "blocked task will go unnoticed"} {
				if !strings.Contains(out, want) {
					t.Errorf("log %q missing %q", out, want)
				}
			}
		})
	}
}

// With no pipeline run in flight, an unheard task terminal is ordinary (an
// interactive session finishing its own task with nobody watching) and
// stays quiet.
func TestDeliverSignalQuietWhenNoPipelineRunning(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	sess := mustCreateSession(t, data, contracts.Session{Name: "interactive only"})
	task := seedBlockedTask(t, data, sess.ID, "tidy up", "waiting on review")

	logs := captureLog(t)
	runner := job.New(data, fastmodel.NewWithBackend(&countingBackend{}, fastmodel.Config{}), registryclient.New(""))
	runner.SetResolver(trackingResolver{})

	runSignal(t, base, runner, sess.ID, job.Signal{
		Kind: job.SignalTaskTerminal, TaskID: task.ID, ToStatus: string(contracts.StatusBlocked),
	})

	if out := logs.String(); strings.Contains(out, "will go unnoticed") {
		t.Errorf("unexpected unheard-terminal warning with no pipeline running: %q", out)
	}
}

// Only a done task's notice offers NOOP; blocked, wont_do and needs_review
// ask the leader for a decision, so each carries its own request instead.
func TestDeliverSignalTaskTerminalNoopOnlyForDone(t *testing.T) {
	cases := []struct {
		status   contracts.TaskStatus
		wantNoop bool
		wantText string
	}{
		{contracts.StatusDone, true, ""},
		{contracts.StatusBlocked, false, "This needs you: unblock it"},
		{contracts.StatusWontDo, false, "The agent gave up on it."},
		{contracts.StatusNeedsReview, false, "waiting on your review"},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
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

			server := newCaptureServer(t, http.StatusAccepted)
			runner := job.New(data, fastmodel.NewWithBackend(&countingBackend{}, fastmodel.Config{}), registryclient.New(""))
			runner.SetResolver(stubResolver{monitor.ID: server.addr()})

			runSignal(t, base, runner, watched.ID, job.Signal{
				Kind: job.SignalTaskTerminal, TaskID: task.ID, ToStatus: string(tc.status),
			})

			if got := server.count(); got != 1 {
				t.Fatalf("monitor received %d deliveries, want 1", got)
			}
			text := server.requests()[0].Text
			if !strings.HasPrefix(text, fmt.Sprintf("Task %q is now %s.", "wire the adapter", tc.status)) {
				t.Errorf("notice %q has the wrong opening", text)
			}
			if got := strings.Contains(text, "NOOP"); got != tc.wantNoop {
				t.Errorf("notice %q offers NOOP = %v, want %v", text, got, tc.wantNoop)
			}
			if !strings.Contains(text, tc.wantText) {
				t.Errorf("notice %q missing %q", text, tc.wantText)
			}
		})
	}
}

// A pipeline-running session's implicit hand-raiser hears its own run's
// tasks. Only a blocked task stalls the run, so every other terminal offers
// NOOP there — unlike a leader's link, where only done does (2026-10-19).
func TestDeliverSignalImplicitHandRaiserNoopExceptBlocked(t *testing.T) {
	cases := []struct {
		status   contracts.TaskStatus
		wantNoop bool
	}{
		{contracts.StatusDone, true},
		{contracts.StatusBlocked, false},
		{contracts.StatusWontDo, true},
		{contracts.StatusNeedsReview, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			base := datatest.Start(t)
			data := dataclient.New(base, "router")
			enableLinkedSessions(t, data)

			sess := mustCreateSession(t, data, contracts.Session{Name: "pipeline leader"})
			if _, err := data.CreateHandRaiser(context.Background(), sess.ID); err != nil {
				t.Fatalf("CreateHandRaiser: %v", err)
			}
			task := contracts.Task{SessionID: sess.ID, Title: "wire the adapter", Source: "test", Status: contracts.StatusInProgress}
			if err := data.CreateTask(context.Background(), &task); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}

			server := newCaptureServer(t, http.StatusAccepted)
			runner := job.New(data, fastmodel.NewWithBackend(&countingBackend{}, fastmodel.Config{}), registryclient.New(""))
			runner.SetResolver(stubResolver{sess.ID: server.addr()})
			runner.SetWaker(&stubWaker{})

			runSignal(t, base, runner, sess.ID, job.Signal{
				Kind: job.SignalTaskTerminal, TaskID: task.ID, ToStatus: string(tc.status),
			})

			if got := server.count(); got != 1 {
				t.Fatalf("hand-raiser received %d deliveries, want 1", got)
			}
			text := server.requests()[0].Text
			if got := strings.Contains(text, "NOOP"); got != tc.wantNoop {
				t.Errorf("notice %q offers NOOP = %v, want %v", text, got, tc.wantNoop)
			}
		})
	}
}
