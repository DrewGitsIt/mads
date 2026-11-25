package mesh

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/pkg/paircode"
)

// Pairing (docs/MESH-AUTH-DESIGN.md D6, docs/MESH-NETWORK-DESIGN.md F2,
// U4), the Router's part.
//
// On the host:
//   - POST /devices/pairing-code makes a code: a fresh secret, registered
//     at the Data Service by its hash only, printed with this host's name,
//     every address its Mesh Service listens on now (mads-pair-2) and its
//     fingerprint. The secret is in this one answer and nowhere else. With
//     no mesh network chosen yet there's nothing to put in it.
//   - The joiner presents it to this host's Mesh Service (/mesh/pair),
//     which opens a request; GET /devices/pair-requests lists them and
//     approve / decline answer them (the Data Service pins the device).
//
// On the joiner (joiner-first):
//   - POST /devices/pair {code} tries the code's addresses that are on a
//     network this device is on (F3's race, pinned to the code's
//     fingerprint). None on a shared network is no_shared_network; none
//     answering, host_unreachable; the code isn't used up either way. The
//     first to complete the pinned handshake carries the secret, with this
//     device's name, platform and addresses. With no mesh network chosen
//     here yet, the network it reached the host over becomes this device's
//     (AdoptedNetwork).
//   - A code from a host already paired here updates its addresses
//     ({update_addresses: true}; otherwise 409 already_paired_update).
//   - It then asks the host for its answer every PairPoll until the
//     request is approved, declined or expires. Approved, it pins the host
//     here, granting it nothing (its user chooses that afterwards), and
//     views it if the host lets this device view. A discovered request
//     (POST /devices/discovered/{fp}/request) pins nothing until this
//     device's user confirms the check code matched
//     (POST /devices/pair/{id}/confirm), unless Tailscale vouched for both
//     ends.
//   - GET /devices/pair/{id} is the joiner's progress.
//
// A join lives in this Router's memory: a restart while waiting loses it
// (the host may still approve; pair again, or `mads pair --accept` it).

// PairingCodeResponse is POST /devices/pairing-code.
type PairingCodeResponse struct {
	ID string `json:"id"`
	// Code is the whole code to copy, secret included.
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	Host      PairHost  `json:"host"`
	// Networks are the networks the code's addresses are on (the dialog
	// names them, not the addresses).
	Networks []NetworkRef `json:"networks"`
}

// PairHost names a host in the pairing answers.
type PairHost struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	MeshURL     string `json:"mesh_url,omitempty"`
	// Addresses are where it may be reached (a code's, or the found
	// device's).
	Addresses []contracts.NetAddress `json:"addresses,omitempty"`
}

// JoinStatus is POST /devices/pair and GET /devices/pair/{id}.
type JoinStatus struct {
	// ID is this device's handle on the join: the host's short fingerprint
	// and its request id ("ABCD-EFGH-IJKL-MNOP:pr_..."), so two hosts'
	// request ids can't collide here. RequestID is the host's own.
	ID        string              `json:"id"`
	RequestID string              `json:"request_id"`
	State     contracts.PairState `json:"state"`
	// Level is what the host granted this device, once approved: "none",
	// view, chat or control.
	Level     string    `json:"level,omitempty"`
	Host      PairHost  `json:"host"`
	ExpiresAt time.Time `json:"expires_at"`
	// PeerID is the row this device pinned the host as, once pinned.
	PeerID string `json:"peer_id,omitempty"`
	// Error says what went wrong pinning the host after approval.
	Error string `json:"error,omitempty"`
	// Self is this device's short fingerprint: what the host's approval
	// prompt should show, for the user to compare before approving.
	Self string `json:"self"`
	// Mode is code or discovered.
	Mode contracts.PairMode `json:"mode,omitempty"`
	// CheckCode is a discovered request's six digits, the same the host
	// shows; Vouched says Tailscale vouched for both ends instead.
	CheckCode string `json:"check_code,omitempty"`
	Vouched   bool   `json:"vouched,omitempty"`
	// Confirm is a discovered join's last step: "needed" (approved; did
	// the host show the same number?), "matched" (pinned) or "mismatch"
	// (nothing pinned).
	Confirm string `json:"confirm,omitempty"`
	// Network is the network the host was reached over ("Tailscale").
	Network string `json:"network,omitempty"`
	// AdoptedNetwork is set when that network became this device's mesh
	// network (it had none: "Your mesh network is now Tailscale"): only
	// once the host is pinned. NotAdopted says why it wasn't ("shared": a
	// shared network is never adopted without the user choosing it).
	AdoptedNetwork *NetworkRef `json:"adopted_network,omitempty"`
	NotAdopted     string      `json:"not_adopted,omitempty"`
}

type join struct {
	mu      sync.Mutex
	status  JoinStatus
	host    contracts.Peer // name, fingerprint, addresses
	mode    contracts.PairMode
	started time.Time
	// level is what the host granted, kept for a discovered join's
	// confirmation.
	level contracts.GrantLevel
	// via is the address the host was reached at: its network becomes this
	// device's mesh network once the host is pinned, if it has none (M1).
	via string
	// repair is the row the host already has here, for a re-pairing (F6
	// R2, R6): approved, it's updated in place rather than pinned anew.
	repair string
}

// joinMemory is how long a join is kept after it started: long past its
// answer, so a late poll still reads it.
const joinMemory = time.Hour

func (j *join) get() JoinStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

func (j *join) set(f func(*JoinStatus)) {
	j.mu.Lock()
	f(&j.status)
	j.mu.Unlock()
}

func (m *Manager) addJoin(key string, j *join) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, old := range m.joins {
		if m.opts.Now().Sub(old.started) > joinMemory {
			delete(m.joins, k)
		}
	}
	m.joins[key] = j
}

// --- This device ---------------------------------------------------------------

type selfCache struct {
	mu    sync.Mutex
	at    time.Time
	l     contracts.MeshListener
	have  bool
	since int64
}

const selfCacheFor = 10 * time.Second

// selfMesh is this node's Mesh Service as it last reported itself
// (mesh.listening), if it ever has. Read incrementally: the report is
// written on every change, so the log holds many.
func (m *Manager) selfMesh(ctx context.Context) (contracts.MeshListener, bool, error) {
	m.self.mu.Lock()
	defer m.self.mu.Unlock()
	if !m.self.at.IsZero() && time.Since(m.self.at) < selfCacheFor {
		return m.self.l, m.self.have, nil
	}
	// The newest written by the Mesh Service itself (actor "mesh"): a
	// mesh.listening from anyone else is ignored.
	types := []string{string(contracts.MeshListening)}
	l, have, since := m.self.l, m.self.have, m.self.since
	for {
		evs, err := m.data.Events(ctx, since, "", types)
		if err != nil {
			return contracts.MeshListener{}, false, err
		}
		if len(evs) == 0 {
			break
		}
		for _, ev := range evs {
			var cand contracts.MeshListener
			// A report says where this host listens (mesh_url, from an
			// older Mesh Service) or what it found (networks: perhaps
			// nowhere yet).
			if ev.Actor == "mesh" && json.Unmarshal(ev.Payload, &cand) == nil && (cand.MeshURL != "" || cand.Networks != nil || cand.Tailscale != nil) {
				l, have = cand, true
			}
		}
		since = evs[len(evs)-1].Seq
	}
	m.self.at, m.self.l, m.self.have, m.self.since = time.Now(), l, have, since
	return l, have, nil
}

// forgetSelf drops the cached report (this device's settings just
// changed: its Mesh Service will report again).
func (m *Manager) forgetSelf() {
	m.self.mu.Lock()
	m.self.at = time.Time{}
	m.self.mu.Unlock()
}

// platform is this device's platform as its hello reports it (its Mesh
// Service's -platform), else this Router's: one answer everywhere.
func (m *Manager) platform(ctx context.Context) string {
	if l, ok, err := m.selfMesh(ctx); err == nil && ok && l.Platform != "" {
		return l.Platform
	}
	return m.opts.Platform
}

// selfAddresses is where this device's Mesh Service listens now: its
// report's addresses, or an older one's mesh_url.
func selfAddresses(l contracts.MeshListener) []contracts.NetAddress {
	if len(l.Addresses) > 0 || l.Networks != nil {
		return l.Addresses
	}
	if host, ok := contracts.MeshURLHost(l.MeshURL); ok {
		return []contracts.NetAddress{{Via: meshnet.DirectVia, Addr: host}}
	}
	return nil
}

// joinerSelf is what a pairing request says about this device: whether it
// hosts sessions, and the addresses the host reaches it back at. With a
// mesh network chosen and nothing reported yet (the Mesh Service starts
// listening a few seconds after the choice, T1), it waits up to ReadyWait
// for the report, then falls back to the chosen networks' own addresses at
// the mesh port, so the host is never left with no address for it (once
// the Mesh Service listens there, the host's hello works).
func (m *Manager) joinerSelf(ctx context.Context) (addrs []contracts.NetAddress, hostsSessions bool, reported bool) {
	deadline := time.Now().Add(m.opts.ReadyWait)
	for {
		l, ok, err := m.selfMesh(ctx)
		if err == nil && ok {
			hostsSessions = l.HostsSessions
			if a := selfAddresses(l); len(a) > 0 {
				return a, hostsSessions, true
			}
		}
		s, serr := m.data.MeshSettings(ctx)
		if serr != nil || len(s.Chosen()) == 0 {
			return nil, hostsSessions, false
		}
		if !time.Now().Before(deadline) {
			return m.chosenAddresses(s), hostsSessions, false
		}
		select {
		case <-ctx.Done():
			return m.chosenAddresses(s), hostsSessions, false
		case <-time.After(250 * time.Millisecond):
		}
		m.forgetSelf()
	}
}

// chosenAddresses are the chosen mesh networks' addresses this device is on
// now, at the mesh port: where its Mesh Service will listen.
func (m *Manager) chosenAddresses(s contracts.MeshSettings) []contracts.NetAddress {
	chosen := map[string]bool{}
	for _, c := range s.Chosen() {
		chosen[c.ID] = true
	}
	var out []contracts.NetAddress
	for _, n := range m.localNetworks() {
		if !chosen[n.ID] {
			continue
		}
		for _, a := range n.Addrs {
			out = append(out, contracts.NetAddress{Via: meshnet.DirectVia, Addr: netip.AddrPortFrom(a, uint16(s.MeshPort())).String(), Kind: string(n.Kind), Label: n.Label, Network: n.ID})
		}
	}
	return out
}

// --- The host's side ---------------------------------------------------------

func (m *Manager) createPairingCode(w http.ResponseWriter, r *http.Request) {
	if m.opts.Identity == nil {
		writeError(w, http.StatusServiceUnavailable, "no_device_key", errNoIdentity.Error())
		return
	}
	l, ok, err := m.selfMesh(r.Context())
	switch {
	case err != nil:
		writeDeviceDataError(w, err)
		return
	case !ok:
		writeError(w, http.StatusConflict, "no_mesh_service",
			"this device's Mesh Service has never run, so no other device can reach it to pair: start it (services/mesh), then make a code")
		return
	case l.Fingerprint != "" && l.Fingerprint != string(m.opts.Identity.Fingerprint()):
		writeError(w, http.StatusConflict, "mesh_key_mismatch",
			"this device's Mesh Service last ran with another device key ("+devicekey.Fingerprint(l.Fingerprint).Short()+"): restart it")
		return
	}
	addrs := selfAddresses(l)
	if len(addrs) == 0 {
		writeError(w, http.StatusConflict, "no_mesh_network",
			"this device doesn't listen on any network yet, so no other device could reach it: set up mesh first (Settings → Paired devices → Set up mesh, or mads mesh setup)")
		return
	}
	secret, err := paircode.NewSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	rec, err := m.client(r.Context()).CreatePairingCode(r.Context(), paircode.Hash(secret))
	if err != nil {
		writeDeviceDataError(w, err)
		return
	}
	fp := m.opts.Identity.Fingerprint()
	c := paircode.Code{Host: m.opts.SelfName, Fingerprint: fp, ExpiresAt: rec.ExpiresAt, Secret: secret}
	var networks []NetworkRef
	for _, a := range addrs {
		c.Addresses = append(c.Addresses, paircode.Address{Via: a.Via, Addr: a.Addr, Kind: a.Kind, Label: a.Label})
		if a.Label != "" && !containsRef(networks, a.Label) {
			networks = append(networks, NetworkRef{ID: a.Network, Label: a.Label, Kind: a.Kind})
		}
	}
	if networks == nil {
		networks = []NetworkRef{}
	}
	writeJSON(w, http.StatusCreated, PairingCodeResponse{
		ID: rec.ID, Code: paircode.Encode(c), ExpiresAt: rec.ExpiresAt, Networks: networks,
		Host: PairHost{Name: m.opts.SelfName, Fingerprint: string(fp), MeshURL: contracts.MeshURLOf(addrs[0].Addr), Addresses: addrs},
	})
}

func containsRef(rs []NetworkRef, label string) bool {
	for _, r := range rs {
		if r.Label == label {
			return true
		}
	}
	return false
}

func (m *Manager) approvePairRequest(w http.ResponseWriter, r *http.Request, id string) {
	var d contracts.PairDecision
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if d.Level == "none" {
		d.Level = contracts.GrantNone
	}
	if d.Level == contracts.GrantAdmin {
		writeError(w, http.StatusUnprocessableEntity, "admin_not_offered",
			"admin is never granted by pairing; once paired, set it on the device's row here (Q2)")
		return
	}
	out, err := m.client(r.Context()).ApprovePairRequest(r.Context(), id, d)
	if err != nil {
		writePairRequestError(w, err)
		return
	}
	// Its own Router pins this device a moment after the approval: until
	// then (pairGuard), a not_paired from it is no answer, and it isn't
	// dialled (heldOff).
	m.notePinned(out.Fingerprint)
	m.noteApproved(out.Fingerprint)
	_ = m.Reload(r.Context())
	writeJSON(w, http.StatusOK, out)
}

// --- The joiner's side ---------------------------------------------------------

// pairCodeError is a code refused here, or a host that couldn't be
// reached; extra carries what the UI shows with it (the host's networks,
// tips).
type pairCodeError struct {
	status int
	code   string
	msg    string
	extra  map[string]any
}

func (e *pairCodeError) Error() string { return e.msg }

// JoinRequest is POST /devices/pair.
type JoinRequest struct {
	Code string `json:"code"`
	// UpdateAddresses: the code is from a host already paired here; update
	// its addresses from it (F2.6) instead of pairing.
	UpdateAddresses bool `json:"update_addresses,omitempty"`
	// Repair: the code is from a host already paired here that no longer
	// has this device paired (repair_offered, F6 R6): pair again, and on
	// approval update its row here in place.
	Repair bool `json:"repair,omitempty"`
}

// UpdateAddressesResult is POST /devices/pair's answer to a code from a
// host already paired here, with update_addresses.
type UpdateAddressesResult struct {
	UpdateAddresses bool   `json:"update_addresses"`
	PeerID          string `json:"peer_id"`
	Name            string `json:"name"`
}

// codeHost is the host a code names, its addresses as a row's.
func codeHost(code paircode.Code, now time.Time) contracts.Peer {
	host := contracts.Peer{Name: code.Host, MeshURL: code.MeshURL, Fingerprint: string(code.Fingerprint)}
	src := contracts.SourceCode
	if code.Version == 1 {
		src = contracts.SourceLegacy
	}
	for _, a := range code.Addresses {
		host.Addresses = append(host.Addresses, contracts.MeshAddress{
			NetAddress: contracts.NetAddress{Via: firstNonEmpty(a.Via, meshnet.DirectVia), Addr: a.Addr, Kind: a.Kind, Label: a.Label},
			Source:     src, AddedAt: now,
		})
	}
	return host
}

// startJoin presents a pasted code to its host and starts waiting for the
// answer, or (update) updates an already-paired host's addresses from it.
func (m *Manager) startJoin(ctx context.Context, in JoinRequest) (any, error) {
	code, err := paircode.Parse(in.Code)
	var verr *paircode.VersionError
	switch {
	case errors.As(err, &verr):
		return nil, &pairCodeError{status: http.StatusUnprocessableEntity, code: "code_version", msg: verr.Error()}
	case errors.Is(err, paircode.ErrNoAddress):
		return nil, &pairCodeError{status: http.StatusUnprocessableEntity, code: "code_no_address", msg: err.Error()}
	case err != nil:
		return nil, &pairCodeError{status: http.StatusUnprocessableEntity, code: "bad_code", msg: err.Error()}
	case m.opts.Identity == nil:
		return nil, &pairCodeError{status: http.StatusServiceUnavailable, code: "no_device_key", msg: errNoIdentity.Error()}
	case code.Fingerprint == m.opts.Identity.Fingerprint():
		return nil, &pairCodeError{status: http.StatusUnprocessableEntity, code: "pair_self", msg: "that's this device's own pairing code: paste it on the other device"}
	}
	peers, err := m.data.Peers(ctx)
	if err != nil {
		return nil, &pairCodeError{status: http.StatusBadGateway, code: "data_unavailable", msg: "this device's Data Service isn't answering"}
	}
	now := m.opts.Now().UTC()
	host := codeHost(code, now)
	repair := ""
	for _, p := range peers {
		if p.Fingerprint != string(code.Fingerprint) {
			continue
		}
		// The code's fingerprint matches the pinned key: it's the manual
		// fix after a host's address changes (F2.6), and doesn't use up
		// the code; unless the host no longer has this device paired (F6
		// R6), checked first: then it's a re-pairing.
		if in.Repair {
			repair = p.ID
			break
		}
		if in.UpdateAddresses {
			if err := m.updateCodeAddresses(ctx, p, host.Addresses); err != nil {
				return nil, err
			}
			return UpdateAddressesResult{UpdateAddresses: true, PeerID: p.ID, Name: p.Name}, nil
		}
		if m.checkCodeHost(ctx, p, host) == contracts.PairingTherePaired {
			return nil, &pairCodeError{status: http.StatusConflict, code: "already_paired_update",
				msg:   p.Name + " is already paired here: update its addresses from this code instead?",
				extra: map[string]any{"peer_id": p.ID, "name": p.Name}}
		}
		return nil, &pairCodeError{status: http.StatusConflict, code: "repair_offered",
			msg:   p.Name + " is paired here, but it doesn't have this device paired now: pair again with this code?",
			extra: map[string]any{"peer_id": p.ID, "name": p.Name}}
	}
	// A minute's grace for clocks that disagree; the host decides anyway.
	if m.opts.Now().After(code.ExpiresAt.Add(time.Minute)) {
		return nil, &pairCodeError{status: http.StatusUnprocessableEntity, code: "code_expired", msg: "that code has expired or was already used; ask for a new one"}
	}
	body := map[string]any{
		"secret":   base64.RawURLEncoding.EncodeToString(code.Secret),
		"name":     m.opts.SelfName,
		"platform": m.platform(ctx),
	}
	if addrs, hosts, _ := m.joinerSelf(ctx); len(addrs) > 0 || hosts {
		body["hosts_sessions"] = hosts
		if len(addrs) > 0 {
			body["mesh_url"], body["addresses"] = contracts.MeshURLOf(addrs[0].Addr), addrs
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := m.conn(ctx, host)
	if err != nil {
		return nil, retryableJoin(joinDialError(host, err), 0)
	}
	raw2, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+c.host+"/mesh/pair", bytes.NewReader(raw2))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, retryableJoin(joinDialError(host, err), 0)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		var e struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		status := resp.StatusCode
		if status < 400 || status >= 500 {
			status = http.StatusBadGateway
		}
		return nil, retryableJoin(&pairCodeError{status: status, code: firstNonEmpty(paircode.CleanText(e.Code), "pair_failed"), msg: firstNonEmpty(paircode.CleanText(e.Error), paircode.CleanText(code.Host)+" answered "+resp.Status)}, retryAfterOf(resp.Header))
	}
	var st struct {
		RequestID string              `json:"request_id"`
		State     contracts.PairState `json:"state"`
		ExpiresAt time.Time           `json:"expires_at"`
	}
	if err := json.Unmarshal(b, &st); err != nil || st.RequestID == "" {
		return nil, &pairCodeError{status: http.StatusBadGateway, code: "pair_failed", msg: paircode.CleanText(code.Host) + " answered something that isn't a pairing request"}
	}
	key := code.Fingerprint.Short() + ":" + st.RequestID
	network := ""
	if n, ok := m.networkOf(c.host); ok {
		network = n.Label
	}
	j := &join{host: host, mode: contracts.PairModeCode, started: m.opts.Now(), repair: repair, status: JoinStatus{
		ID: key, RequestID: st.RequestID, State: contracts.PairPending, ExpiresAt: st.ExpiresAt, Self: m.opts.Identity.Fingerprint().Short(),
		Host: PairHost{Name: paircode.CleanText(code.Host), Fingerprint: string(code.Fingerprint), MeshURL: code.MeshURL, Addresses: netAddresses(host.Addresses)},
		Mode: contracts.PairModeCode, Network: network,
	}}
	j.via = c.host
	m.addJoin(key, j)
	if st.State == contracts.PairDeclined || st.State == contracts.PairExpired {
		// The same code again after the host answered (F6 R7): that answer
		// is final. An approved one still goes through waitJoin, which pins.
		j.set(func(s *JoinStatus) { s.State = st.State })
		return j.get(), nil
	}
	go m.waitJoin(m.bgCtx(), j)
	return j.get(), nil
}

// checkCodeHost checks whether the host of a code, paired here as p, still
// has this device paired (F6 R3, R6), at the code's addresses and the
// row's own. The answer is kept as p's pairing_there; nothing is removed.
func (m *Manager) checkCodeHost(ctx context.Context, p contracts.Peer, host contracts.Peer) string {
	q := p
	q.ID = "" // a probe for the check only: nothing written from its hello
	seen := map[string]bool{}
	q.Addresses = nil
	for _, a := range append(append([]contracts.MeshAddress{}, host.Addresses...), p.Candidates()...) {
		if k := strings.ToLower(a.Addr); !seen[k] && len(q.Addresses) < contracts.MaxPeerAddresses {
			seen[k] = true
			q.Addresses = append(q.Addresses, a)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, checkWithin)
	defer cancel()
	return m.noteThere(ctx, p, m.ProbePeer(cctx, q), false)
}

func netAddresses(as []contracts.MeshAddress) []contracts.NetAddress {
	out := make([]contracts.NetAddress, 0, len(as))
	for _, a := range as {
		out = append(out, a.NetAddress)
	}
	return out
}

// updateCodeAddresses replaces an already-paired host's code-given
// addresses with a newer code's (user-added ones and last_good stay).
func (m *Manager) updateCodeAddresses(ctx context.Context, p contracts.Peer, from []contracts.MeshAddress) error {
	err := m.editPeer(ctx, p.ID, func(cur *contracts.Peer) (bool, error) {
		if cur.Fingerprint != p.Fingerprint {
			return false, errNotEditable
		}
		var out []contracts.MeshAddress
		seen := map[string]bool{}
		for _, a := range from {
			k := strings.ToLower(a.Addr)
			if !seen[k] && meshnet.CheckShape(a.Addr) == nil {
				out = append(out, a)
				seen[k] = true
			}
		}
		for _, a := range cur.Candidates() {
			k := strings.ToLower(a.Addr)
			keep := a.Source == contracts.SourceUser || (cur.LastGood != nil && strings.EqualFold(cur.LastGood.Addr, a.Addr))
			if keep && !seen[k] && len(out) < contracts.MaxPeerAddresses {
				out = append(out, a)
				seen[k] = true
			}
		}
		if len(out) > contracts.MaxPeerAddresses {
			out = out[:contracts.MaxPeerAddresses]
		}
		cur.Addresses = out
		return false, nil
	})
	if err != nil {
		if errors.Is(err, errNotEditable) {
			return &pairCodeError{status: http.StatusConflict, code: "conflict", msg: "the device was re-paired meanwhile; try again"}
		}
		return err
	}
	m.invalidate(p.Fingerprint, "its addresses were updated")
	_ = m.Reload(ctx)
	return nil
}

// joinDialError is why a host couldn't be reached to pair, worded for the
// joiner (F2 step 3, F4): not on a network this device is on
// (no_shared_network, with the host's networks and the tips key), tried
// and none answered (host_unreachable), or another device answered at
// every address (host_key_mismatch). The code isn't used up.
func joinDialError(host contracts.Peer, err error) error {
	name := paircode.CleanText(host.Name)
	var noShared *noSharedNetworkError
	var race *meshnet.RaceError
	var pin *devicekey.PinError
	switch {
	case errors.As(err, &noShared):
		nets := noShared.Networks
		if nets == nil {
			nets = []string{}
		}
		return &pairCodeError{status: http.StatusUnprocessableEntity, code: "no_shared_network",
			msg:   name + " isn't reachable from this network: it's reachable over " + firstNonEmpty(strings.Join(nets, " and "), "networks") + ", and this device isn't on any of them now. Devices only connect over a network they share",
			extra: map[string]any{"host_networks": nets, "tips": "no_shared_network"}}
	case errors.As(err, &race):
		others := 0
		for _, a := range race.Attempts {
			if errors.As(a.Err, &pin) {
				others++
			}
		}
		if others > 0 && others == startedCount(race.Attempts) {
			return &pairCodeError{status: http.StatusBadGateway, code: "host_key_mismatch",
				msg: "the device answering isn't the " + name + " in the code (" + pin.Error() + "); nothing was paired"}
		}
		port := "17380"
		for _, a := range host.Candidates() {
			if _, p, err := net.SplitHostPort(a.Addr); err == nil {
				port = p
				break
			}
		}
		return &pairCodeError{status: http.StatusBadGateway, code: "host_unreachable",
			msg:   "can't reach " + name + ": " + race.Error() + ". Is it awake with mads running, and its firewall letting mads in (port " + port + ")?",
			extra: map[string]any{"tips": "host_unreachable", "port": port}}
	case errors.As(err, &pin):
		return &pairCodeError{status: http.StatusBadGateway, code: "host_key_mismatch",
			msg: "the device answering isn't the " + name + " in the code (" + pin.Error() + "); nothing was paired"}
	}
	state, reason := dialFailure(host, err)
	return &pairCodeError{status: http.StatusBadGateway, code: "host_" + string(state), msg: "can't reach " + name + ": " + reason}
}

// waitJoin asks the host for its answer until there is one.
func (m *Manager) waitJoin(ctx context.Context, j *join) {
	id := j.get().RequestID
	t := time.NewTicker(m.opts.PairPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st, code, err := m.pollJoin(ctx, j.host, id)
		switch {
		case err == nil && code == http.StatusOK:
			switch st.State {
			case contracts.PairApproved:
				level := contracts.GrantLevel(st.Level)
				if st.Level == "none" || !level.Valid() {
					level = contracts.GrantNone
				}
				cur := j.get()
				if j.mode == contracts.PairModeDiscovered && cur.Vouched && !st.Vouched {
					// The host withheld its vouch (another request was
					// waiting, or a name collided): compare the code here too.
					j.set(func(s *JoinStatus) { s.Vouched = false })
					cur.Vouched = false
				}
				if j.mode == contracts.PairModeDiscovered && !cur.Vouched {
					// Both screens confirm: nothing is pinned until this
					// device's user says the host showed the same number.
					j.mu.Lock()
					j.level = level
					j.status.State, j.status.Level, j.status.Confirm = contracts.PairApproved, levelWord(level), "needed"
					j.mu.Unlock()
					return
				}
				m.pinHost(ctx, j, level)
				return
			case contracts.PairDeclined, contracts.PairExpired:
				j.set(func(s *JoinStatus) { s.State = st.State })
				return
			}
		case err == nil && code == http.StatusNotFound:
			j.set(func(s *JoinStatus) { s.State = contracts.PairExpired })
			return
		}
		// The host's expiry, but never longer than a request lives here
		// (R1): a host in the middle can't keep this device waiting.
		deadline := j.get().ExpiresAt
		if max := j.started.Add(m.opts.JoinMaxWait); deadline.IsZero() || deadline.After(max) {
			deadline = max
		}
		if m.opts.Now().After(deadline.Add(m.opts.JoinGrace)) {
			j.set(func(s *JoinStatus) { s.State = contracts.PairExpired })
			return
		}
	}
}

type polled struct {
	State contracts.PairState `json:"state"`
	Level string              `json:"level"`
	// Vouched is whether the host still vouched when it answered (an older
	// host doesn't say: read as not).
	Vouched bool `json:"vouched"`
}

func (m *Manager) pollJoin(ctx context.Context, host contracts.Peer, id string) (polled, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, client, err := m.peerRequest(ctx, http.MethodGet, host, "/mesh/pair/"+url.PathEscape(id), nil)
	if err != nil {
		return polled{}, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return polled{}, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	var st polled
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&st); err != nil {
			return polled{}, 0, err
		}
	}
	return st, resp.StatusCode, nil
}

// confirmJoin is POST /devices/pair/{id}/confirm {match}: this device's
// user saying whether the host showed the same check code. Yes pins the
// host; no pins nothing (another device may be answering as it).
func (m *Manager) confirmJoin(ctx context.Context, id string, match bool) (JoinStatus, error) {
	m.mu.Lock()
	j, ok := m.joins[id]
	m.mu.Unlock()
	if !ok {
		return JoinStatus{}, &pairCodeError{status: http.StatusNotFound, code: "not_found", msg: "no pairing in progress by that id (a Router restart forgets them)"}
	}
	j.mu.Lock()
	if j.status.Confirm != "needed" {
		st := j.status
		j.mu.Unlock()
		if (st.Confirm == "matched" && match) || (st.Confirm == "mismatch" && !match) {
			return st, nil // asked twice: the same answer
		}
		return st, &pairCodeError{status: http.StatusConflict, code: "not_awaiting_confirm", msg: "this pairing isn't waiting for the check code (state " + string(st.State) + ")"}
	}
	if !match {
		j.status.Confirm = "mismatch"
		st := j.status
		host := j.host
		j.mu.Unlock()
		log.Printf("mesh: the check code for %s didn't match: nothing pinned", st.Host.Name)
		// Tell the host, so it undoes its side too (best effort: if another
		// device answered as the host, this reaches that device, which
		// changes nothing).
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			status, _, err := m.peerCallWithin(ctx, 6*time.Second, host, http.MethodPost, "/mesh/pair/"+url.PathEscape(st.RequestID)+"/mismatch", struct{}{})
			if err != nil || status != http.StatusOK {
				log.Printf("mesh: tell %s the check code didn't match: %d %v", st.Host.Name, status, err)
			}
		}()
		return st, nil
	}
	level := j.level
	j.mu.Unlock()
	m.pinHost(ctx, j, level)
	j.set(func(s *JoinStatus) {
		if s.PeerID != "" {
			s.Confirm = "matched"
		}
	})
	return j.get(), nil
}

// pinHost pins an approving host here: granted nothing (this device's user
// chooses), viewed if the host lets this device view and its hello says
// so now, with what it granted recorded as what it lets this device do,
// and the addresses it was reached at.
func (m *Manager) pinHost(ctx context.Context, j *join, level contracts.GrantLevel) {
	now := m.opts.Now().UTC()
	yes := true
	how := contracts.PairCode
	if j.mode == contracts.PairModeDiscovered {
		how = contracts.PairDiscovered
	}
	row := contracts.Peer{
		Name:        pinName(j.host.Name, devicekey.Fingerprint(j.host.Fingerprint)),
		MeshURL:     j.host.MeshURL,
		Addresses:   j.host.Addresses,
		Fingerprint: j.host.Fingerprint,
		Pairing:     &contracts.PeerPairing{How: how},
		Remote:      &contracts.PeerRemote{Level: level, AsOf: &now, HostsSessions: &yes},
	}
	if ps := m.pathFor(j.host.Fingerprint); ps != nil {
		ps.mu.Lock()
		if ps.cur != "" {
			row.LastGood = &contracts.PeerPath{Addr: ps.cur, At: now, RTT: ps.rtt.Milliseconds()}
		}
		ps.mu.Unlock()
	}
	viewed := false
	if level != contracts.GrantNone {
		// The host's Mesh Service learns of the approval from its Data
		// Service's events, a moment after the answer: give it a few
		// seconds before settling for not viewing.
		probe := m.ProbePeer(ctx, row)
		for i := 0; i < 15 && probe.State != contracts.PeerConnected && ctx.Err() == nil; i++ {
			select {
			case <-ctx.Done():
			case <-time.After(200 * time.Millisecond):
			}
			probe = m.ProbePeer(ctx, row)
		}
		viewed = probe.State == contracts.PeerConnected
		if probe.Hello.DNSName != "" {
			row.DNSName = probe.Hello.DNSName
		}
		if r, ok := remoteFromProbe(probe); ok && r.Platform != "" {
			row.Remote.Platform = r.Platform
		}
		if r, ok := remoteFromProbe(probe); ok {
			row.Remote.TailscaleKeyExpiry = r.TailscaleKeyExpiry
		}
		if next, changed := mergeAddresses(row.Addresses, row.LastGood, probe.Hello.Addresses, m.policy, now); changed {
			row.Addresses = next
		}
	}
	row.Viewed = &viewed
	if j.repair != "" {
		m.repairHost(ctx, j, row, level)
		return
	}
	base := row.Name
	var err error
	for i := 2; i <= 9; i++ {
		err = m.data.CreatePeer(ctx, &row)
		var v *contracts.ValidationError
		if err == nil || !errors.As(err, &v) || len(v.Errors) != 1 || v.Errors[0].Field != "name" {
			break
		}
		row.ID, row.Name = "", fmt.Sprintf("%s-%d", base, i)
	}
	if err != nil {
		log.Printf("mesh: pin %s after it approved this device: %v", j.host.Name, err)
		j.set(func(s *JoinStatus) {
			s.State, s.Level = contracts.PairApproved, levelWord(level)
			s.Error = "approved, but this device couldn't pin " + j.host.Name + ": " + err.Error()
		})
		return
	}
	m.notePinned(row.Fingerprint)
	_ = m.Reload(ctx)
	// Pinned (confirmed, for a discovered join): now the network it paired
	// over may become this device's mesh network (joiner-first, F2).
	adopted, why := m.adoptNetwork(ctx, j.via)
	j.set(func(s *JoinStatus) {
		s.State, s.Level, s.PeerID = contracts.PairApproved, levelWord(level), row.ID
		s.AdoptedNetwork, s.NotAdopted = adopted, why
	})
}

// repairHost is pinHost for a re-pairing (F6 R2, R6): the host's row here
// is updated in place, keeping its id, name, viewed flag and the level
// this device gives it; only the other side is new: its addresses (the
// user's own kept), what it lets this device do, and how it was reached.
func (m *Manager) repairHost(ctx context.Context, j *join, row contracts.Peer, level contracts.GrantLevel) {
	err := m.editPeer(ctx, j.repair, func(cur *contracts.Peer) (bool, error) {
		if cur.Fingerprint != row.Fingerprint {
			return false, errNotEditable
		}
		out := append([]contracts.MeshAddress{}, row.Addresses...)
		seen := map[string]bool{}
		for _, a := range out {
			seen[strings.ToLower(a.Addr)] = true
		}
		for _, a := range cur.Candidates() {
			if a.Source == contracts.SourceUser && !seen[strings.ToLower(a.Addr)] && len(out) < contracts.MaxPeerAddresses {
				out = append(out, a)
			}
		}
		if len(out) > contracts.MaxPeerAddresses {
			out = out[:contracts.MaxPeerAddresses]
		}
		if len(out) > 0 {
			cur.Addresses = out
		}
		if row.LastGood != nil {
			cur.LastGood = row.LastGood
		}
		if row.DNSName != "" {
			cur.DNSName = row.DNSName
		}
		remote := *row.Remote
		if cur.Remote != nil && remote.Platform == "" {
			remote.Platform = cur.Remote.Platform
		}
		cur.Remote = &remote
		return false, nil
	})
	if err != nil {
		if errors.Is(err, errNotEditable) || errors.Is(err, dataclient.ErrNotFound) {
			err = errors.New("its row here changed or was removed meanwhile")
		}
		log.Printf("mesh: re-pair %s after it approved this device: %v", j.host.Name, err)
		j.set(func(s *JoinStatus) {
			s.State, s.Level = contracts.PairApproved, levelWord(level)
			s.Error = "approved, but this device couldn't update " + j.host.Name + " here: " + err.Error()
		})
		return
	}
	m.notePinned(row.Fingerprint)
	m.invalidate(row.Fingerprint, "it was paired again")
	_ = m.Reload(ctx)
	adopted, why := m.adoptNetwork(ctx, j.via)
	j.set(func(s *JoinStatus) {
		s.State, s.Level, s.PeerID = contracts.PairApproved, levelWord(level), j.repair
		s.AdoptedNetwork, s.NotAdopted = adopted, why
	})
}

// pinName makes a host's name (from its code: anyone's text) safe as a
// row name, as the Data Service does for a joiner's.
func pinName(name string, fp devicekey.Fingerprint) string {
	var b strings.Builder
	for _, c := range name {
		if b.Len() >= 60 {
			break
		}
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			b.WriteRune(c)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" || reservedNames[out] || strings.HasPrefix(out, "per_") {
		out = "device-" + strings.ToLower(string(fp[:8]))
	}
	return out
}

func levelWord(l contracts.GrantLevel) string {
	if l == contracts.GrantNone {
		return "none"
	}
	return string(l)
}

func (m *Manager) joinStatus(id string) (JoinStatus, bool) {
	m.mu.Lock()
	j, ok := m.joins[id]
	m.mu.Unlock()
	if !ok {
		return JoinStatus{}, false
	}
	return j.get(), true
}
