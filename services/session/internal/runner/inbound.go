package runner

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/inbound"
	"github.com/drewkidwell/mads/providers/provider"
)

// Inbound messages (docs/LINKED-SESSIONS-DESIGN.md "Inbound messages"): a
// message that reached this session's agent from outside mads, over a
// channel the harness itself runs. The provider reports it neutrally
// (provider.EventInbound / Role "inbound"); this file resolves who sent it
// and treats it like any other turn the runner didn't start. Nothing here
// knows which harness, channel or address format is involved.

// inboundResolver is this session's sender resolver, built once the
// provider is known (Start). nil before that.
func (r *Runner) inboundResolver() *inbound.Resolver {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inbound == nil && r.providerName != "" {
		var data inbound.Data
		if r.data != nil {
			data = r.data
		}
		r.inbound = inbound.New(data, r.sessionID, r.providerName)
	}
	return r.inbound
}

// inboundSource resolves one inbound message's source; nil without a
// resolver (Start hasn't run) or without an Inbound.
func (r *Runner) inboundSource(ctx context.Context, in *provider.Inbound, at time.Time) *contracts.InboundSource {
	res := r.inboundResolver()
	if res == nil || in == nil {
		return nil
	}
	return res.Source(ctx, in, at)
}

// onInbound handles one live EventInbound before anything else sees it. A
// delivered message that lands while no turn is in flight woke the agent:
// the harness is running a turn this runner didn't start, so it is marked
// busy — Send refuses and SendOrQueue/Inject queue behind it, exactly as
// behind a sent turn, until its EventTurnEnd — and the agent working. It
// does not count as a sent turn (sentAny). It is also the turn's opening digest entry, so the digest says what prompted
// it. A held message reached only the harness and changes neither.
//
// No link.message_injected is recorded: that event means mads delivered
// the message, and mads didn't.
//
// The returned event carries its stream form (provider.Event.Wire): the
// "inbound" event with the sender resolved.
func (r *Runner) onInbound(ev provider.Event) provider.Event {
	if ev.Inbound == nil {
		return ev
	}
	src := r.inboundSource(context.Background(), ev.Inbound, time.Now())
	if ev.Inbound.State == "" {
		r.mu.Lock()
		opens := !r.busy
		if opens {
			r.busy = true
		}
		r.mu.Unlock()
		r.digest.recordInbound(inboundLabel(src), ev.Text)
		if opens {
			// Not markAgentWorking(ev.Text): someone else's words must not
			// label the turn (a peer's "/compact" isn't a compaction).
			r.markAgentWorking("")
		}
	}
	if wire, err := json.Marshal(contracts.InboundStreamEvent{Kind: contracts.StreamInbound, Text: ev.Text, Inbound: src}); err == nil {
		ev.Wire = wire
	}
	return ev
}

// inboundLabel names an inbound message's sender for the digest: the mads
// session when it resolved to one, else the harness's own label.
func inboundLabel(src *contracts.InboundSource) string {
	switch {
	case src == nil:
		return "another session"
	case src.SessionName != "":
		return src.SessionName
	case src.FromName != "":
		return src.FromName
	default:
		return "another session"
	}
}

// notePeerAddress records the live session's own inbound address
// (provider.PeerAddresser) as session.peer_address whenever it differs from
// the last one this runner recorded — so once per provider process, from
// its first event on (the harness learns the address as it starts, after
// Start has returned). Other sessions' inbound senders resolve against
// these. Best-effort; a failed write is retried on the next event.
func (r *Runner) notePeerAddress(sess provider.Session) {
	pa, ok := sess.(provider.PeerAddresser)
	if !ok {
		return
	}
	addr := pa.PeerAddress()
	if addr == "" {
		return
	}
	r.mu.Lock()
	if addr == r.peerAddr {
		r.mu.Unlock()
		return
	}
	r.peerAddr = addr
	r.mu.Unlock()
	if r.data == nil {
		return
	}
	err := r.data.Append(context.Background(), &contracts.Envelope{
		Type:      contracts.SessionPeerAddress,
		SessionID: r.sessionID,
		Payload:   mustJSON(contracts.PeerAddress{Address: addr}),
	})
	if err != nil {
		log.Printf("runner: append %s: %v", contracts.SessionPeerAddress, err)
		r.mu.Lock()
		if r.peerAddr == addr {
			r.peerAddr = ""
		}
		r.mu.Unlock()
	}
}
