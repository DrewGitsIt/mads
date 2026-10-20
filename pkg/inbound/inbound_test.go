package inbound

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// memData is an in-memory Data: an event log with chosen timestamps, paged
// two at a time so the resolver's paging is exercised.
type memData struct {
	envs     []contracts.Envelope
	links    []contracts.Link
	names    map[string]string
	reads    int
	sessions int
}

func (m *memData) announce(session, addr string, at time.Time) {
	p, _ := json.Marshal(contracts.PeerAddress{Address: addr})
	m.envs = append(m.envs, contracts.Envelope{Seq: int64(len(m.envs) + 1), Timestamp: at, Type: contracts.SessionPeerAddress, SessionID: session, Payload: p})
}

func (m *memData) Events(_ context.Context, since int64, _ string, types []string) ([]contracts.Envelope, error) {
	m.reads++
	var out []contracts.Envelope
	for _, e := range m.envs {
		if e.Seq > since && string(e.Type) == types[0] && len(out) < 2 {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memData) LinksForSession(context.Context, string) ([]contracts.Link, error) {
	return m.links, nil
}

func (m *memData) Session(_ context.Context, id string) (contracts.Session, error) {
	m.sessions++
	return contracts.Session{ID: id, Name: m.names[id]}, nil
}

var t0 = time.Date(2026, 10, 19, 5, 0, 0, 0, time.UTC)

func peer(from string) *provider.Inbound {
	return &provider.Inbound{Channel: "peer", From: from, FromName: "label"}
}

// An address belongs to whichever session announced it most recently at or
// before the message: claude-code reuses a pid's socket path once the
// process is gone, so the same address can name two sessions over time.
func TestSourceResolvesByAddressAndTime(t *testing.T) {
	d := &memData{names: map[string]string{"ses_old": "Old", "ses_new": "New", "ses_other": "Other"}}
	d.announce("ses_old", "uds:/s/1.sock", t0)
	d.announce("ses_other", "uds:/s/2.sock", t0.Add(time.Minute))
	d.announce("ses_new", "uds:/s/1.sock", t0.Add(time.Hour)) // reused
	d.links = []contracts.Link{
		{ID: "lnk_down", GrantingSessionID: "ses_me", SubjectSessionID: "ses_new"},
		{ID: "lnk_unrelated", GrantingSessionID: "ses_x", SubjectSessionID: "ses_old"},
	}
	r := New(d, "ses_me", "claude-code")

	src := r.Source(context.Background(), peer("uds:/s/1.sock"), t0.Add(30*time.Minute))
	want := contracts.InboundSource{Channel: "peer", Provider: "claude-code", FromName: "label", SessionID: "ses_old", SessionName: "Old"}
	if *src != want {
		t.Errorf("before reuse = %+v, want %+v", *src, want)
	}
	src = r.Source(context.Background(), peer("uds:/s/1.sock"), t0.Add(2*time.Hour))
	if src.SessionID != "ses_new" || src.SessionName != "New" || src.LinkID != "lnk_down" {
		t.Errorf("after reuse = %+v, want ses_new over lnk_down (the recipient granting)", *src)
	}
	// Before anyone announced it: unresolved, the harness's label only.
	src = r.Source(context.Background(), peer("uds:/s/1.sock"), t0.Add(-time.Hour))
	if src.SessionID != "" || src.FromName != "label" {
		t.Errorf("before any announcement = %+v, want unresolved", *src)
	}
}

// A miss reads announcements made since the last read; an address no
// session ever announced stays unresolved, and once a read has covered a
// message's time, more misses for older messages don't read again.
func TestSourceRefreshesOnMiss(t *testing.T) {
	d := &memData{names: map[string]string{"ses_a": "A"}}
	r := New(d, "ses_me", "claude-code")
	r.now = func() time.Time { return t0.Add(10 * time.Minute) }

	if src := r.Source(context.Background(), peer("uds:a"), t0.Add(time.Minute)); src.SessionID != "" {
		t.Fatalf("resolved %+v from an empty log", *src)
	}
	reads := d.reads
	if src := r.Source(context.Background(), peer("uds:external"), t0.Add(2*time.Minute)); src.SessionID != "" || d.reads != reads {
		t.Errorf("an older miss read again (%d reads, was %d)", d.reads, reads)
	}

	d.announce("ses_a", "uds:a", t0.Add(11*time.Minute))
	d.links = []contracts.Link{{ID: "lnk_up", GrantingSessionID: "ses_a", SubjectSessionID: "ses_me"}}
	r.now = func() time.Time { return t0.Add(12 * time.Minute) }
	src := r.Source(context.Background(), peer("uds:a"), time.Time{}) // live: now
	if src.SessionID != "ses_a" || src.LinkID != "lnk_up" {
		t.Errorf("after the sender announced = %+v, want ses_a over lnk_up", *src)
	}
}

// Held messages keep the harness's state and reason; without a Data
// Service a source still carries everything the harness said.
func TestSourceWithoutData(t *testing.T) {
	r := New(nil, "ses_me", "claude-code")
	in := &provider.Inbound{Channel: "peer", From: "uds:x", FromName: "a-6f", State: "held", HoldReason: "mode-mismatch"}
	src := r.Source(context.Background(), in, t0)
	want := contracts.InboundSource{Channel: "peer", Provider: "claude-code", FromName: "a-6f", State: "held", HoldReason: "mode-mismatch"}
	if *src != want {
		t.Errorf("source = %+v, want %+v", *src, want)
	}
	if r.Source(context.Background(), nil, t0) != nil {
		t.Error("nil inbound gave a source")
	}
}
