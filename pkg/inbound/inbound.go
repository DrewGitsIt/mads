// Package inbound turns a provider's neutral description of a message that
// reached an agent from outside mads (providers/provider.Inbound) into the
// contracts form the UI renders (contracts.InboundSource), resolving the
// sender to a mads session when its address is one a session announced
// (contracts.SessionPeerAddress). Shared by the session runner (live stream
// and live transcript) and the Router (a dormant session's transcript), so
// both render a message the same way.
//
// Nothing here knows what an address looks like: it is an opaque string,
// matched for equality against what sessions announced, never parsed.
package inbound

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// Data is the slice of the Data Service client the resolver reads.
// *dataclient.Client satisfies it.
type Data interface {
	Events(ctx context.Context, since int64, sessionID string, types []string) ([]contracts.Envelope, error)
	LinksForSession(ctx context.Context, sessionID string) ([]contracts.Link, error)
	Session(ctx context.Context, id string) (contracts.Session, error)
}

// clockSkew is how far after a message's own time an address announcement
// may be stamped and still count as "at or before" it. The message's time
// and the announcement's come from different clocks on the same machine
// (the harness's transcript stamp, the Data Service's append time), and a
// sender announces its address before its turn's first tool call, so the
// two can sit a hair apart in the wrong order. Address reuse (claude-code's
// are pid-based) happens over minutes at the very least, never inside this.
const clockSkew = 2 * time.Second

// detailTTL is how long a looked-up sender name and the recipient's links
// are reused: long enough that rendering a transcript with many inbound
// messages costs one lookup each, short enough that a rename or a new link
// shows on the next render.
const detailTTL = 10 * time.Second

// Resolver resolves inbound senders for one recipient session. Safe for
// concurrent use. The zero value is not usable; use New.
type Resolver struct {
	data      Data
	recipient string
	provider  string

	mu sync.Mutex
	// since is the event-log cursor sightings were read up to; refreshedAt
	// when that last happened.
	since       int64
	refreshedAt time.Time
	// sightings holds, per address, every announcement of it in time order.
	sightings map[string][]sighting
	names     map[string]cached[string]
	links     cached[[]contracts.Link]
	now       func() time.Time
}

type sighting struct {
	at        time.Time
	sessionID string
}

type cached[T any] struct {
	v  T
	at time.Time
}

// New builds a resolver for messages arriving in recipient, a session on
// providerName. data may be nil: every source then carries only what the
// harness said.
func New(data Data, recipient, providerName string) *Resolver {
	return &Resolver{
		data: data, recipient: recipient, provider: providerName,
		sightings: map[string][]sighting{}, names: map[string]cached[string]{}, now: time.Now,
	}
}

// Source is in's contracts form for a message that arrived at time at. The
// harness's own fields always carry over; SessionID, SessionName and LinkID
// are filled only when the sender resolves to a mads session. nil for a nil
// in. Lookups are best-effort: a failed read leaves the source unresolved.
func (r *Resolver) Source(ctx context.Context, in *provider.Inbound, at time.Time) *contracts.InboundSource {
	if in == nil {
		return nil
	}
	src := &contracts.InboundSource{
		Channel: in.Channel, Provider: r.provider, FromName: in.FromName,
		State: in.State, HoldReason: in.HoldReason,
	}
	if r.data == nil || in.From == "" {
		return src
	}
	if at.IsZero() {
		at = r.now()
	}
	sender := r.senderAt(ctx, in.From, at)
	if sender == "" {
		return src
	}
	src.SessionID = sender
	src.SessionName = r.name(ctx, sender)
	src.LinkID = r.linkWith(ctx, sender)
	return src
}

// senderAt is the session whose most recent announcement of addr at or
// before at (give or take clockSkew) it was, reading announcements made
// since the last read when nothing known matches — unless that read already
// happened after at, in which case a newer one can't change the answer.
func (r *Resolver) senderAt(ctx context.Context, addr string, at time.Time) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id := r.lookupLocked(addr, at); id != "" {
		return id
	}
	if !r.refreshedAt.IsZero() && r.refreshedAt.After(at.Add(clockSkew)) {
		return ""
	}
	r.refreshLocked(ctx)
	return r.lookupLocked(addr, at)
}

func (r *Resolver) lookupLocked(addr string, at time.Time) string {
	ss := r.sightings[addr]
	limit := at.Add(clockSkew)
	for i := len(ss) - 1; i >= 0; i-- {
		if !ss[i].at.After(limit) {
			return ss[i].sessionID
		}
	}
	return ""
}

// refreshLocked reads every address announcement since the cursor, a page
// at a time until the log has no more.
func (r *Resolver) refreshLocked(ctx context.Context) {
	started := r.now()
	for {
		envs, err := r.data.Events(ctx, r.since, "", []string{string(contracts.SessionPeerAddress)})
		if err != nil {
			return // nothing learned: the next miss tries again
		}
		if len(envs) == 0 {
			break
		}
		for _, env := range envs {
			if env.Seq > r.since {
				r.since = env.Seq
			}
			var p contracts.PeerAddress
			if env.SessionID == "" || json.Unmarshal(env.Payload, &p) != nil || p.Address == "" {
				continue
			}
			r.sightings[p.Address] = append(r.sightings[p.Address], sighting{at: env.Timestamp, sessionID: env.SessionID})
		}
	}
	for addr, ss := range r.sightings {
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].at.Before(ss[j].at) })
		r.sightings[addr] = ss
	}
	r.refreshedAt = started
}

// name is the sender session's current name, "" if it can't be read.
func (r *Resolver) name(ctx context.Context, id string) string {
	r.mu.Lock()
	c, ok := r.names[id]
	r.mu.Unlock()
	if ok && r.now().Sub(c.at) < detailTTL {
		return c.v
	}
	sess, err := r.data.Session(ctx, id)
	if err != nil {
		return c.v // a stale name beats none
	}
	r.mu.Lock()
	r.names[id] = cached[string]{v: sess.Name, at: r.now()}
	r.mu.Unlock()
	return sess.Name
}

// linkWith is the link between sender and the recipient, either direction;
// "" when there is none.
func (r *Resolver) linkWith(ctx context.Context, sender string) string {
	r.mu.Lock()
	c := r.links
	r.mu.Unlock()
	links := c.v
	if c.at.IsZero() || r.now().Sub(c.at) >= detailTTL {
		fresh, err := r.data.LinksForSession(ctx, r.recipient)
		if err == nil {
			links = fresh
			r.mu.Lock()
			r.links = cached[[]contracts.Link]{v: fresh, at: r.now()}
			r.mu.Unlock()
		}
	}
	for _, l := range links {
		if (l.GrantingSessionID == sender && l.SubjectSessionID == r.recipient) ||
			(l.GrantingSessionID == r.recipient && l.SubjectSessionID == sender) {
			return l.ID
		}
	}
	return ""
}
