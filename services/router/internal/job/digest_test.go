package job_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

func enableLinkedSessions(t *testing.T, data *dataclient.Client) {
	t.Helper()
	cfg, err := data.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.FeatureFlags.LinkedSessions = true
	if err := data.UpdateConfig(context.Background(), &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
}

func mustCreateLink(t *testing.T, c *dataclient.Client, req contracts.LinkRequest) contracts.Link {
	t.Helper()
	l, err := c.CreateLink(context.Background(), req)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	return l
}

// captureServer stands in for a monitor session's child process, recording
// every /inject delivery it receives.
type captureServer struct {
	srv *httptest.Server

	mu   sync.Mutex
	reqs []contracts.InjectRequest
}

func newCaptureServer(t *testing.T, status int) *captureServer {
	t.Helper()
	c := &captureServer{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body contracts.InjectRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.reqs = append(c.reqs, body)
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *captureServer) addr() string {
	return strings.TrimPrefix(c.srv.URL, "http://")
}

func (c *captureServer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func (c *captureServer) requests() []contracts.InjectRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]contracts.InjectRequest, len(c.reqs))
	copy(out, c.reqs)
	return out
}

// stubResolver maps session IDs to fixed addresses, standing in for the
// Supervisor's live child tracking.
type stubResolver map[string]string

func (s stubResolver) SessionAddr(sessionID string) (string, bool) {
	addr, ok := s[sessionID]
	return addr, ok
}

// countingBackend is a fastmodel.Backend that counts calls, so tests can
// assert the compression step was (or wasn't) reached.
type countingBackend struct {
	reply string

	mu    sync.Mutex
	calls int
}

func (b *countingBackend) Complete(ctx context.Context, system, prompt, model string) (string, provider.Usage, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	return b.reply, provider.Usage{}, nil
}

func (b *countingBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// A watched turn's digest reaches every per_turn read link's monitor
// exactly once, with correct attribution, and skips manual-only and
// non-read links entirely — the core acceptance criterion.
func TestGenerateDigestDeliversToPerTurnReadersOnly(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})
	monitorPerTurn := mustCreateSession(t, data, contracts.Session{Name: "per-turn monitor"})
	monitorManual := mustCreateSession(t, data, contracts.Session{Name: "manual-only monitor"})
	injectOnly := mustCreateSession(t, data, contracts.Session{Name: "inject-only link"})

	linkPerTurn := mustCreateLink(t, data, contracts.LinkRequest{
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

	perTurnServer := newCaptureServer(t, http.StatusAccepted)
	manualServer := newCaptureServer(t, http.StatusAccepted)
	injectOnlyServer := newCaptureServer(t, http.StatusAccepted)

	backend := &countingBackend{reply: "compressed digest"}
	fm := fastmodel.NewWithBackend(backend, fastmodel.Config{})

	runner := job.New(data, fm, registryclient.New(""))
	runner.SetResolver(stubResolver{
		monitorPerTurn.ID: perTurnServer.addr(),
		monitorManual.ID:  manualServer.addr(),
		injectOnly.ID:     injectOnlyServer.addr(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.DigestKind, watched.ID, job.GenerateDigest(watched.ID, []string{"user: hi\nassistant: did stuff\n"})); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, watched.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	if got := perTurnServer.count(); got != 1 {
		t.Fatalf("per-turn monitor received %d deliveries, want 1", got)
	}
	if got := manualServer.count(); got != 0 {
		t.Fatalf("manual-only monitor received %d deliveries, want 0", got)
	}
	if got := injectOnlyServer.count(); got != 0 {
		t.Fatalf("inject-only link's session received %d deliveries, want 0", got)
	}
	if got := backend.count(); got != 1 {
		t.Fatalf("fast model called %d times, want 1 (one compression shared across targets)", got)
	}

	reqs := perTurnServer.requests()
	if !strings.HasPrefix(reqs[0].Text, "compressed digest") {
		t.Errorf("delivered text = %q, want it to open with the compressed digest", reqs[0].Text)
	}
	if !strings.Contains(reqs[0].Text, "reply with exactly NOOP") {
		t.Errorf("delivered text = %q, want the noop instruction framing", reqs[0].Text)
	}
	if reqs[0].Attribution.LinkID != linkPerTurn.ID {
		t.Errorf("attribution link id = %q, want %q", reqs[0].Attribution.LinkID, linkPerTurn.ID)
	}
	if reqs[0].Attribution.SourceName != watched.Name {
		t.Errorf("attribution source name = %q, want %q (the watched session's name)", reqs[0].Attribution.SourceName, watched.Name)
	}
}

// A subject with only a manual-only read link gets no digest pushed, and —
// the cheap early exit — the fast model is never even called.
func TestGenerateDigestManualOnlySkipsCompressionEntirely(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})
	monitor := mustCreateSession(t, data, contracts.Session{Name: "manual-only monitor"})
	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: monitor.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceManualOnly,
	})

	monitorServer := newCaptureServer(t, http.StatusAccepted)
	backend := &countingBackend{reply: "should never be produced"}
	fm := fastmodel.NewWithBackend(backend, fastmodel.Config{})

	runner := job.New(data, fm, registryclient.New(""))
	runner.SetResolver(stubResolver{monitor.ID: monitorServer.addr()})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.DigestKind, watched.ID, job.GenerateDigest(watched.ID, []string{"user: hi\n"})); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, watched.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	if got := monitorServer.count(); got != 0 {
		t.Fatalf("manual-only monitor received %d deliveries, want 0", got)
	}
	if got := backend.count(); got != 0 {
		t.Fatalf("fast model called %d times, want 0 (manual-only should skip compression entirely)", got)
	}
}

// One monitor's delivery failing must not affect delivery to the other, nor
// fail the job itself — best-effort fan-out (docs/LINKED-SESSIONS-DESIGN.md
// decision 4: "the watched side must be completely unaffected by its
// observers").
func TestGenerateDigestObserverFailureDoesNotAffectOthers(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	watched := mustCreateSession(t, data, contracts.Session{Name: "watched session"})
	goodMonitor := mustCreateSession(t, data, contracts.Session{Name: "good monitor"})
	badMonitor := mustCreateSession(t, data, contracts.Session{Name: "bad monitor"})

	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: goodMonitor.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadencePerTurn,
	})
	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: badMonitor.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadencePerTurn,
	})

	goodServer := newCaptureServer(t, http.StatusAccepted)
	badServer := newCaptureServer(t, http.StatusInternalServerError)

	backend := &countingBackend{reply: "compressed digest"}
	fm := fastmodel.NewWithBackend(backend, fastmodel.Config{})

	runner := job.New(data, fm, registryclient.New(""))
	runner.SetResolver(stubResolver{
		goodMonitor.ID: goodServer.addr(),
		badMonitor.ID:  badServer.addr(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := runner.Spawn(ctx, job.DigestKind, watched.ID, job.GenerateDigest(watched.ID, []string{"user: hi\n"})); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	verify := dataclient.New(base, "test-verify")
	eventually(t, 5*time.Second, func() bool {
		evs, err := verify.Events(ctx, 0, watched.ID, []string{"job.completed"})
		return err == nil && len(evs) > 0
	})

	if got := goodServer.count(); got != 1 {
		t.Fatalf("good monitor received %d deliveries, want 1 (the other observer's failure must not affect it)", got)
	}

	failedEvs, err := verify.Events(ctx, 0, watched.ID, []string{"job.failed"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(failedEvs) != 0 {
		t.Fatalf("job.failed events: %v, want none — a monitor delivery failure must not fail the job", failedEvs)
	}
}
