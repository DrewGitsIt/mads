package runner

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// peerProvider's sessions have an inbound channel: the test sets the
// address (PeerAddresser) and pushes events as the harness would. Send
// records the text and leaves the turn open until the test ends it.
type peerProvider struct {
	mu   sync.Mutex
	sess *peerSession
}

func (*peerProvider) Name() string            { return "peerish" }
func (*peerProvider) Matrix() provider.Matrix { return fake.New().Matrix() }
func (*peerProvider) OneShot(context.Context, provider.OneShotRequest) (*provider.OneShotResult, error) {
	return nil, provider.ErrUnsupported
}
func (p *peerProvider) Start(context.Context, provider.StartOptions) (provider.Session, error) {
	s := &peerSession{events: make(chan provider.Event, 64)}
	p.mu.Lock()
	p.sess = s
	p.mu.Unlock()
	return s, nil
}

type peerSession struct {
	mu     sync.Mutex
	events chan provider.Event
	closed bool
	addr   string
	sent   []string
}

func (*peerSession) Ref() string                     { return "peer-1" }
func (*peerSession) Mode() provider.Mode             { return provider.ModeACP }
func (s *peerSession) Events() <-chan provider.Event { return s.events }
func (*peerSession) Interrupt(context.Context) error { return nil }
func (s *peerSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	return nil
}
func (s *peerSession) Send(_ context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, text)
	return nil
}
func (s *peerSession) PeerAddress() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}
func (s *peerSession) setAddr(a string) {
	s.mu.Lock()
	s.addr = a
	s.mu.Unlock()
}
func (s *peerSession) sentTexts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

type peerRig struct {
	r    *Runner
	sess *peerSession
	data *dataclient.Client
	base string
	id   string
}

func startPeer(t *testing.T) peerRig {
	t.Helper()
	ctx := context.Background()
	base := datatest.Start(t)
	rec := contracts.Session{Name: "follower", Provider: "peerish"}
	if err := dataclient.New(base, "test-bootstrap").CreateSession(ctx, &rec); err != nil {
		t.Fatal(err)
	}
	data := dataclient.New(base, rec.ID)
	p := &peerProvider{}
	reg := provider.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	r := New(rec.ID, data, reg)
	r.agent.floor = -1
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	return peerRig{r: r, sess: p.sess, data: data, base: base, id: rec.ID}
}

// The live session's own address is recorded once per address: the first
// event after it's known, again only when a new process reports another.
func TestPeerAddressRecordedOncePerAddress(t *testing.T) {
	rig := startPeer(t)
	addrs := func() string {
		var got []string
		for _, p := range eventsOfType[contracts.PeerAddress](t, rig.data, rig.id, contracts.SessionPeerAddress) {
			got = append(got, p.Address)
		}
		return strings.Join(got, ",")
	}
	stream, unsub := rig.r.Subscribe()
	defer unsub()
	// handled pushes one event and waits for the pump to have handled it.
	handled := func() {
		rig.sess.events <- provider.Event{Kind: provider.EventText, Text: "x"}
		<-stream
	}
	handled() // no address yet: nothing recorded
	if got := addrs(); got != "" {
		t.Fatalf("recorded %q before the harness had an address", got)
	}
	rig.sess.setAddr("addr-1")
	for range 3 {
		handled()
	}
	rig.sess.setAddr("addr-2") // a restart's new process
	handled()
	handled()
	if got := addrs(); got != "addr-1,addr-2" {
		t.Errorf("recorded addresses = %v, want addr-1 then addr-2, once each", got)
	}
}

// A message from outside mads that wakes an idle agent: the turn the
// harness runs for it reads as working, an Inject meanwhile queues behind
// it instead of racing it, the stream event names the sender's session and
// link, the turn's digest says what prompted it, and no
// link.message_injected is written for it (mads didn't deliver it).
func TestInboundWakesIdleAgent(t *testing.T) {
	ctx := context.Background()
	rig := startPeer(t)

	leader := contracts.Session{Name: "Leader", Provider: "peerish"}
	boot := dataclient.New(rig.base, "test-bootstrap")
	if err := boot.CreateSession(ctx, &leader); err != nil {
		t.Fatal(err)
	}
	if err := dataclient.New(rig.base, leader.ID).Append(ctx, &contracts.Envelope{
		Type: contracts.SessionPeerAddress, SessionID: leader.ID,
		Payload: mustJSON(contracts.PeerAddress{Address: "addr-leader"}),
	}); err != nil {
		t.Fatal(err)
	}
	link, err := boot.CreateLink(ctx, contracts.LinkRequest{GrantingSessionID: leader.ID, SubjectSessionID: rig.id, Preset: contracts.PresetMonitor})
	if err != nil {
		t.Fatal(err)
	}

	stream, unsub := rig.r.Subscribe()
	defer unsub()
	rig.sess.events <- provider.Event{Kind: provider.EventInbound, Text: "run the counts",
		Inbound: &provider.Inbound{Channel: "peer", From: "addr-leader", FromName: "repo-9c"}}

	var got provider.Event
	select {
	case got = <-stream:
	case <-time.After(5 * time.Second):
		t.Fatal("no inbound event on the stream")
	}
	var wire contracts.InboundStreamEvent
	if err := json.Unmarshal(got.Wire, &wire); err != nil {
		t.Fatalf("stream body %q: %v", got.Wire, err)
	}
	want := contracts.InboundSource{Channel: "peer", Provider: "peerish", FromName: "repo-9c", SessionID: leader.ID, SessionName: "Leader", LinkID: link.ID}
	if wire.Kind != "inbound" || wire.Text != "run the counts" || wire.Inbound == nil || *wire.Inbound != want {
		t.Errorf("stream event = %+v (source %+v), want text and %+v", wire, wire.Inbound, want)
	}
	if !rig.r.isBusy() {
		t.Fatal("runner not busy during the turn the message opened")
	}
	wantStates(t, waitAgentStates(t, rig.data, rig.id, 1), "working/Working…")

	if err := rig.r.Inject(ctx, "from the leader", contracts.Attribution{LinkID: link.ID, SourceName: "Leader"}); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if got := rig.sess.sentTexts(); len(got) != 0 {
		t.Fatalf("harness got %q while the inbound turn ran, want it queued", got)
	}
	if _, err := rig.r.SendOrQueue(ctx, "from the user"); err != nil {
		t.Fatalf("SendOrQueue: %v", err)
	}

	rig.sess.events <- provider.Event{Kind: provider.EventText, Text: "on it"}
	rig.sess.events <- provider.Event{Kind: provider.EventTurnEnd}
	waitFor(t, "the queued inject to go out", func() bool {
		s := rig.sess.sentTexts()
		return len(s) == 1 && s[0] == "from the leader"
	})

	turns := eventsOfType[map[string]any](t, rig.data, rig.id, contracts.TurnEnded)
	if len(turns) == 0 || !strings.Contains(turns[0]["delta"].(string), "user: (message from Leader) run the counts") {
		t.Errorf("turn digest = %v, want it to open with the inbound message", turns)
	}
	for _, inj := range eventsOfType[map[string]string](t, rig.data, rig.id, contracts.LinkMessageInjected) {
		if inj["text"] != "from the leader" {
			t.Errorf("link.message_injected for %q: only mads's own delivery may write one", inj["text"])
		}
	}
	// The inbound turn took no sent-turn ordinal: the inject is turn 1.
	rig.r.mu.Lock()
	sent, attr := rig.r.sentTurns, rig.r.turnAttributions[1]
	rig.r.mu.Unlock()
	if sent != 1 || attr.LinkID != link.ID {
		t.Errorf("sentTurns = %d, turn 1 attribution = %+v, want the inject as turn 1", sent, attr)
	}
}

// A held message reached the harness, not the agent: no turn, no busy.
func TestHeldInboundOpensNoTurn(t *testing.T) {
	rig := startPeer(t)
	stream, unsub := rig.r.Subscribe()
	defer unsub()
	rig.sess.events <- provider.Event{Kind: provider.EventInbound,
		Inbound: &provider.Inbound{Channel: "peer", From: "addr-x", FromName: "a-6f", State: "held", HoldReason: "mode-mismatch"}}
	var got provider.Event
	select {
	case got = <-stream:
	case <-time.After(5 * time.Second):
		t.Fatal("no inbound event")
	}
	var wire contracts.InboundStreamEvent
	if err := json.Unmarshal(got.Wire, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Inbound == nil || wire.Inbound.State != "held" || wire.Inbound.HoldReason != "mode-mismatch" || wire.Inbound.SessionID != "" {
		t.Errorf("held source = %+v, want held, unresolved", wire.Inbound)
	}
	if rig.r.isBusy() {
		t.Error("a held message opened a turn")
	}
}

// An inbound entry is not a user turn: the attribution of the user turn
// after it is still the one recorded for that turn's ordinal.
func TestRenderTranscriptInboundKeepsAttributions(t *testing.T) {
	attr := contracts.Attribution{LinkID: "lnk_1", SourceName: "Leader"}
	msgs := []provider.Message{
		{Role: "user", Text: "first"},
		{Role: "inbound", Text: "from outside", Inbound: &provider.Inbound{Channel: "peer", From: "a", FromName: "x"}},
		{Role: "user", Text: "injected"},
	}
	var asked []string
	out := renderTranscript(msgs, map[int]contracts.Attribution{2: attr}, func(in *provider.Inbound, _ time.Time) *contracts.InboundSource {
		asked = append(asked, in.From)
		return &contracts.InboundSource{Channel: in.Channel, Provider: "p", FromName: in.FromName}
	})
	if out[0].Attribution != nil || out[1].Attribution != nil {
		t.Errorf("attribution on the wrong entry: %+v", out)
	}
	if out[2].Attribution == nil || *out[2].Attribution != attr {
		t.Errorf("injected turn attribution = %+v, want %+v", out[2].Attribution, attr)
	}
	if out[1].Role != "inbound" || out[1].Inbound == nil || out[1].Inbound.FromName != "x" || len(asked) != 1 {
		t.Errorf("inbound entry = %+v, want its resolved source", out[1])
	}
}
