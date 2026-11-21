package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
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
type pairing struct {
	log   *store.Log
	peers *store.Registry[contracts.Peer]
	now   func() time.Time

	mu       sync.Mutex
	codes    map[string]*pairCode // by id
	requests map[string]*contracts.PairRequest
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
	p := &pairing{log: l, peers: peers, now: now, codes: map[string]*pairCode{}, requests: map[string]*contracts.PairRequest{}}
	mux.HandleFunc("POST /pairing/codes", p.handleCreateCode)
	mux.HandleFunc("DELETE /pairing/codes/{id}", p.handleDeleteCode)
	mux.HandleFunc("POST /pairing/redeem", p.handleRedeem)
	mux.HandleFunc("GET /pairing/requests", p.handleListRequests)
	mux.HandleFunc("GET /pairing/requests/{id}", p.handleGetRequest)
	mux.HandleFunc("POST /pairing/requests/{id}/approve", p.handleApprove)
	mux.HandleFunc("POST /pairing/requests/{id}/decline", p.handleDecline)
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
		p.mu.Unlock()
		refusedPairing(w)
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
	p.requests[req.ID] = req
	out := *req
	p.mu.Unlock()
	p.appendEvent(r, contracts.DevicePairRequested, out)
	writeJSON(w, http.StatusCreated, out)
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

func (p *pairing) handleListRequests(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.sweepLocked(p.now().UTC())
	out := make([]contracts.PairRequest, 0, len(p.requests))
	for _, r := range p.requests {
		out = append(out, *r)
	}
	p.mu.Unlock()
	for i := range out {
		p.markCollision(r, &out[i])
	}
	slices.SortFunc(out, func(a, b contracts.PairRequest) int { return a.CreatedAt.Compare(b.CreatedAt) })
	writeJSON(w, http.StatusOK, out)
}

func (p *pairing) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.sweepLocked(p.now().UTC())
	req, ok := p.requests[r.PathValue("id")]
	var out contracts.PairRequest
	if ok {
		out = *req
	}
	p.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such pairing request"})
		return
	}
	p.markCollision(r, &out)
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
	p.mu.Lock()
	defer p.mu.Unlock()
	req, ok := p.pending(w, r)
	if !ok {
		return
	}
	no := false
	hosts := req.HostsSessions
	row := contracts.Peer{
		Name:        req.Name,
		Viewed:      &no,
		MeshURL:     req.MeshURL,
		Fingerprint: req.Fingerprint,
		Pairing:     &contracts.PeerPairing{How: contracts.PairCode},
		Remote:      &contracts.PeerRemote{Platform: req.Platform, HostsSessions: &hosts},
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

func (p *pairing) handleDecline(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	req, ok := p.pending(w, r)
	if !ok {
		p.mu.Unlock()
		return
	}
	at := p.now().UTC()
	req.State, req.DecidedAt = contracts.PairDeclined, &at
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
	// This host's mesh settings ("Only accept devices on my tailnet"): read
	// by the Mesh Service, which follows device.settings_changed; written
	// only by this host's own user (the install token, not relayed).
	mux.HandleFunc("GET /devices/settings", func(w http.ResponseWriter, r *http.Request) {
		s, err := l.MeshSettings(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
	mux.HandleFunc("PUT /devices/settings", func(w http.ResponseWriter, r *http.Request) {
		if _, relayed := r.Header[contracts.MeshRelayHeader]; relayed || !store.HasHostAuthority(writeContext(r)) {
			writeJSON(w, http.StatusForbidden, map[string]string{"code": "forbidden", "error": "only this host's own user changes its mesh settings"})
			return
		}
		var s contracts.MeshSettings
		if !decode(w, r, &s) {
			return
		}
		if s.RequireTailnet == nil {
			v := &contracts.ValidationError{}
			v.Add("require_tailnet", "required: true or false")
			writeJSON(w, http.StatusUnprocessableEntity, v)
			return
		}
		if err := l.SetMeshSettings(r.Context(), s, actor(r)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s)
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
