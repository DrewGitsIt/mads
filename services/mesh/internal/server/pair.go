package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/pkg/meshnet/tailscale"
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
	CreateDiscoveredPairRequest(ctx context.Context, in contracts.PairDiscoveredRequest) (contracts.PairRequest, error)
	ReportPairMismatch(ctx context.Context, id, fp string) (contracts.PairRequest, error)
	// ForgetPairing removes the device fp's row here (/mesh/forget);
	// dataclient.ErrNotFound when it isn't paired.
	ForgetPairing(ctx context.Context, fp string) error
}

// PairJoin is POST /mesh/pair's body.
type PairJoin struct {
	// Secret is the code's secret, base64url (unpadded), as in the code.
	Secret        string `json:"secret"`
	Name          string `json:"name,omitempty"`
	Platform      string `json:"platform,omitempty"`
	HostsSessions bool   `json:"hosts_sessions,omitempty"`
	// MeshURL is the joiner's own Mesh Service, if it runs one: where this
	// host could view it back. Addresses is its whole list (an older
	// joiner sends only MeshURL).
	MeshURL   string                 `json:"mesh_url,omitempty"`
	Addresses []contracts.NetAddress `json:"addresses,omitempty"`
	// Mode "discovered" is a request with no secret, from a device that
	// found this one in its setup window (docs/MESH-NETWORK-DESIGN.md U4);
	// "" or "code" presents Secret.
	Mode contracts.PairMode `json:"mode,omitempty"`
	// A discovered request is two steps (the commitment, C1): first
	// Commit, the joiner's commitment to its nonce (paircode.Commit),
	// answered with this host's nonce and a CommitID; then CommitID with
	// Nonce, the reveal, checked against the commitment before any code is
	// made or shown.
	Commit   string `json:"commit,omitempty"`
	CommitID string `json:"commit_id,omitempty"`
	Nonce    string `json:"nonce,omitempty"`
	// TailscaleVouch is the joiner's own WhoIs of this host's address:
	// "same tailnet user, untagged". With this host's WhoIs of the joiner
	// agreeing, there's no number to compare.
	TailscaleVouch bool `json:"tailscale_vouch,omitempty"`
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
	// HostNonce is this host's half of a discovered request's check code;
	// Vouched says Tailscale vouched for both ends instead.
	HostNonce string `json:"host_nonce,omitempty"`
	Vouched   bool   `json:"vouched,omitempty"`
}

// PairCommitted is a discovered request's first answer: this host's nonce,
// given only after the joiner committed to its own.
type PairCommitted struct {
	CommitID  string    `json:"commit_id"`
	HostNonce string    `json:"host_nonce"`
	ExpiresAt time.Time `json:"expires_at"`
	State     string    `json:"state"` // "committed"
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
	case isStatus && r.Method == http.MethodPost && strings.HasSuffix(id, "/mismatch") && !strings.Contains(strings.TrimSuffix(id, "/mismatch"), "/"):
		s.pairMismatch(w, r, fp, strings.TrimSuffix(id, "/mismatch"))
	default:
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusNotFound, "not_found", "no such mesh route")
	}
}

func (s *Server) pairJoin(w http.ResponseWriter, r *http.Request, fp devicekey.Fingerprint, tc tailnet.Caller) {
	if wait, ok := s.pairLimits.allowJoin(limitKey(r.RemoteAddr)); !ok {
		closeIfUnadmitted(w, r)
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many pairing attempts; wait a minute and try again")
		return
	}
	c := caller{fp: fp}
	// A key already paired here may ask again (docs/MESH-NETWORK-DESIGN.md
	// F6, R2): its request is taken as any other, and says it's a
	// re-pairing; approval updates its row in place.
	if fp == s.opts.Device {
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusForbidden, "pair_self", "that's this host's own device key: a device can't pair with itself")
		return
	}
	var body PairJoin
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPairBody)).Decode(&body); err != nil {
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	// The host may dial the joiner's addresses (to read what it lets this
	// host do): each must be an address this host could reach on a network
	// it's on, never loopback, link-local or cloud metadata (S3; checked
	// again at every dial).
	addrs, ok := s.joinerAddresses(w, r, body)
	if !ok {
		return
	}
	network := connNet(r.Context())
	if body.Mode == contracts.PairModeDiscovered {
		s.pairDiscovered(w, r, fp, tc, body, addrs, network)
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
		Addresses: addrs, Network: network.ID, NetworkLabel: network.Label,
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
	if req.State == contracts.PairPending {
		log.Printf("mesh: pairing request %s from %s (%q, %s), waiting for approval on this host", req.ID, fp.Short(), req.Name, r.RemoteAddr)
	} else {
		// The same key with the same code again (F6 R7): its request as it is.
		log.Printf("mesh: pairing request %s from %s (%q, %s) presented again, already %s", req.ID, fp.Short(), req.Name, r.RemoteAddr, req.State)
	}
	writeJSON(w, http.StatusCreated, s.pairStatusOf(req))
}

func (s *Server) pairStatus(w http.ResponseWriter, r *http.Request, fp devicekey.Fingerprint, id string) {
	// A device already paired here (approved a moment ago, reading its
	// answer) isn't limited with strangers' polls.
	if wait, ok := s.pairLimits.allowPoll(limitKey(r.RemoteAddr)); !ok && !s.paired(fp) {
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
	if req.Mode == contracts.PairModeDiscovered {
		// Whether Tailscale still vouches as answered (M3): withheld, the
		// joiner confirms the code too.
		st.Vouched = req.Vouched
	}
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

	mu          sync.Mutex
	global      bucket
	discoverAll bucket
	join        map[string]*bucket
	poll        map[string]*bucket
	discover    map[string]*bucket
}

func newPairLimits(now func() time.Time) *pairLimits {
	return &pairLimits{now: now, join: map[string]*bucket{}, poll: map[string]*bucket{}, discover: map[string]*bucket{}}
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

// joinerAddresses is the joiner's addresses (its list, else its
// mesh_url), each checked against S3's shape rules: 422 bad_address when
// one fails, nothing pinned.
func (s *Server) joinerAddresses(w http.ResponseWriter, r *http.Request, body PairJoin) ([]contracts.NetAddress, bool) {
	in := body.Addresses
	if len(in) > contracts.MaxPeerAddresses {
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusUnprocessableEntity, "bad_address", "at most 8 addresses")
		return nil, false
	}
	if len(in) == 0 && body.MeshURL != "" {
		host, ok := contracts.MeshURLHost(body.MeshURL)
		if !ok || paircode.CheckMeshURL(body.MeshURL) != nil {
			closeIfUnadmitted(w, r)
			writeError(w, http.StatusUnprocessableEntity, "bad_address", "this device's mesh address must be http://host:port")
			return nil, false
		}
		in = []contracts.NetAddress{{Via: meshnet.DirectVia, Addr: host}}
	}
	out := make([]contracts.NetAddress, 0, len(in))
	for _, a := range in {
		if a.Via != "" && a.Via != meshnet.DirectVia {
			continue // an adapter this build doesn't have: skipped, never fatal
		}
		if err := s.opts.AddressPolicy.CheckAddress(a.Addr); err != nil {
			closeIfUnadmitted(w, r)
			writeError(w, http.StatusUnprocessableEntity, "bad_address", "this device's address "+strconv.Quote(clip(a.Addr))+" isn't one this host would dial: "+err.Error())
			return nil, false
		}
		a.Via = meshnet.DirectVia
		out = append(out, a)
	}
	return meshnet.CleanNets(out), true
}

// pairDiscovered is a discovered pairing request (U4, S6): accepted only
// while the setup window is open, with the same per-address limits as
// codes (pairJoin's), and at most three pending (the Data Service's). No
// secret is presented, so the host's user compares the check code (both
// screens show it) before approving, unless Tailscale vouches for both
// ends: the request came over the Tailscale network, this host's WhoIs of
// the joiner says "same tailnet user, untagged", and the joiner's WhoIs
// of this host says so too (TailscaleVouch).
func (s *Server) pairDiscovered(w http.ResponseWriter, r *http.Request, fp devicekey.Fingerprint, tc tailnet.Caller, body PairJoin, addrs []contracts.NetAddress, network Net) {
	c := caller{fp: fp}
	if !s.DiscoverableNow() {
		// Outside the window it's no different from a bad code.
		s.refusePair(w, r, c)
		return
	}
	if s.sweepCommits() {
		s.refusePair(w, r, c)
		return
	}
	if body.CommitID == "" {
		s.pairCommit(w, r, fp, body, addrs)
		return
	}
	// The reveal: the commitment it was made against, by the same key,
	// still fresh, and the nonce it committed to.
	cm, found, fresh := s.commits.take(body.CommitID, fp, s.opts.Now())
	if !found || !fresh || !paircode.VerifyCommit(cm.commit, body.Nonce, fp, s.opts.Device) {
		// Only a commitment this host issued counts as abandoned (final
		// pass 2): an invented commit_id spends nothing, so it closes
		// nothing.
		if found && s.commits.spoiled() >= maxAbandoned {
			s.closeWindowAbandoned()
		}
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusUnprocessableEntity, "bad_reveal", "that nonce isn't the one committed to (or the commitment expired): ask again")
		return
	}
	revealed := body.Nonce
	body, addrs = cm.body, cm.addrs
	code, err := paircode.CheckCode(s.opts.Device, fp, revealed, cm.hostNonce)
	if err != nil {
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusUnprocessableEntity, "bad_nonce", err.Error())
		return
	}
	hostNonce := cm.hostNonce
	vouched := false
	node := tc.NodeName
	if network.Tailscale && body.TailscaleVouch && s.opts.Tailnet != nil {
		who := tc
		var werr error
		if tc.UserID == 0 {
			who, werr = s.whois(r.Context(), r.RemoteAddr)
		}
		self := s.self()
		vouched = werr == nil && tailscale.Vouches(self.UserID, who)
		if werr == nil {
			node = who.NodeName
		}
	}
	req, err := s.opts.Pairing.CreateDiscoveredPairRequest(r.Context(), contracts.PairDiscoveredRequest{
		Fingerprint: string(fp), Name: clip(body.Name), Platform: clip(body.Platform), HostsSessions: body.HostsSessions,
		Addresses: addrs, Remote: r.RemoteAddr, TailnetNode: node, Network: network.ID, NetworkLabel: network.Label,
		CheckCode: code, Vouched: vouched,
	})
	var forbidden *dataclient.ForbiddenError
	var limited *dataclient.RateLimitedError
	switch {
	case errors.As(err, &forbidden) && forbidden.Code == "declined_source":
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusForbidden, "declined_source", s.name()+" declined a request from this address: ask its user, or pair with a code")
		return
	case errors.As(err, &forbidden):
		s.refusePair(w, r, c)
		return
	case errors.As(err, &limited):
		closeIfUnadmitted(w, r)
		wait := max(limited.RetryAfter, time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())))
		writeError(w, http.StatusTooManyRequests, "too_many_requests", s.name()+": "+limited.Msg)
		return
	case err != nil:
		log.Printf("mesh: discovered pairing request from %s: %v", fp.Short(), err)
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusServiceUnavailable, "pairing_unavailable", "this host can't take pairing requests right now (is its Data Service up?)")
		return
	}
	how := "check code"
	if vouched {
		how = "Tailscale vouched"
	}
	log.Printf("mesh: discovered pairing request %s from %s (%q, %s, %s), waiting for approval on this host", req.ID, fp.Short(), req.Name, r.RemoteAddr, how)
	st := s.pairStatusOf(req)
	// The store's vouch, as it stands (withheld when another request is
	// waiting, or the name collides), not just this host's WhoIs (T6).
	st.HostNonce, st.Vouched = hostNonce, req.Vouched
	writeJSON(w, http.StatusCreated, st)
}

// pairCommit is a discovered request's first step: the joiner commits to
// its nonce; this host picks its own and answers it. Nothing is shown to
// anyone yet: a commitment that's never revealed expires in a minute.
func (s *Server) pairCommit(w http.ResponseWriter, r *http.Request, fp devicekey.Fingerprint, body PairJoin, addrs []contracts.NetAddress) {
	if body.Nonce != "" || !paircode.ValidCommit(body.Commit) {
		// A nonce sent before this host's would let whoever answers last
		// pick the code.
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusUnprocessableEntity, "commit_required", "a discovered request commits to its nonce first (commit), and reveals it after this host's")
		return
	}
	hostNonce, err := paircode.NewNonce()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.discovered.record(contracts.DeviceDiscovery{Kind: "request", Remote: r.RemoteAddr, Fingerprint: string(fp), Name: clip(body.Name), Network: connNet(r.Context()).ID})
	now := s.opts.Now()
	body.Nonce = ""
	id, abandoned, ok := s.commits.put(commitEntry{fp: fp, commit: body.Commit, hostNonce: hostNonce, body: body, addrs: addrs,
		source: limitKey(r.RemoteAddr), expires: now.Add(commitTTL)}, now)
	if abandoned >= maxAbandoned {
		s.closeWindowAbandoned()
	}
	if !ok || !s.DiscoverableNow() {
		// Past maxCommitsPerWindow (R1): each commitment is a chance at the
		// six digits for someone in the middle; the window takes no more.
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusTooManyRequests, "too_many_pairings",
			s.name()+" has had too many pairing attempts in this setup window: open Add devices there again")
		return
	}
	// Counted when it expires, even if nothing else arrives (final pass 4).
	time.AfterFunc(commitTTL+time.Second, func() { s.sweepCommits() })
	s.notePairStarted(contracts.DeviceDiscovery{Kind: "commit", Remote: r.RemoteAddr, Fingerprint: string(fp), Name: clip(body.Name), Network: connNet(r.Context()).ID})
	writeJSON(w, http.StatusOK, PairCommitted{CommitID: id, HostNonce: hostNonce, ExpiresAt: now.Add(commitTTL), State: "committed"})
}

// Round-2 review R1: someone in the middle posing as a joiner can commit,
// learn this host's code for it before revealing, and abandon until it
// matches the real joiner's. Each commitment is one chance in a million,
// so a window takes at most maxCommitsPerWindow of them (a chance in
// ~10^5 at most), and closes itself, telling its user, once maxAbandoned
// were started and never revealed.
const (
	maxCommitsPerWindow = 8
	maxAbandoned        = 3
)

// notePairStarted records a commitment (device.pair_started), for the
// host to show "a device started pairing".
func (s *Server) notePairStarted(ev contracts.DeviceDiscovery) {
	log.Printf("mesh: setup window: %s (%s) started pairing", ev.Name, devicekey.Fingerprint(ev.Fingerprint).Short())
	s.appendAsync(contracts.DevicePairStarted, ev)
}

// sweepCommits counts the commitments that expired unrevealed, closing
// the window once maxAbandoned were (true: closed now).
func (s *Server) sweepCommits() bool {
	if s.commits.sweep(s.opts.Now()) >= maxAbandoned && s.DiscoverableNow() {
		s.closeWindowAbandoned()
		return true
	}
	return false
}

// closeWindowAbandoned closes the setup window after maxAbandoned
// pairings were started and never finished, and says why.
func (s *Server) closeWindowAbandoned() {
	if !s.DiscoverableNow() {
		return
	}
	started, abandoned, sources := s.commits.counts()
	s.discoverUntil.Store(0)
	s.abuseClosed.Store(true)
	log.Printf("mesh: setup window closed: %d pairings were started and never finished (someone may be trying check codes); open Add devices again to go on", abandoned)
	s.appendAsync(contracts.DevicePairingAbandoned, contracts.PairingAbandoned{Abandoned: abandoned, Started: started, Sources: sources})
	if s.opts.CloseWindow != nil {
		go s.opts.CloseWindow("abandoned_pairings")
	}
}

func (s *Server) appendAsync(typ contracts.EventType, payload any) {
	if s.opts.Audit == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, _ := json.Marshal(payload)
		if err := s.opts.Audit.Append(ctx, &contracts.Envelope{Type: typ, Actor: "mesh", Payload: raw}); err != nil {
			log.Printf("mesh: log %s: %v", typ, err)
		}
	}()
}

// commitTTL is how long a commitment waits for its reveal.
const commitTTL = time.Minute

// maxCommits bounds the commitments kept; one per source address.
const maxCommits = 64

type commitEntry struct {
	fp        devicekey.Fingerprint
	commit    string
	hostNonce string
	body      PairJoin
	addrs     []contracts.NetAddress
	source    string
	expires   time.Time
}

type commitStore struct {
	mu sync.Mutex
	m  map[string]commitEntry
	// started and abandoned count this window's commitments, and those
	// dropped unrevealed (replaced, expired, or revealed wrong).
	started, abandoned int
	sources            map[string]bool
}

// reset starts a new window's counts.
func (c *commitStore) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m, c.started, c.abandoned, c.sources = nil, 0, 0, nil
}

func (c *commitStore) counts() (started, abandoned int, sources []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for s := range c.sources {
		sources = append(sources, s)
	}
	slices.Sort(sources)
	return c.started, c.abandoned, sources
}

// put stores a commitment, unless this window has had
// maxCommitsPerWindow (ok false); abandoned is how many this window were
// dropped unrevealed.
func (c *commitStore) put(e commitEntry, now time.Time) (id string, abandoned int, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]commitEntry{}
	}
	if c.sources == nil {
		c.sources = map[string]bool{}
	}
	c.sweepLocked(now)
	for id, old := range c.m {
		if old.source == e.source {
			// A replacement. The same key asking again (a joiner retrying
			// Add) abandons nothing (final pass 3); another key from the
			// same address does.
			delete(c.m, id)
			if old.fp != e.fp {
				c.abandoned++
			}
		}
	}
	if c.started >= maxCommitsPerWindow {
		return "", c.abandoned, false
	}
	c.started++
	c.sources[e.source] = true
	for len(c.m) >= maxCommits {
		var oldest string
		for id, old := range c.m {
			if oldest == "" || old.expires.Before(c.m[oldest].expires) {
				oldest = id
			}
		}
		delete(c.m, oldest)
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id = "pc_" + base64.RawURLEncoding.EncodeToString(b)
	c.m[id] = e
	return id, c.abandoned, true
}

// sweep drops the commitments that expired unrevealed, counting each as
// abandoned when it expires (final pass 4), not when the next request
// comes; it returns how many this window were abandoned.
func (c *commitStore) sweep(now time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now)
	return c.abandoned
}

func (c *commitStore) sweepLocked(now time.Time) {
	for id, old := range c.m {
		if !now.Before(old.expires) {
			delete(c.m, id)
			c.abandoned++
		}
	}
}

// spoiled counts a reveal that failed (the wrong nonce, too late) as
// abandoned.
func (c *commitStore) spoiled() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.abandoned++
	return c.abandoned
}

// take removes and returns the commitment id: found if this host issued
// it to fp (and it's still held), fresh if it hasn't expired. A
// commitment is revealed once.
func (c *commitStore) take(id string, fp devicekey.Fingerprint, now time.Time) (e commitEntry, found, fresh bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok || e.fp != fp {
		return commitEntry{}, false, false
	}
	delete(c.m, id)
	return e, true, now.Before(e.expires)
}

// limitKey is the address a limit counts by: the IP, an IPv6 address by
// its /64 (one host usually holds a whole /64).
func limitKey(remote string) string {
	host := hostOf(remote)
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	ip = ip.Unmap()
	if ip.Is6() {
		p, _ := ip.Prefix(64)
		return p.String()
	}
	return ip.String()
}

// pairMismatch is POST /mesh/pair/{id}/mismatch: the joiner (by the key
// its request was made with) saying the codes differed. This host
// declines the request, or undoes its approval, so neither side keeps a
// one-sided pairing.
func (s *Server) pairMismatch(w http.ResponseWriter, r *http.Request, fp devicekey.Fingerprint, id string) {
	if wait, ok := s.pairLimits.allowPoll(limitKey(r.RemoteAddr)); !ok {
		closeIfUnadmitted(w, r)
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too fast")
		return
	}
	req, err := s.opts.Pairing.ReportPairMismatch(r.Context(), id, string(fp))
	switch {
	case errors.Is(err, dataclient.ErrNotFound):
		closeIfUnadmitted(w, r)
		writeError(w, http.StatusNotFound, "not_found", "no such pairing request")
		return
	case err != nil:
		var conflict *dataclient.ConflictError
		if errors.As(err, &conflict) {
			writeError(w, http.StatusConflict, "too_late", "that request can't be undone from here any more")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "pairing_unavailable", "this host can't take that right now")
		return
	}
	log.Printf("mesh: %s (%s) said the check code didn't match: request %s undone", req.Name, fp.Short(), id)
	writeJSON(w, http.StatusOK, s.pairStatusOf(req))
}

// handleForget is POST /mesh/forget (docs/MESH-NETWORK-DESIGN.md F6, R1):
// a paired device, at any level (none included), asking this host to
// forget it, before it removes this host on its side. It only takes away
// the caller's own access, so it needs nothing from this host's user: the
// Data Service deletes its row (device.unpaired, by "itself") and its
// live requests end. 204; unpaired_device if it wasn't paired (the
// caller's success too).
func (s *Server) handleForget(w http.ResponseWriter, r *http.Request, c caller) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	if s.opts.Pairing == nil {
		writeError(w, http.StatusNotFound, "not_found", "forgetting isn't available on this host")
		return
	}
	err := s.opts.Pairing.ForgetPairing(r.Context(), string(c.fp))
	switch {
	case errors.Is(err, dataclient.ErrNotFound):
		w.Header().Set("Connection", "close")
		writeError(w, http.StatusForbidden, "unpaired_device", "this host hasn't paired device "+c.fp.Short())
		return
	case err != nil:
		log.Printf("mesh: forget %s: %v", c.label(), err)
		writeError(w, http.StatusServiceUnavailable, "pairing_unavailable", "this host can't change its paired devices right now (is its Data Service up?)")
		return
	}
	log.Printf("mesh: %s asked to be forgotten: unpaired here", c.label())
	// Its streams end now, not at the directory's next reload.
	for _, a := range s.activeReqs() {
		if a.fp == string(c.fp) {
			a.cancel()
		}
	}
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusNoContent)
}
