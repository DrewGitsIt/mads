package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/pkg/paircode"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// Pairing by code (docs/MESH-AUTH-DESIGN.md D6). Codes and the
// requests they make live in memory here, never on disk: they last
// minutes, and a restart simply voids them. Of a code only the SHA-256 of
// its secret is kept (paircode.Hash); the Router makes the secret and
// prints it, the Mesh Service hashes what a joiner presents.
//
//   - POST /pairing/codes (the Router, for POST /devices/pairing-code):
//     a new code from its secret's hash; past contracts.MaxPairingCodes
//     outstanding, the oldest is retired. device.code_created.
//   - DELETE /pairing/codes/{id}: retire one (the dialog closed).
//   - POST /pairing/redeem (the Mesh Service, for /mesh/pair): consume a
//     code atomically and open a request for the device that presented
//     it. Wrong, expired and used codes all get the same 403
//     pairing_refused: no oracle. device.pair_requested.
//   - GET /pairing/requests, GET /pairing/requests/{id}: the prompt's list,
//     and the joiner's poll (through the Mesh Service).
//   - POST /pairing/requests/{id}/approve {level, name}: pins the device
//     (pairing.how "code"; device.paired), never at admin, and only with
//     host authority (the install token, not relayed).
//   - POST /pairing/requests/{id}/decline: device.pair_declined.
//   - POST /pairing/forget {fingerprint} (the Mesh Service, for
//     /mesh/forget): the device asked to be forgotten; its row goes
//     (device.unpaired, by "itself").
//
// A key already paired here may ask again (docs/MESH-NETWORK-DESIGN.md
// F6, R2): its request carries repair (its row and level now), and
// approval updates that row in place. A code is single-use per key (R7):
// the key that redeemed it gets its request back from the same secret
// again, for the request's lifetime.
type pairing struct {
	log   *store.Log
	peers *store.Registry[contracts.Peer]
	now   func() time.Time

	mu       sync.Mutex
	codes    map[string]*pairCode // by id
	requests map[string]*contracts.PairRequest
	// blocked is discovered requests' sources this host declined, with the
	// window (its opened_at) they're blocked for (R6).
	blocked map[string]time.Time
	// redeemed is the codes used, by request id: a lost answer can be
	// retried by the same key (F6 R7).
	redeemed map[string]*redeemedCode
}

// redeemedCode is a used code: its secret's hash, the key that used it,
// and until when that key may present it again.
type redeemedCode struct {
	hash        []byte
	fingerprint string
	until       time.Time
}

type pairCode struct {
	contracts.PairingCode
	hash []byte // SHA-256 of the secret
}

// decidedMemory is how long an answered or expired request stays readable,
// so a joiner polling a little late still learns the outcome.
const decidedMemory = 10 * time.Minute

func pairingRoutes(mux *http.ServeMux, l *store.Log, peers *store.Registry[contracts.Peer], now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	p := &pairing{log: l, peers: peers, now: now, codes: map[string]*pairCode{}, requests: map[string]*contracts.PairRequest{}, redeemed: map[string]*redeemedCode{}}
	mux.HandleFunc("POST /pairing/codes", p.handleCreateCode)
	mux.HandleFunc("DELETE /pairing/codes/{id}", p.handleDeleteCode)
	mux.HandleFunc("POST /pairing/redeem", p.handleRedeem)
	mux.HandleFunc("POST /pairing/discovered", p.handleDiscovered)
	mux.HandleFunc("GET /pairing/requests", p.handleListRequests)
	mux.HandleFunc("GET /pairing/requests/{id}", p.handleGetRequest)
	mux.HandleFunc("POST /pairing/requests/{id}/approve", p.handleApprove)
	mux.HandleFunc("POST /pairing/requests/{id}/decline", p.handleDecline)
	mux.HandleFunc("POST /pairing/requests/{id}/mismatch", p.handleMismatch)
	mux.HandleFunc("POST /pairing/forget", p.handleForget)
}

func newPairID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// sweepLocked drops expired codes, marks expired requests, and forgets
// requests decided long enough ago.
func (p *pairing) sweepLocked(now time.Time) {
	for id, c := range p.codes {
		if !now.Before(c.ExpiresAt) {
			delete(p.codes, id)
		}
	}
	for id, r := range p.requests {
		if r.State == contracts.PairPending && !now.Before(r.ExpiresAt) {
			r.State = contracts.PairExpired
			at := r.ExpiresAt
			r.DecidedAt = &at
		}
		if r.DecidedAt != nil && now.Sub(*r.DecidedAt) > decidedMemory {
			delete(p.requests, id)
		}
	}
	for id, c := range p.redeemed {
		if !now.Before(c.until) {
			delete(p.redeemed, id)
		}
	}
}

func (p *pairing) appendEvent(req *http.Request, typ contracts.EventType, payload any) {
	raw, _ := json.Marshal(payload)
	if err := p.log.Append(req.Context(), &contracts.Envelope{Type: typ, Actor: actor(req), Payload: raw}); err != nil {
		log.Printf("api: append %s: %v", typ, err)
	}
}

func (p *pairing) handleCreateCode(w http.ResponseWriter, r *http.Request) {
	var in contracts.PairingCodeCreate
	if !decode(w, r, &in) {
		return
	}
	if !paircode.ValidHash(in.SecretHash) {
		v := &contracts.ValidationError{}
		v.Add("secret_hash", "must be the secret's SHA-256, 64 lower-case hex characters")
		writeJSON(w, http.StatusUnprocessableEntity, v)
		return
	}
	hash, _ := hex.DecodeString(in.SecretHash)
	now := p.now().UTC()
	c := &pairCode{PairingCode: contracts.PairingCode{ID: newPairID("pc_"), CreatedAt: now, ExpiresAt: now.Add(contracts.PairingCodeTTL)}, hash: hash}
	p.mu.Lock()
	p.sweepLocked(now)
	for len(p.codes) >= contracts.MaxPairingCodes {
		var oldest *pairCode
		for _, o := range p.codes {
			if oldest == nil || o.CreatedAt.Before(oldest.CreatedAt) {
				oldest = o
			}
		}
		delete(p.codes, oldest.ID)
	}
	p.codes[c.ID] = c
	p.mu.Unlock()
	p.appendEvent(r, contracts.DeviceCodeCreated, c.PairingCode)
	writeJSON(w, http.StatusCreated, c.PairingCode)
}

func (p *pairing) handleDeleteCode(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	_, ok := p.codes[r.PathValue("id")]
	delete(p.codes, r.PathValue("id"))
	p.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such pairing code (used, expired or never made)"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// refusedPairing is redeem's one answer for every bad code.
func refusedPairing(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]string{"code": "pairing_refused", "error": "that pairing code isn't valid here: it may have expired or been used"})
}

func (p *pairing) handleRedeem(w http.ResponseWriter, r *http.Request) {
	var in contracts.PairRedeem
	if !decode(w, r, &in) {
		return
	}
	fp, err := devicekey.ParseFingerprint(in.Fingerprint)
	if err != nil || string(fp) != in.Fingerprint {
		v := &contracts.ValidationError{}
		v.Add("fingerprint", "must be a canonical device fingerprint")
		writeJSON(w, http.StatusUnprocessableEntity, v)
		return
	}
	hash, err := hex.DecodeString(in.SecretHash)
	if err != nil || !paircode.ValidHash(in.SecretHash) {
		refusedPairing(w)
		return
	}
	now := p.now().UTC()
	p.mu.Lock()
	p.sweepLocked(now)
	var match *pairCode
	// Every outstanding code is compared, in constant time each, so how
	// long this takes says nothing about which (if any) matched.
	for _, c := range p.codes {
		if subtle.ConstantTimeCompare(c.hash, hash) == 1 {
			match = c
		}
	}
	if match == nil {
		// A code already used is single use per key (R7): the key that used
		// it gets its request back, whatever its state, so a reply lost
		// after this host took the request can be retried; any other key is
		// refused as for any bad code.
		var again *contracts.PairRequest
		for id, c := range p.redeemed {
			if subtle.ConstantTimeCompare(c.hash, hash) == 1 && c.fingerprint == in.Fingerprint {
				again = p.requests[id]
			}
		}
		if again == nil {
			p.mu.Unlock()
			refusedPairing(w)
			return
		}
		out := *again
		p.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
		return
	}
	delete(p.codes, match.ID) // single use, consumed under the lock
	req := &contracts.PairRequest{
		ID:          newPairID("pr_"),
		Fingerprint: in.Fingerprint,
		Name:        pairName(in.Name, fp),
		Platform:    pairPlatform(in.Platform),
		Remote:      clipText(in.Remote, 128),
		TailnetNode: clipText(in.TailnetNode, 128),
		State:       contracts.PairPending,
		CreatedAt:   now,
		ExpiresAt:   now.Add(contracts.PairRequestTTL),
	}
	req.HostsSessions = in.HostsSessions
	if paircode.CheckMeshURL(in.MeshURL) == nil {
		req.MeshURL = strings.TrimSuffix(in.MeshURL, "/")
	}
	req.Mode = contracts.PairModeCode
	req.Addresses = meshnet.CleanNets(in.Addresses)
	req.Network, req.NetworkLabel = clipText(in.Network, 128), clipText(paircode.CleanText(in.NetworkLabel), 64)
	req.Repair = p.repairOf(r, in.Fingerprint)
	p.requests[req.ID] = req
	p.redeemed[req.ID] = &redeemedCode{hash: match.hash, fingerprint: in.Fingerprint, until: now.Add(contracts.PairRequestTTL)}
	out := *req
	p.mu.Unlock()
	p.appendEvent(r, contracts.DevicePairRequested, out)
	writeJSON(w, http.StatusCreated, out)
}

// repairOf is a request's repair: the row fp is paired under here now,
// if any (R2).
func (p *pairing) repairOf(r *http.Request, fp string) *contracts.PairRepair {
	row, ok := p.rowOf(r, fp)
	if !ok {
		return nil
	}
	return &contracts.PairRepair{PeerID: row.ID, Level: row.Level()}
}

// freshRepair re-reads a pending request's repair from the rows now: the
// row it named may have been removed (or re-leveled) while it waited, and
// the prompt says what approving replaces.
func (p *pairing) freshRepair(r *http.Request, req *contracts.PairRequest) {
	if req.Repair == nil || req.State != contracts.PairPending {
		return
	}
	req.Repair = p.repairOf(r, req.Fingerprint)
}

// rowOf is the row fingerprint fp is pinned to here.
func (p *pairing) rowOf(r *http.Request, fp string) (contracts.Peer, bool) {
	rows, err := p.peers.List(r.Context())
	if err != nil {
		return contracts.Peer{}, false
	}
	for _, row := range rows {
		if row.Fingerprint == fp {
			return row, true
		}
	}
	return contracts.Peer{}, false
}

// handleDiscovered is POST /pairing/discovered (the Mesh Service, for a
// discovered /mesh/pair): a request with no secret, from a device that
// found this one while its setup window was open
// (docs/MESH-NETWORK-DESIGN.md U4, S6). Accepted only while the window is
// open (checked here too, not only by the Mesh Service), at most
// contracts.MaxDiscoveredPending at once. A device asking again replaces
// its own pending request.
func (p *pairing) handleDiscovered(w http.ResponseWriter, r *http.Request) {
	if _, relayed := r.Header[contracts.MeshRelayHeader]; relayed {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "forbidden", "error": "a pairing request isn't made for another device"})
		return
	}
	var in contracts.PairDiscoveredRequest
	if !decode(w, r, &in) {
		return
	}
	fp, err := devicekey.ParseFingerprint(in.Fingerprint)
	if err != nil || string(fp) != in.Fingerprint {
		v := &contracts.ValidationError{}
		v.Add("fingerprint", "must be a canonical device fingerprint")
		writeJSON(w, http.StatusUnprocessableEntity, v)
		return
	}
	if !validCheckCode(in.CheckCode) {
		v := &contracts.ValidationError{}
		v.Add("check_code", "six digits")
		writeJSON(w, http.StatusUnprocessableEntity, v)
		return
	}
	now := p.now().UTC()
	win, err := p.log.Discoverable(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !win.Open(now) {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "not_discoverable", "error": "this host isn't adding devices now"})
		return
	}
	p.mu.Lock()
	p.applyWindowLocked(win, now)
	p.sweepLocked(now)
	// One pending request per source address (an IPv6 one by its /64), and
	// MaxDiscoveredPending sources at once (round-1 review M2): throwaway
	// keys from one address hold one slot, never all of them.
	source := sourceKey(in.Remote)
	if opened, ok := p.blocked[source]; ok && opened.Equal(win.OpenedAt) {
		p.mu.Unlock()
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "declined_source", "error": "this host declined a request from this address: it takes no more from it in this setup window"})
		return
	}
	pending := 0
	var earliest time.Time
	for _, old := range p.requests {
		if old.State != contracts.PairPending || old.Mode != contracts.PairModeDiscovered {
			continue
		}
		if old.Fingerprint == in.Fingerprint {
			// Its own earlier request: superseded.
			old.State, old.DecidedAt = contracts.PairExpired, &now
			continue
		}
		if sourceKey(old.Remote) == source {
			p.mu.Unlock()
			tooMany(w, old.ExpiresAt, now, "a request from this address is already waiting on this host")
			return
		}
		pending++
		if earliest.IsZero() || old.ExpiresAt.Before(earliest) {
			earliest = old.ExpiresAt
		}
	}
	if pending >= contracts.MaxDiscoveredPending {
		p.mu.Unlock()
		tooMany(w, earliest, now, "this host already has requests waiting")
		return
	}
	req := &contracts.PairRequest{
		ID:           newPairID("pr_"),
		Fingerprint:  in.Fingerprint,
		Name:         pairName(in.Name, fp),
		Platform:     pairPlatform(in.Platform),
		Remote:       clipText(in.Remote, 128),
		TailnetNode:  clipText(in.TailnetNode, 128),
		Mode:         contracts.PairModeDiscovered,
		CheckCode:    in.CheckCode,
		Vouched:      in.Vouched,
		Addresses:    meshnet.CleanNets(in.Addresses),
		Network:      clipText(in.Network, 128),
		NetworkLabel: clipText(paircode.CleanText(in.NetworkLabel), 64),
		State:        contracts.PairPending,
		CreatedAt:    now,
		ExpiresAt:    now.Add(contracts.PairRequestTTL),
	}
	req.HostsSessions = in.HostsSessions
	req.Repair = p.repairOf(r, in.Fingerprint)
	if win.Until.Add(windowGrace).Before(req.ExpiresAt) {
		// It can't outlive the window it was made in by much: a minute's
		// grace to answer one that came in at the end.
		req.ExpiresAt = win.Until.Add(windowGrace)
	}
	p.requests[req.ID] = req
	out := *req
	pendingNow := pendingDiscoveredLocked(p.requests)
	p.mu.Unlock()
	// The vouch as it stands, from the first answer (T6): withheld with
	// another request waiting or a name collision, so both screens agree.
	p.markCollision(r, &out)
	withholdVouch(&out, pendingNow)
	p.appendEvent(r, contracts.DevicePairRequested, out)
	writeJSON(w, http.StatusCreated, out)
}

// tooMany is the 429 for a discovered request, saying when a slot frees.
func tooMany(w http.ResponseWriter, until, now time.Time, what string) {
	wait := until.Sub(now)
	if wait < time.Second {
		wait = time.Second
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Round(time.Second).Seconds())))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"code": "too_many_requests",
		"error":       fmt.Sprintf("%s; try again after %s (in %s)", what, until.Local().Format("15:04:05"), wait.Round(time.Second)),
		"retry_after": int(wait.Round(time.Second).Seconds())})
}

// sourceKey is a request's source address as limits count it: the IP, an
// IPv6 one by its /64.
func sourceKey(remote string) string {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	ip = ip.Unmap()
	if ip.Is6() {
		pre, _ := ip.Prefix(64)
		return pre.String()
	}
	return ip.String()
}

// windowGrace is how long a discovered request outlives the window it
// was made in (an answer to one made at its very end).
const windowGrace = time.Minute

// applyWindowLocked ends discovered requests the setup window no longer
// covers: a window closed early (or run out) takes its pending requests
// with it within windowGrace (round-1 tester T2). p.mu must be held.
func (p *pairing) applyWindowLocked(win contracts.Discoverable, now time.Time) {
	end := win.End()
	if win.Open(now) {
		end = win.Until
	}
	deadline := end.Add(windowGrace)
	for _, r := range p.requests {
		if r.Mode == contracts.PairModeDiscovered && r.State == contracts.PairPending && r.ExpiresAt.After(deadline) {
			r.ExpiresAt = deadline
		}
	}
}

// window reads the setup window for applyWindowLocked (closed when it
// can't be read: discovered requests then can't be answered).
func (p *pairing) window(r *http.Request) contracts.Discoverable {
	win, err := p.log.Discoverable(r.Context())
	if err != nil {
		return contracts.Discoverable{}
	}
	return win
}

func validCheckCode(c string) bool {
	if len(c) != 6 {
		return false
	}
	for _, d := range c {
		if d < '0' || d > '9' {
			return false
		}
	}
	return true
}

// pairName is a name a device gave, made safe to pin under (a peer name
// is a URL path segment on the Router, store.validatePeer): characters
// outside letters, digits, '.', '_' and '-' become '-', at most 63; none
// left is "device-" and the fingerprint's start, as `mads pair --accept`
// names one.
func pairName(name string, fp devicekey.Fingerprint) string {
	var b strings.Builder
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			b.WriteRune(c)
		default:
			b.WriteRune('-')
		}
		if b.Len() >= 63 {
			break
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" || out == "candidates" || strings.HasPrefix(out, "per_") {
		out = "device-" + strings.ToLower(string(fp[:8]))
	}
	return out
}

func pairPlatform(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if !contracts.ValidPlatform(p) {
		return ""
	}
	return p
}

// hostOnly refuses a relayed request, or one without host authority.
func hostOnly(w http.ResponseWriter, r *http.Request, msg string) bool {
	if _, relayed := r.Header[contracts.MeshRelayHeader]; relayed || !store.HasHostAuthority(writeContext(r)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "forbidden", "error": msg})
		return false
	}
	return true
}

func clipText(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// markCollision flags a pending request whose name another row here
// already has.
func (p *pairing) markCollision(r *http.Request, req *contracts.PairRequest) {
	if req.State != contracts.PairPending {
		return
	}
	if row, err := p.peers.GetByName(r.Context(), req.Name); err == nil && row.Fingerprint != req.Fingerprint {
		req.NameCollides = true
	}
}

// withholdVouch is M3: a vouched discovered request still needs its check
// code when another discovered request is waiting too, or its name is
// another device's here. pending is how many discovered requests wait.
func withholdVouch(req *contracts.PairRequest, pending int) {
	if req.Mode != contracts.PairModeDiscovered || !req.Vouched || req.State != contracts.PairPending {
		return
	}
	switch {
	case pending > 1:
		req.Vouched, req.VouchWithheld = false, "another_request"
	case req.NameCollides:
		req.Vouched, req.VouchWithheld = false, "name_collides"
	}
}

func pendingDiscoveredLocked(reqs map[string]*contracts.PairRequest) int {
	n := 0
	for _, r := range reqs {
		if r.Mode == contracts.PairModeDiscovered && r.State == contracts.PairPending {
			n++
		}
	}
	return n
}

func (p *pairing) handleListRequests(w http.ResponseWriter, r *http.Request) {
	win := p.window(r)
	p.mu.Lock()
	p.applyWindowLocked(win, p.now().UTC())
	p.sweepLocked(p.now().UTC())
	out := make([]contracts.PairRequest, 0, len(p.requests))
	for _, r := range p.requests {
		out = append(out, *r)
	}
	pending := pendingDiscoveredLocked(p.requests)
	p.mu.Unlock()
	for i := range out {
		p.markCollision(r, &out[i])
		withholdVouch(&out[i], pending)
		p.freshRepair(r, &out[i])
	}
	slices.SortFunc(out, func(a, b contracts.PairRequest) int { return a.CreatedAt.Compare(b.CreatedAt) })
	writeJSON(w, http.StatusOK, out)
}

func (p *pairing) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	win := p.window(r)
	p.mu.Lock()
	p.applyWindowLocked(win, p.now().UTC())
	p.sweepLocked(p.now().UTC())
	req, ok := p.requests[r.PathValue("id")]
	var out contracts.PairRequest
	if ok {
		out = *req
	}
	pending := pendingDiscoveredLocked(p.requests)
	p.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such pairing request"})
		return
	}
	p.markCollision(r, &out)
	withholdVouch(&out, pending)
	p.freshRepair(r, &out)
	writeJSON(w, http.StatusOK, out)
}

// decide is approve and decline's common part: the request, pending, under
// the lock. The caller must hold host authority: only this host's own user
// answers a pairing request, never a relayed device.
func (p *pairing) pending(w http.ResponseWriter, r *http.Request) (*contracts.PairRequest, bool) {
	if _, relayed := r.Header[contracts.MeshRelayHeader]; relayed || !store.HasHostAuthority(writeContext(r)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "forbidden", "error": "only this host's own user answers a pairing request"})
		return nil, false
	}
	p.sweepLocked(p.now().UTC())
	req, ok := p.requests[r.PathValue("id")]
	switch {
	case !ok:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such pairing request"})
		return nil, false
	case req.State == contracts.PairExpired:
		writeJSON(w, http.StatusConflict, map[string]string{"code": "expired", "state": string(req.State), "error": "this pairing request has expired; ask for a new code"})
		return nil, false
	case req.State != contracts.PairPending:
		writeJSON(w, http.StatusConflict, map[string]string{"code": "already_answered", "state": string(req.State), "error": "this pairing request was already " + string(req.State)})
		return nil, false
	}
	return req, true
}

func (p *pairing) handleApprove(w http.ResponseWriter, r *http.Request) {
	var d contracts.PairDecision
	if !decode(w, r, &d) {
		return
	}
	if d.Level == "none" {
		d.Level = contracts.GrantNone
	}
	switch {
	case d.Level == contracts.GrantAdmin:
		v := &contracts.ValidationError{}
		v.Add("level", "admin is never granted by pairing; set it afterwards on this host (Q2)")
		writeJSON(w, http.StatusUnprocessableEntity, v)
		return
	case d.Level != contracts.GrantNone && !d.Level.Valid():
		v := &contracts.ValidationError{}
		v.Add("level", fmt.Sprintf("must be none, view, chat or control; got %q", d.Level))
		writeJSON(w, http.StatusUnprocessableEntity, v)
		return
	}
	// Held across the pin, so a request is approved (or declined) once.
	win := p.window(r)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applyWindowLocked(win, p.now().UTC())
	req, ok := p.pending(w, r)
	if !ok {
		return
	}
	// The vouch as it stands now is the one the joiner learns (its poll):
	// withheld, the joiner compares the code too.
	p.markCollision(r, req)
	withholdVouch(req, pendingDiscoveredLocked(p.requests))
	// What the approver saw has to still hold (R5): a discovered request
	// is approved on a confirmed check code, or on a vouch that's still
	// there and was the one shown.
	vouchHolds := req.Vouched && d.ShownVouched
	if req.Mode == contracts.PairModeDiscovered && !d.CheckCodeConfirmed && !vouchHolds {
		code, msg := "check_code_required", "compare the check code with the joining device's screen first"
		if req.VouchWithheld != "" {
			code, msg = "vouch_withheld", "Tailscale's vouch for this request is withheld now ("+req.VouchWithheld+"): compare the check code first"
		}
		writeJSON(w, http.StatusConflict, map[string]any{"code": code, "error": msg, "vouch_withheld": req.VouchWithheld})
		return
	}
	if cur, ok := p.rowOf(r, req.Fingerprint); ok {
		p.repairInPlace(w, r, req, cur, d)
		return
	}
	no := false
	hosts := req.HostsSessions
	how, source := contracts.PairCode, contracts.SourceJoiner
	if req.Mode == contracts.PairModeDiscovered {
		how, source = contracts.PairDiscovered, contracts.SourceDiscovered
	}
	row := contracts.Peer{
		Name:        req.Name,
		Viewed:      &no,
		MeshURL:     req.MeshURL,
		Fingerprint: req.Fingerprint,
		Pairing:     &contracts.PeerPairing{How: how},
		Remote:      &contracts.PeerRemote{Platform: req.Platform, HostsSessions: &hosts},
	}
	// The joiner's own addresses (was one mesh_url), where this host could
	// reach it back: the store mirrors mesh_url from them.
	at0 := p.now().UTC()
	for _, a := range req.Addresses {
		row.Addresses = append(row.Addresses, contracts.MeshAddress{NetAddress: a, Source: source, AddedAt: at0})
	}
	if len(row.Addresses) == 0 && req.MeshURL != "" {
		if host, ok := contracts.MeshURLHost(req.MeshURL); ok && meshnet.CheckShape(host) == nil {
			row.Addresses = []contracts.MeshAddress{{NetAddress: contracts.NetAddress{Via: meshnet.DirectVia, Addr: host}, Source: source, AddedAt: at0}}
		}
	}
	if d.Name != "" {
		// The approver's own choice, cleaned as a device's is.
		row.Name = pairName(d.Name, devicekey.Fingerprint(req.Fingerprint))
	}
	// Always a grant, none included: the device is paired for access here
	// (an admin device may manage it remotely), and its limits, once set,
	// survive a spell at none.
	row.Grant = &contracts.PeerGrant{Level: d.Level}
	err := p.pin(r, &row, d.Name == "")
	if err != nil {
		var verr *contracts.ValidationError
		if errors.As(err, &verr) && slices.ContainsFunc(verr.Errors, func(e contracts.FieldError) bool { return e.Field == "fingerprint" }) {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "already_paired", "error": "this device is already paired here: " + verr.Error()})
			return
		}
		writeStoreError(w, err)
		return
	}
	at := p.now().UTC()
	// The name it's pinned under (cleaned, perhaps suffixed or the
	// approver's), not the one it claimed.
	req.State, req.Level, req.PeerID, req.DecidedAt, req.Name = contracts.PairApproved, d.Level, row.ID, &at, row.Name
	writeJSON(w, http.StatusOK, *req)
}

// repairInPlace approves a request from a key already paired here (R2):
// the row it has is updated in place (same id and name; the level just
// chosen, its limits kept; the request's addresses, with the ones this
// host's user added; pairing.how from this request), and device.paired
// says it was a re-pairing. p.mu is held.
func (p *pairing) repairInPlace(w http.ResponseWriter, r *http.Request, req *contracts.PairRequest, cur contracts.Peer, d contracts.PairDecision) {
	how, source := contracts.PairCode, contracts.SourceJoiner
	if req.Mode == contracts.PairModeDiscovered {
		how, source = contracts.PairDiscovered, contracts.SourceDiscovered
	}
	row := cur
	at0 := p.now().UTC()
	var addrs []contracts.MeshAddress
	seen := map[string]bool{}
	for _, a := range req.Addresses {
		addrs = append(addrs, contracts.MeshAddress{NetAddress: a, Source: source, AddedAt: at0})
		seen[strings.ToLower(a.Addr)] = true
	}
	if len(addrs) == 0 && req.MeshURL != "" {
		if host, ok := contracts.MeshURLHost(req.MeshURL); ok && meshnet.CheckShape(host) == nil {
			addrs = append(addrs, contracts.MeshAddress{NetAddress: contracts.NetAddress{Via: meshnet.DirectVia, Addr: host}, Source: source, AddedAt: at0})
			seen[strings.ToLower(host)] = true
		}
	}
	if len(addrs) > 0 {
		for _, a := range cur.Addresses {
			if a.Source == contracts.SourceUser && !seen[strings.ToLower(a.Addr)] && len(addrs) < contracts.MaxPeerAddresses {
				addrs = append(addrs, a)
			}
		}
		row.Addresses = addrs
		if row.LastGood != nil && !slices.ContainsFunc(addrs, func(a contracts.MeshAddress) bool { return strings.EqualFold(a.Addr, row.LastGood.Addr) }) {
			row.LastGood = nil
		}
	}
	hosts := req.HostsSessions
	remote := contracts.PeerRemote{}
	if cur.Remote != nil {
		remote = *cur.Remote
	}
	if req.Platform != "" {
		remote.Platform = req.Platform
	}
	remote.HostsSessions = &hosts
	row.Remote = &remote
	grant := contracts.PeerGrant{Level: d.Level}
	if cur.Grant != nil {
		grant.Ceilings = cur.Grant.Ceilings
	}
	row.Grant = &grant
	row.Pairing = &contracts.PeerPairing{How: how}
	if err := p.peers.UpdateIfUnchanged(store.WithRepair(writeContext(r)), &row, actor(r), cur.UpdatedAt); err != nil {
		writeStoreError(w, err)
		return
	}
	at := p.now().UTC()
	req.State, req.Level, req.PeerID, req.DecidedAt, req.Name = contracts.PairApproved, d.Level, row.ID, &at, row.Name
	writeJSON(w, http.StatusOK, *req)
}

// handleForget is POST /pairing/forget (the Mesh Service, for a paired
// device's /mesh/forget, F6 R1): the device the handshake proved asked to
// be forgotten, so its row goes, both sides (device.unpaired, by
// "itself"). It only takes away that device's own access here. Never
// relayed: only the Mesh Service, for the caller itself, sends it.
func (p *pairing) handleForget(w http.ResponseWriter, r *http.Request) {
	if _, relayed := r.Header[contracts.MeshRelayHeader]; relayed {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "forbidden", "error": "only the device itself asks to be forgotten"})
		return
	}
	var in contracts.PairForget
	if !decode(w, r, &in) {
		return
	}
	fp, err := devicekey.ParseFingerprint(in.Fingerprint)
	if err != nil || string(fp) != in.Fingerprint {
		v := &contracts.ValidationError{}
		v.Add("fingerprint", "must be a canonical device fingerprint")
		writeJSON(w, http.StatusUnprocessableEntity, v)
		return
	}
	row, ok := p.rowOf(r, in.Fingerprint)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"code": "device_not_found", "error": "no device " + fp.Short() + " is paired here"})
		return
	}
	err = p.peers.Delete(store.WithUnpairedBy(writeContext(r), "itself"), row.ID, actor(r))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"code": "device_not_found", "error": "no device " + fp.Short() + " is paired here"})
		return
	case err != nil:
		writeStoreError(w, err)
		return
	}
	log.Printf("api: %s (%s) asked to be forgotten: removed", row.Name, fp.Short())
	w.WriteHeader(http.StatusNoContent)
}

// pin creates row; with autoName, a name already taken by another device
// gets "-2", "-3", ... instead of failing.
func (p *pairing) pin(r *http.Request, row *contracts.Peer, autoName bool) error {
	base := row.Name
	for i := 2; ; i++ {
		err := p.peers.Create(writeContext(r), row, actor(r))
		var verr *contracts.ValidationError
		if err == nil || !autoName || i > 9 || !errors.As(err, &verr) ||
			!slices.ContainsFunc(verr.Errors, func(e contracts.FieldError) bool { return e.Field == "name" }) ||
			slices.ContainsFunc(verr.Errors, func(e contracts.FieldError) bool { return e.Field != "name" }) {
			return err
		}
		row.ID = ""
		row.Name = clipText(base, 60) + "-" + strconv.Itoa(i)
	}
}

// mismatchWithin is how long after approval a joiner can still undo a
// discovered pairing by reporting a code mismatch.
const mismatchWithin = 10 * time.Minute

// handleMismatch is POST /pairing/requests/{id}/mismatch (the Mesh
// Service, for the joiner whose request it was, proved by its key): the
// joiner's user said the codes differed. A pending request is declined;
// one approved minutes ago is undone (its device unpinned here), so no
// one-sided pairing remains. Both are on record as device.pair_declined
// with joiner_mismatch.
func (p *pairing) handleMismatch(w http.ResponseWriter, r *http.Request) {
	if _, relayed := r.Header[contracts.MeshRelayHeader]; relayed {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "forbidden", "error": "only the joiner itself reports a mismatch"})
		return
	}
	var in struct {
		Fingerprint string `json:"fingerprint"`
	}
	if !decode(w, r, &in) {
		return
	}
	now := p.now().UTC()
	p.mu.Lock()
	req, ok := p.requests[r.PathValue("id")]
	if !ok || req.Fingerprint != in.Fingerprint || req.Mode != contracts.PairModeDiscovered {
		p.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such pairing request"})
		return
	}
	switch {
	case req.State == contracts.PairPending:
	case req.State == contracts.PairApproved && req.PeerID != "" && req.DecidedAt != nil && now.Sub(*req.DecidedAt) < mismatchWithin:
		// Only the row that's still this joiner's (R7): one re-pinned or
		// replaced since is someone else's to keep.
		row, err := p.peers.Get(r.Context(), req.PeerID)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			p.mu.Unlock()
			writeStoreError(w, err)
			return
		case row.Fingerprint == req.Fingerprint:
			if err := p.peers.DeleteIfUnchanged(writeContext(r), req.PeerID, actor(r), row.UpdatedAt); err != nil && !errors.Is(err, store.ErrNotFound) {
				p.mu.Unlock()
				writeStoreError(w, err)
				return
			}
		}
	default:
		out := *req
		p.mu.Unlock()
		writeJSON(w, http.StatusConflict, out)
		return
	}
	req.State, req.DecidedAt, req.JoinerMismatch, req.PeerID = contracts.PairDeclined, &now, true, ""
	out := *req
	p.mu.Unlock()
	log.Printf("api: pairing request %s undone: %s said its check code didn't match", out.ID, out.Name)
	p.appendEvent(r, contracts.DevicePairDeclined, out)
	writeJSON(w, http.StatusOK, out)
}

func (p *pairing) handleDecline(w http.ResponseWriter, r *http.Request) {
	win := p.window(r)
	p.mu.Lock()
	req, ok := p.pending(w, r)
	if !ok {
		p.mu.Unlock()
		return
	}
	at := p.now().UTC()
	req.State, req.DecidedAt = contracts.PairDeclined, &at
	if req.Mode == contracts.PairModeDiscovered {
		// The host's user turned this source away: no more discovered
		// requests from it for the rest of this window (R6), so a few
		// aliases can't refill the slots after each decline.
		if p.blocked == nil {
			p.blocked = map[string]time.Time{}
		}
		p.blocked[sourceKey(req.Remote)] = win.OpenedAt
	}
	out := *req
	p.mu.Unlock()
	p.appendEvent(r, contracts.DevicePairDeclined, out)
	writeJSON(w, http.StatusOK, out)
}

// --- Device activity --------------------------------------------------------

func deviceActivityRoutes(mux *http.ServeMux, l *store.Log) {
	mux.HandleFunc("GET /devices/activity", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var f store.DeviceActivityFilter
		if d := q.Get("device"); d != "" {
			fp, err := devicekey.ParseFingerprint(d)
			if err != nil {
				v := &contracts.ValidationError{}
				v.Add("device", err.Error())
				writeJSON(w, http.StatusUnprocessableEntity, v)
				return
			}
			f.Fingerprint, f.Short = string(fp), fp.Short()
		}
		for name, dst := range map[string]*int64{"before": &f.Before} {
			if raw := q.Get(name); raw != "" {
				n, err := strconv.ParseInt(raw, 10, 64)
				if err != nil || n < 0 {
					writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": name + " must be a non-negative integer"})
					return
				}
				*dst = n
			}
		}
		if raw := q.Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "limit must be a non-negative integer"})
				return
			}
			f.Limit = n
		}
		events, err := l.DeviceActivity(r.Context(), f)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if events == nil {
			events = []contracts.Envelope{}
		}
		writeJSON(w, http.StatusOK, events)
	})
	// This host's mesh settings (docs/MESH-NETWORK-DESIGN.md): the
	// mesh_settings row, read by the Mesh Service (which follows
	// device.settings_changed) and the Router; written only by this host's
	// own user (the install token, not relayed).
	mux.HandleFunc("GET /devices/settings", func(w http.ResponseWriter, r *http.Request) {
		s, err := l.MeshSettings(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
	mux.HandleFunc("PUT /devices/settings", func(w http.ResponseWriter, r *http.Request) {
		if !hostOnly(w, r, "only this host's own user changes its mesh settings") {
			return
		}
		// networks absent with require_tailnet given is the earlier form
		// ("Only accept devices on my tailnet"): it sets the Tailscale
		// network's check and leaves the rest.
		var in struct {
			Networks       *[]contracts.NetworkChoice `json:"networks"`
			Port           int                        `json:"port"`
			Public         bool                       `json:"public"`
			RequireTailnet *bool                      `json:"require_tailnet"`
		}
		if !decode(w, r, &in) {
			return
		}
		var s contracts.MeshSettings
		switch {
		case in.Networks != nil:
			s = contracts.MeshSettings{Networks: *in.Networks, Port: in.Port, Public: in.Public, RequireTailnet: in.RequireTailnet}
		case in.RequireTailnet != nil:
			cur, err := l.MeshSettings(r.Context())
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			s = cur
			for i := range s.Networks {
				if s.Networks[i].ID == "tailscale" {
					s.Networks[i].TailnetCheck = *in.RequireTailnet
				}
			}
			s.RequireTailnet = in.RequireTailnet
		default:
			v := &contracts.ValidationError{}
			v.Add("networks", "required: this device's network choices")
			writeJSON(w, http.StatusUnprocessableEntity, v)
			return
		}
		out, err := l.SetMeshSettings(r.Context(), s, actor(r))
		var verr *contracts.ValidationError
		switch {
		case errors.As(err, &verr):
			writeJSON(w, http.StatusUnprocessableEntity, verr)
			return
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	// This host's setup window (U4): opened, renewed and closed by this
	// host's own user at the device (the Router's POST
	// /devices/discoverable), never remotely; the Mesh Service follows
	// device.discoverable.
	mux.HandleFunc("GET /devices/discoverable", func(w http.ResponseWriter, r *http.Request) {
		d, err := l.Discoverable(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	mux.HandleFunc("PUT /devices/discoverable", func(w http.ResponseWriter, r *http.Request) {
		if !hostOnly(w, r, "only this host's own user opens its setup window") {
			return
		}
		var in contracts.Discoverable
		if !decode(w, r, &in) {
			return
		}
		reason := ""
		if in.Until.IsZero() {
			reason = clipText(in.Reason, 64)
		}
		d, err := l.SetDiscoverableReason(r.Context(), in.Until, actor(r), in.Renew, reason)
		if errors.Is(err, store.ErrWindowClosed) {
			writeJSON(w, http.StatusConflict, map[string]string{"code": "window_closed", "error": err.Error()})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	mux.HandleFunc("GET /devices/last-used", func(w http.ResponseWriter, r *http.Request) {
		out, err := l.DeviceLastUsed(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
