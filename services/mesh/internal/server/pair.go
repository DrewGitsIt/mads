package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/paircode"
	"github.com/drewkidwell/mads/services/mesh/internal/tailnet"
)

// Pairing by code, the host's side (docs/MESH-AUTH-DESIGN.md D6):
//
//   - POST /mesh/pair {secret, name, platform, hosts_sessions, mesh_url}:
//     a device presents a pairing code's secret. Its key is the one the
//     TLS handshake proved; the request it opens waits for this host's
//     approval. The only route a key that isn't paired here may use.
//   - GET /mesh/pair/{id}: that device asks how its request went, by the
//     same key; another key reads nothing.
//
// Both are rate-limited, per address and overall. A wrong, expired or
// used secret gets one generic answer (no oracle) and is audited as
// device.refused (pairing_refused), debounced like every refusal. The
// secret itself is only ever hashed (paircode.Hash) and passed on: never
// logged, never in an event.

// Pairing is the Data Service's pairing store, as the Mesh Service uses it.
type Pairing interface {
	RedeemPairingCode(ctx context.Context, in contracts.PairRedeem) (contracts.PairRequest, error)
	PairRequest(ctx context.Context, id string) (contracts.PairRequest, error)
}

// PairJoin is POST /mesh/pair's body.
type PairJoin struct {
	// Secret is the code's secret, base64url (unpadded), as in the code.
	Secret        string `json:"secret"`
	Name          string `json:"name,omitempty"`
	Platform      string `json:"platform,omitempty"`
	HostsSessions bool   `json:"hosts_sessions,omitempty"`
	// MeshURL is the joiner's own Mesh Service, if it runs one: where this
	// host could view it back.
	MeshURL string `json:"mesh_url,omitempty"`
}

// PairStatus is /mesh/pair's answer and GET /mesh/pair/{id}'s.
type PairStatus struct {
	RequestID string              `json:"request_id"`
	State     contracts.PairState `json:"state"`
	// Level is what this host granted the device, once approved: "none",
	// view, chat or control.
	Level     string    `json:"level,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	Host      PairHost  `json:"host"`
}

// PairHost names this host in a PairStatus.
type PairHost struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
}

const maxPairBody = 16 << 10

// pairRefusal is the one answer for every bad code.
const pairRefusal = "that pairing code isn't valid here: it may have expired or been used. Ask for a new one"

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request, fp devicekey.Fingerprint, tc tailnet.Caller) {
	id, isStatus := strings.CutPrefix(r.URL.Path, "/mesh/pair/")
	switch {
	case s.opts.Pairing == nil:
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusNotFound, "not_found", "pairing isn't available on this host")
	case !isStatus && r.Method == http.MethodPost:
		s.pairJoin(w, r, fp, tc)
	case isStatus && r.Method == http.MethodGet && id != "" && !strings.Contains(id, "/"):
		s.pairStatus(w, r, fp, id)
	default:
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusNotFound, "not_found", "no such mesh route")
	}
}

func (s *Server) pairJoin(w http.ResponseWriter, r *http.Request, fp devicekey.Fingerprint, tc tailnet.Caller) {
	if wait, ok := s.pairLimits.allowJoin(hostOf(r.RemoteAddr)); !ok {
		closeIfUnadmitted(w, r)
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many pairing attempts; wait a minute and try again")
		return
	}
	c := caller{fp: fp}
	switch {
	case fp == s.opts.Device:
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusForbidden, "pair_self", "that's this host's own device key: a device can't pair with itself")
		return
	case s.paired(fp):
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusConflict, "already_paired", "this device is already paired with "+s.name()+
			"; to pair it again, remove it there first (Settings → Paired devices, or mads pair --revoke)")
		return
	}
	var body PairJoin
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPairBody)).Decode(&body); err != nil {
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	// The host will dial the joiner's address (to read what it lets this
	// host do): it must be a device on this tailnet, never a way to make
	// this host reach anything else.
	if body.MeshURL != "" && !s.onTailnet(r.Context(), body.MeshURL) {
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusUnprocessableEntity, "bad_mesh_url", "this device's mesh address must be a device on this host's tailnet")
		return
	}
	secret, err := base64.RawURLEncoding.DecodeString(body.Secret)
	if err != nil || len(secret) != paircode.SecretLen {
		s.refusePair(w, r, c)
		return
	}
	req, err := s.opts.Pairing.RedeemPairingCode(r.Context(), contracts.PairRedeem{
		SecretHash: paircode.Hash(secret), Fingerprint: string(fp),
		Name: clip(body.Name), Platform: clip(body.Platform), HostsSessions: body.HostsSessions,
		MeshURL: clip(body.MeshURL), Remote: r.RemoteAddr, TailnetNode: tc.NodeName,
	})
	var forbidden *dataclient.ForbiddenError
	switch {
	case errors.As(err, &forbidden):
		s.refusePair(w, r, c)
		return
	case err != nil:
		log.Printf("mesh: pairing request from %s: %v", fp.Short(), err)
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusServiceUnavailable, "pairing_unavailable", "this host can't take pairing requests right now (is its Data Service up?)")
		return
	}
	log.Printf("mesh: pairing request %s from %s (%q, %s), waiting for approval on this host", req.ID, fp.Short(), req.Name, r.RemoteAddr)
	writeJSON(w, http.StatusCreated, s.pairStatusOf(req))
}

func (s *Server) pairStatus(w http.ResponseWriter, r *http.Request, fp devicekey.Fingerprint, id string) {
	// A device already paired here (approved a moment ago, reading its
	// answer) isn't limited with strangers' polls.
	if wait, ok := s.pairLimits.allowPoll(hostOf(r.RemoteAddr)); !ok && !s.paired(fp) {
		closeIfUnadmitted(w, r)
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "polling too fast")
		return
	}
	req, err := s.opts.Pairing.PairRequest(r.Context(), id)
	switch {
	case errors.Is(err, dataclient.ErrNotFound) || (err == nil && req.Fingerprint != string(fp)):
		// Another key's request reads exactly as no request.
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusNotFound, "not_found", "no such pairing request")
		return
	case err != nil:
		log.Printf("mesh: pairing status %s for %s: %v", clip(id), fp.Short(), err)
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusServiceUnavailable, "pairing_unavailable", "this host can't read pairing requests right now")
		return
	}
	writeJSON(w, http.StatusOK, s.pairStatusOf(req))
}

func (s *Server) pairStatusOf(req contracts.PairRequest) PairStatus {
	st := PairStatus{RequestID: req.ID, State: req.State, ExpiresAt: req.ExpiresAt,
		Host: PairHost{Name: s.name(), Fingerprint: string(s.opts.Device)}}
	if req.State == contracts.PairApproved {
		st.Level = string(req.Level)
		if req.Level == contracts.GrantNone {
			st.Level = "none"
		}
	}
	return st
}

// name is this host's mads name (what other devices label it), else its
// tailnet host name.
func (s *Server) name() string {
	if s.opts.Name != "" {
		return s.opts.Name
	}
	return s.opts.Self.HostName
}

// paired reports whether fp is already paired here.
func (s *Server) paired(fp devicekey.Fingerprint) bool {
	_, ok := s.opts.Devices.Lookup(string(fp))
	return ok
}

func (s *Server) refusePair(w http.ResponseWriter, r *http.Request, c caller) {
	s.refuseDevice(w, r, c, "/mesh/pair", http.StatusForbidden, "pairing_refused", pairRefusal)
}

// --- Rate limits -------------------------------------------------------------

// Pairing attempts: a few per address a minute, a few dozen overall; status
// polls more generously, per address. A 256-bit secret can't be guessed
// anyway: the limits keep a hostile client from loading the Data Service
// or the audit log.
var (
	pairJoinPerAddr = rate{burst: 5, every: 12 * time.Second}
	// Overall, a backstop well above any real use: the per-address bucket
	// is the defence, so one address can't spend what real joiners need.
	pairJoinGlobal  = rate{burst: 120, every: 500 * time.Millisecond}
	pairPollPerAddr = rate{burst: 20, every: time.Second}
)

type rate struct {
	burst int
	every time.Duration // one token back per every
}

type bucket struct {
	tokens float64
	at     time.Time
}

// take spends a token from b at rate r, or says how long until one is back.
func (b *bucket) take(r rate, now time.Time) (time.Duration, bool) {
	if b.at.IsZero() {
		b.tokens, b.at = float64(r.burst), now
	}
	b.tokens = min(float64(r.burst), b.tokens+float64(now.Sub(b.at))/float64(r.every))
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	return time.Duration((1 - b.tokens) * float64(r.every)), false
}

type pairLimits struct {
	now func() time.Time

	mu     sync.Mutex
	global bucket
	join   map[string]*bucket
	poll   map[string]*bucket
}

func newPairLimits(now func() time.Time) *pairLimits {
	return &pairLimits{now: now, join: map[string]*bucket{}, poll: map[string]*bucket{}}
}

// pairMemory bounds the addresses remembered per limit.
const pairMemory = 4096

func (l *pairLimits) perAddr(m map[string]*bucket, addr string, r rate, now time.Time) (time.Duration, bool) {
	b, ok := m[addr]
	if !ok {
		if len(m) >= pairMemory {
			clear(m)
		}
		b = &bucket{}
		m[addr] = b
	}
	return b.take(r, now)
}

func (l *pairLimits) allowJoin(addr string) (time.Duration, bool) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if wait, ok := l.perAddr(l.join, addr, pairJoinPerAddr, now); !ok {
		return wait, false
	}
	return l.global.take(pairJoinGlobal, now)
}

func (l *pairLimits) allowPoll(addr string) (time.Duration, bool) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.perAddr(l.poll, addr, pairPollPerAddr, now)
}

// onTailnet reports whether another device's chosen mesh address is a
// device on this host's tailnet: a MagicDNS name under its suffix, or a
// Tailscale address. 100.64.0.0/10 is also carrier-grade NAT space, so
// with the tailnet required an IP must also be one of this user's tailnet
// nodes (WhoIs); without it the range is all there is to check.
func (s *Server) onTailnet(ctx context.Context, meshURL string) bool {
	if paircode.CheckTarget(meshURL, paircode.TargetPolicy{TailnetSuffix: paircode.TailnetSuffixOf(s.opts.Self.DNSName)}) != nil {
		return false
	}
	ip, isIP := paircode.TargetIP(meshURL)
	if !isIP || !s.requireTailnet.Load() {
		return true
	}
	tc, err := s.whois(ctx, net.JoinHostPort(ip.String(), "1"))
	return err == nil && tc.UserID == s.opts.Self.UserID
}
