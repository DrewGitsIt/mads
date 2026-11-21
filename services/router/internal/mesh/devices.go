package mesh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/paircode"
)

// Devices serves the Router's /devices routes: Settings → Paired devices
// (docs/MESH-AUTH-DESIGN.md D5, D6, D8, D9). The Router's route table
// registers each pattern (api.go) so the front door and the mesh route
// table classify it; this serves them.
//
// Local only (the install token's holder; Deny over the mesh):
//
//	GET    /devices/self                          this device
//	PUT    /devices/settings                      {require_tailnet}: "Only
//	                                              accept devices on my tailnet"
//	GET    /devices/activity?device=&limit=&before= the device activity log
//	POST   /devices/pairing-code                  a pairing code (host)
//	DELETE /devices/pairing-code/{id}             retire an unused one
//	GET    /devices/pair-requests                 requests waiting here
//	POST   /devices/pair-requests/{id}/approve    {level, name?}
//	POST   /devices/pair-requests/{id}/decline
//	POST   /devices/pair                          {code}: pair with a host (joiner)
//	GET    /devices/pair/{id}                     how that's going
//	POST   /devices/{fp}/introduce                {host, level}: introduce a
//	                                              device paired here to a host
//	                                              this device administers
//	POST   /devices/{fp}/remove-everywhere        {hosts?}: remove here, then on
//	                                              every host this device administers
//
// Also over the mesh, for a device administering this host (authz Admin):
//
//	GET    /devices                               every row, with its status
//	POST   /devices/introduce                     {fingerprint, name, level, ...}
//	PUT    /devices/{fp}/grant                    {level, ceilings?}
//	DELETE /devices/{fp}                          remove a device
//
// Over the mesh the Router caps what an admin device does (at most its own
// level, never admin, only the level), and the Data Service's peer
// registry checks it again against the stored rows.
func (m *Manager) Devices() http.Handler {
	mux := http.NewServeMux()
	local := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if _, relayed := dataclient.MeshRelay(r.Context()); relayed {
				writeError(w, http.StatusForbidden, "local_only", "this route is this device's own, never another's")
				return
			}
			if !m.admit(w, r) {
				return
			}
			h(w, r.WithContext(asUser(r.Context())))
		}
	}
	// admitted is also open to a device administering this host: the
	// Mesh Service already required admin; checked again here.
	admitted := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !m.admit(w, r) {
				return
			}
			if level, relayed := relayedLevel(r); relayed {
				if level != contracts.GrantAdmin {
					writeError(w, http.StatusForbidden, "no_access", "managing this host's devices needs admin here")
					return
				}
			} else {
				r = r.WithContext(asUser(r.Context()))
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /devices", admitted(m.listDevices))
	mux.HandleFunc("GET /devices/self", local(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.selfDevice(r.Context()))
	}))
	mux.HandleFunc("PUT /devices/settings", local(m.setMeshSettings))
	mux.HandleFunc("GET /devices/activity", local(m.deviceActivity))
	mux.HandleFunc("POST /devices/pairing-code", local(m.createPairingCode))
	mux.HandleFunc("DELETE /devices/pairing-code/{id}", local(func(w http.ResponseWriter, r *http.Request) {
		if err := m.client(r.Context()).DeletePairingCode(r.Context(), r.PathValue("id")); err != nil {
			if errors.Is(err, dataclient.ErrNotFound) {
				writeError(w, http.StatusNotFound, "pairing_code_not_found", "no such pairing code (used, expired or never made)")
				return
			}
			writeDeviceDataError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /devices/pair-requests", local(func(w http.ResponseWriter, r *http.Request) {
		list, err := m.data.PairRequests(r.Context())
		if err != nil {
			writeDeviceDataError(w, err)
			return
		}
		if list == nil {
			list = []contracts.PairRequest{}
		}
		writeJSON(w, http.StatusOK, list)
	}))
	mux.HandleFunc("POST /devices/pair-requests/{id}/approve", local(func(w http.ResponseWriter, r *http.Request) {
		m.approvePairRequest(w, r, r.PathValue("id"))
	}))
	mux.HandleFunc("POST /devices/pair-requests/{id}/decline", local(func(w http.ResponseWriter, r *http.Request) {
		out, err := m.client(r.Context()).DeclinePairRequest(r.Context(), r.PathValue("id"))
		if err != nil {
			writePairRequestError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("POST /devices/pair", local(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
			return
		}
		st, err := m.startJoin(r.Context(), body.Code)
		var pe *pairCodeError
		switch {
		case errors.As(err, &pe):
			writeError(w, pe.status, pe.code, pe.msg)
		case err != nil:
			writeError(w, http.StatusBadGateway, "pair_failed", err.Error())
		default:
			writeJSON(w, http.StatusAccepted, st)
		}
	}))
	mux.HandleFunc("GET /devices/pair/{id}", local(func(w http.ResponseWriter, r *http.Request) {
		st, ok := m.joinStatus(r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", "no pairing in progress by that id (a Router restart forgets them)")
			return
		}
		writeJSON(w, http.StatusOK, st)
	}))
	mux.HandleFunc("POST /devices/introduce", admitted(m.acceptIntroduction))
	mux.HandleFunc("POST /devices/{fp}/introduce", local(m.introduceTo))
	mux.HandleFunc("PUT /devices/{fp}/grant", admitted(m.setGrant))
	mux.HandleFunc("DELETE /devices/{fp}", admitted(m.removeDevice))
	mux.HandleFunc("POST /devices/{fp}/remove-everywhere", local(m.removeEverywhere))
	return mux
}

// writePairRequestError is writeDataError for a pairing request: an
// unknown one is pair_request_not_found (forgotten 10 minutes after its
// answer), not a device.
func writePairRequestError(w http.ResponseWriter, err error) {
	if errors.Is(err, dataclient.ErrNotFound) {
		writeError(w, http.StatusNotFound, "pair_request_not_found", "no such pairing request (answered ones are forgotten after 10 minutes)")
		return
	}
	writeDeviceDataError(w, err)
}

// relayedLevel is the effective level of the device a relayed request
// came from (the Mesh Service's X-Mads-Mesh-Level), and whether the
// request was relayed at all.
func relayedLevel(r *http.Request) (contracts.GrantLevel, bool) {
	if _, relayed := dataclient.MeshRelay(r.Context()); !relayed {
		return contracts.GrantNone, false
	}
	l := contracts.GrantLevel(r.Header.Get(contracts.MeshLevelHeader))
	if !l.Valid() {
		l = contracts.GrantNone
	}
	return l, true
}

// checkTarget checks a mesh address another device chose for this host to
// dial, against this host's own tailnet (its Mesh Service's
// mesh.listening). With the tailnet required (the live setting,
// requireTailnet, not what the Mesh Service started with), only a MagicDNS name: a
// Tailscale IPv4 address is also carrier-grade NAT space, and the Router
// can't ask tailscaled whose an address is (the Mesh Service can, for a
// joiner).
func (m *Manager) checkTarget(ctx context.Context, meshURL string) error {
	l, ok, err := m.selfMesh(ctx)
	if err != nil || !ok {
		return errors.New("this host's Mesh Service has never said where it listens")
	}
	if err := paircode.CheckTarget(meshURL, paircode.TargetPolicy{TailnetSuffix: paircode.TailnetSuffixOf(l.DNSName)}); err != nil {
		return err
	}
	if _, isIP := paircode.TargetIP(meshURL); isIP {
		if required, _ := m.requireTailnet(ctx, l, true); !required {
			return nil
		}
		return errors.New("give the device's MagicDNS name, not an address")
	}
	return nil
}

// viewOnlyMsg refuses a remote change to a device this host only views.
const viewOnlyMsg = "this host only views that device (to show its sessions); it was never paired to be let in here, so only this host changes or removes it"

// installDefaultCeilings are the ceilings every new grant starts with. D9
// plans install-wide defaults; until they exist, none.
var installDefaultCeilings = contracts.PeerCeilings{}

// callerRow is the row of the device a relayed request came from (its
// full fingerprint, stamped by the Mesh Service).
func (m *Manager) callerRow(ctx context.Context, w http.ResponseWriter) (contracts.Peer, bool) {
	fp, ok := dataclient.MeshDevice(ctx)
	if ok {
		if peers, err := m.data.Peers(ctx); err == nil {
			for _, p := range peers {
				if p.Fingerprint == fp {
					return p, true
				}
			}
		} else {
			writeDeviceDataError(w, err)
			return contracts.Peer{}, false
		}
	}
	writeError(w, http.StatusForbidden, "no_access", "this device isn't paired here")
	return contracts.Peer{}, false
}

// rankOf orders grant levels, none lowest.
func rankOf(l contracts.GrantLevel) int { return slices.Index(contracts.GrantLevels, l) + 1 }

// parseLevel reads a level as the UI sends it: "none" (or "") for none.
func parseLevel(s string) (contracts.GrantLevel, bool) {
	if s == "none" || s == "" {
		return contracts.GrantNone, true
	}
	l := contracts.GrantLevel(s)
	return l, l.Valid()
}

// rowByFingerprint is the Data Service's current row for a fingerprint
// path segment.
func (m *Manager) rowByFingerprint(ctx context.Context, w http.ResponseWriter, raw string) (contracts.Peer, bool) {
	fp, err := devicekey.ParseFingerprint(raw)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "bad_fingerprint", err.Error())
		return contracts.Peer{}, false
	}
	peers, err := m.data.Peers(ctx)
	if err != nil {
		writeDeviceDataError(w, err)
		return contracts.Peer{}, false
	}
	for _, p := range peers {
		if p.Fingerprint == string(fp) {
			return p, true
		}
	}
	writeError(w, http.StatusNotFound, "device_not_found", "no device "+fp.Short()+" is paired here")
	return contracts.Peer{}, false
}

// --- The list ---------------------------------------------------------------------

func (m *Manager) listDevices(w http.ResponseWriter, r *http.Request) {
	_, relayed := relayedLevel(r)
	// Fresh rows: a write a moment ago (an approval, an introduction) shows
	// at once, and a relayed admin reads what's stored, not a cache.
	if err := m.Reload(r.Context()); err != nil {
		writeDeviceDataError(w, err)
		return
	}
	lastUsed := map[string]time.Time{}
	if uses, err := m.data.DeviceLastUsed(r.Context()); err == nil {
		for _, u := range uses {
			i := strings.LastIndex(u.Actor, "/")
			if i < 0 {
				continue
			}
			short := u.Actor[i+1:]
			if u.At.After(lastUsed[short]) {
				lastUsed[short] = u.At
			}
		}
	}
	m.mu.Lock()
	out := make([]contracts.DeviceStatus, 0, len(m.all))
	for id, p := range m.all {
		var s contracts.PeerStatus
		switch {
		case m.peers[id] != nil:
			s = m.peers[id].status()
		case m.paired[id] != nil:
			st := m.paired[id]
			s = contracts.PeerStatus{Peer: p, Reachability: st.reach, Reason: st.reason, RTTMillis: st.rtt.Milliseconds()}
			if s.Reachability == "" {
				s.Reachability = contracts.PeerUnknown
			}
			if !st.checkedAt.IsZero() {
				t := st.checkedAt
				s.CheckedAt = &t
			}
		default:
			s = contracts.PeerStatus{Peer: p}
		}
		if relayed {
			// Another device never reads this host's dial errors (its
			// addresses, its network): just the state.
			s.Reason = ""
		}
		d := contracts.DeviceStatus{PeerStatus: m.overlayLocked(s)}
		if p.IsPaired() {
			if at, ok := lastUsed[devicekey.Fingerprint(p.Fingerprint).Short()]; ok {
				t := at
				d.LastUsedAt = &t
			}
		}
		out = append(out, d)
	}
	m.mu.Unlock()
	slices.SortFunc(out, func(a, b contracts.DeviceStatus) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, out)
}

func (m *Manager) deviceActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var fp string
	if d := q.Get("device"); d != "" {
		f, err := devicekey.ParseFingerprint(d)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "bad_fingerprint", err.Error())
			return
		}
		fp = string(f)
	}
	var before int64
	var limit int
	if raw := q.Get("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "before must be a non-negative event seq")
			return
		}
		before = n
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 500 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be 0 to 500")
			return
		}
		limit = n
	}
	if limit == 0 {
		limit = 100
	}
	evs, err := m.data.DeviceActivity(r.Context(), fp, before, limit)
	if err != nil {
		writeDeviceDataError(w, err)
		return
	}
	if evs == nil {
		evs = []contracts.Envelope{}
	}
	writeJSON(w, http.StatusOK, evs)
}

// --- Levels and removal (also an admin device's, over the mesh) ----------------

// GrantRequest is PUT /devices/{fp}/grant. Level is "none", view, chat,
// control or admin. Ceilings, when given, replace the device's; over the
// mesh they can't be given (they're set on the host).
type GrantRequest struct {
	Level    string                  `json:"level"`
	Ceilings *contracts.PeerCeilings `json:"ceilings,omitempty"`
}

func (m *Manager) setGrant(w http.ResponseWriter, r *http.Request) {
	var req GrantRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	level, ok := parseLevel(req.Level)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "bad_level", "level must be none, view, chat, control or admin")
		return
	}
	if c := req.Ceilings; c != nil {
		// What Settings offers (D4): C1 allowed or refused; held is
		// deferred. The contract still allows held, so a stored row may
		// carry it: the Mesh Service refuses it as held_unsupported.
		switch c.FullTrustChat {
		case "", contracts.FullTrustChatAllowed, contracts.FullTrustChatRefused:
		default:
			writeError(w, http.StatusUnprocessableEntity, "bad_ceiling", "full_trust_chat must be allowed or refused")
			return
		}
		if c.MaxTrust != "" && !c.MaxTrust.Valid() {
			writeError(w, http.StatusUnprocessableEntity, "bad_ceiling", "max_trust must be one of: "+contracts.SessionTrustChoices())
			return
		}
	}
	cur, ok := m.rowByFingerprint(r.Context(), w, r.PathValue("fp"))
	if !ok {
		return
	}
	var lend *contracts.PeerCeilings // the caller's own limits, when relayed
	if mine, relayed := relayedLevel(r); relayed {
		caller, ok := m.callerRow(r.Context(), w)
		if !ok {
			return
		}
		switch {
		case !cur.PairedForAccess():
			writeError(w, http.StatusForbidden, "view_only", viewOnlyMsg)
			return
		case req.Ceilings != nil:
			writeError(w, http.StatusForbidden, "local_only", "a device's limits are set on this host, not remotely")
			return
		case level == contracts.GrantAdmin && cur.Level() != contracts.GrantAdmin:
			writeError(w, http.StatusForbidden, "admin_host_only", "admin is granted only on this host itself (Q2)")
			return
		case rankOf(level) > rankOf(cur.Level()) && rankOf(level) > rankOf(mine):
			writeError(w, http.StatusForbidden, "above_own_level", "a device can't give another more than it has here ("+levelWord(mine)+")")
			return
		}
		if caller.Grant != nil {
			c := caller.Grant.Ceilings
			lend = &c
		}
	}
	var out contracts.Peer
	err := m.editPeer(r.Context(), cur.ID, func(p *contracts.Peer) (bool, error) {
		if p.Fingerprint != cur.Fingerprint {
			return false, errNotEditable
		}
		raised := rankOf(level) > rankOf(p.Level())
		if p.Grant == nil && p.PairedForAccess() {
			p.Grant = &contracts.PeerGrant{} // paired at none before grants carried levels
		}
		switch {
		case p.Grant != nil:
			// None keeps the grant and its limits, for when it's raised
			// again.
			p.Grant.Level = level
		case level != contracts.GrantNone || req.Ceilings != nil:
			p.Grant = &contracts.PeerGrant{Level: level}
		}
		if req.Ceilings != nil && p.Grant != nil {
			p.Grant.Ceilings = *req.Ceilings
		}
		// Raised remotely: at least the raising device's own limits.
		if lend != nil && raised {
			p.Grant.Ceilings = contracts.TighterCeilings(p.Grant.Ceilings, *lend)
		}
		out = *p
		return false, nil
	})
	switch {
	case errors.Is(err, errNotEditable):
		writeError(w, http.StatusConflict, "conflict", "the device was re-paired meanwhile; reload")
		return
	case err != nil:
		writeDeviceDataError(w, err)
		return
	}
	_ = m.Reload(r.Context())
	writeJSON(w, http.StatusOK, out)
}

func (m *Manager) removeDevice(w http.ResponseWriter, r *http.Request) {
	fp, err := devicekey.ParseFingerprint(r.PathValue("fp"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "bad_fingerprint", err.Error())
		return
	}
	peers, err := m.data.Peers(r.Context())
	if err != nil {
		writeDeviceDataError(w, err)
		return
	}
	var cur *contracts.Peer
	for i := range peers {
		if peers[i].Fingerprint == string(fp) {
			cur = &peers[i]
		}
	}
	also := wantIntroduced(r.URL.Query().Get("also_introduced"))
	if cur == nil && !also {
		writeError(w, http.StatusNotFound, "device_not_found", "no device "+fp.Short()+" is paired here")
		return
	}
	if _, relayed := relayedLevel(r); relayed && cur != nil && !cur.PairedForAccess() {
		writeError(w, http.StatusForbidden, "view_only", viewOnlyMsg)
		return
	}
	intro := introducedBy(peers, string(fp))
	out := DeviceRemoval{IntroducedByThis: hostsOf(intro), AlsoRemoved: []PairHost{}, Absent: cur == nil}
	if cur != nil {
		if err := m.deleteRow(r.Context(), *cur); err != nil {
			writeDeviceDataError(w, err)
			return
		}
	}
	// The devices it introduced go too when asked, even once it's gone
	// (a retry after the device itself was removed).
	if also {
		out.AlsoRemoved = m.removeRows(r.Context(), intro)
	}
	writeJSON(w, http.StatusOK, out)
}

// DeviceRemoval is DELETE /devices/{fp}: the devices this one introduced
// here (a compromised admin's introductions outlive it unless removed
// too), and those removed with it (?also_introduced=1).
type DeviceRemoval struct {
	IntroducedByThis []PairHost `json:"introduced_by_this"`
	AlsoRemoved      []PairHost `json:"also_removed"`
	// Absent: the device itself wasn't paired here (already removed); with
	// also_introduced, its introductions were still looked for.
	Absent bool `json:"absent,omitempty"`
}

func wantIntroduced(v string) bool { return v == "1" || v == "true" }

// introducedBy is every row the device fp introduced here (pairing.how
// introduction, paired_by its device actor, which ends in its short
// fingerprint).
func introducedBy(rows []contracts.Peer, fp string) []contracts.Peer {
	suffix := "/" + devicekey.Fingerprint(fp).Short()
	var out []contracts.Peer
	for _, p := range rows {
		if p.Fingerprint != fp && p.Pairing != nil && p.Pairing.How == contracts.PairIntroduction &&
			strings.HasPrefix(p.Pairing.PairedBy, "device:") && strings.HasSuffix(p.Pairing.PairedBy, suffix) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b contracts.Peer) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func hostsOf(rows []contracts.Peer) []PairHost {
	out := make([]PairHost, 0, len(rows))
	for _, p := range rows {
		out = append(out, PairHost{Name: p.Name, Fingerprint: p.Fingerprint})
	}
	return out
}

// removeRows deletes rows, reporting those it did.
func (m *Manager) removeRows(ctx context.Context, rows []contracts.Peer) []PairHost {
	out := []PairHost{}
	for _, p := range rows {
		if err := m.deleteRow(ctx, p); err != nil {
			log.Printf("mesh: remove %s (introduced by a removed device): %v", p.Name, err)
			continue
		}
		out = append(out, PairHost{Name: p.Name, Fingerprint: p.Fingerprint})
	}
	return out
}

// deleteRow removes a device's row, both sides: it can't use mads here
// from its next request, and its sessions stop showing here.
func (m *Manager) deleteRow(ctx context.Context, cur contracts.Peer) error {
	err := m.editPeer(ctx, cur.ID, func(p *contracts.Peer) (bool, error) {
		if p.Fingerprint != cur.Fingerprint {
			return false, errNotEditable
		}
		return true, nil
	})
	if errors.Is(err, dataclient.ErrNotFound) {
		err = nil
	}
	if err == nil {
		_ = m.Reload(ctx)
	}
	return err
}

// --- Introductions (D5) ---------------------------------------------------------

// Introduction is POST /devices/introduce, sent by an introducer's Router
// to a host it administers: a device it has paired, to pin here.
type Introduction struct {
	Fingerprint   string `json:"fingerprint"`
	Name          string `json:"name"`
	Platform      string `json:"platform,omitempty"`
	HostsSessions *bool  `json:"hosts_sessions,omitempty"`
	MeshURL       string `json:"mesh_url,omitempty"`
	// Level is "none", view, chat or control: at most the introducer's
	// own here, never admin.
	Level string `json:"level"`
}

func (m *Manager) acceptIntroduction(w http.ResponseWriter, r *http.Request) {
	mine, relayed := relayedLevel(r)
	if !relayed {
		writeError(w, http.StatusForbidden, "relayed_only",
			"an introduction comes from a device administering this host; here, pair the device or `mads pair --accept` it")
		return
	}
	var in Introduction
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	level, ok := parseLevel(in.Level)
	switch {
	case !ok:
		writeError(w, http.StatusUnprocessableEntity, "bad_level", "level must be none, view, chat or control")
		return
	case level == contracts.GrantAdmin:
		writeError(w, http.StatusForbidden, "admin_host_only", "admin is never granted by introduction (Q2)")
		return
	case rankOf(level) > rankOf(mine):
		writeError(w, http.StatusForbidden, "above_own_level", "a device can introduce another at most at its own level here ("+levelWord(mine)+")")
		return
	}
	fp, err := devicekey.ParseFingerprint(in.Fingerprint)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "bad_fingerprint", err.Error())
		return
	}
	if m.opts.Identity != nil && fp == m.opts.Identity.Fingerprint() {
		writeError(w, http.StatusUnprocessableEntity, "pair_self", "that's this host's own key")
		return
	}
	no := false
	row := contracts.Peer{
		Name:        pinName(in.Name, fp),
		Viewed:      &no,
		Fingerprint: string(fp),
		Pairing:     &contracts.PeerPairing{How: contracts.PairIntroduction},
	}
	if in.MeshURL != "" {
		// This host will dial it: only a device on this tailnet (at any
		// port).
		if err := m.checkTarget(r.Context(), in.MeshURL); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "bad_mesh_url", "the introduced device's mesh address must be a device on this host's tailnet (with the tailnet required, its MagicDNS name)")
			return
		}
		row.MeshURL = strings.TrimSuffix(in.MeshURL, "/")
	}
	plat := strings.ToLower(in.Platform)
	if !contracts.ValidPlatform(plat) {
		plat = ""
	}
	if plat != "" || in.HostsSessions != nil {
		row.Remote = &contracts.PeerRemote{Platform: plat, HostsSessions: in.HostsSessions}
	}
	// The introducer's own limits come with it (and the install-wide
	// defaults, when there are any; none exist yet): a capped admin can't
	// introduce a key of its own uncapped.
	caller, ok := m.callerRow(r.Context(), w)
	if !ok {
		return
	}
	var ceil contracts.PeerCeilings
	if caller.Grant != nil {
		ceil = contracts.TighterCeilings(caller.Grant.Ceilings, installDefaultCeilings)
	}
	row.Grant = &contracts.PeerGrant{Level: level, Ceilings: ceil}
	base := row.Name
	for i := 2; ; i++ {
		err = m.data.CreatePeer(r.Context(), &row)
		var v *contracts.ValidationError
		if !errors.As(err, &v) {
			break
		}
		if slices.ContainsFunc(v.Errors, func(e contracts.FieldError) bool { return e.Field == "fingerprint" }) {
			writeError(w, http.StatusConflict, "already_paired", "that device is already paired here")
			return
		}
		if i > 9 || len(v.Errors) != 1 || v.Errors[0].Field != "name" {
			break
		}
		row.ID, row.Name = "", base+"-"+strconv.Itoa(i)
	}
	if err != nil {
		writeDeviceDataError(w, err)
		return
	}
	_ = m.Reload(r.Context())
	writeJSON(w, http.StatusCreated, row)
}

// IntroduceRequest is POST /devices/{fp}/introduce: introduce the device
// with that fingerprint (paired here) to host (a name or fingerprint of a
// device paired here that this device administers).
type IntroduceRequest struct {
	Host  string `json:"host"`
	Level string `json:"level"`
}

func (m *Manager) introduceTo(w http.ResponseWriter, r *http.Request) {
	var req IntroduceRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	level, ok := parseLevel(req.Level)
	if !ok || level == contracts.GrantAdmin {
		writeError(w, http.StatusUnprocessableEntity, "bad_level", "level must be none, view, chat or control (never admin)")
		return
	}
	dev, ok := m.rowByFingerprint(r.Context(), w, r.PathValue("fp"))
	if !ok {
		return
	}
	host, ok := m.hostRow(r.Context(), w, req.Host)
	if !ok {
		return
	}
	if host.ID == dev.ID {
		writeError(w, http.StatusUnprocessableEntity, "bad_request", "a device can't be introduced to itself")
		return
	}
	in := Introduction{Fingerprint: dev.Fingerprint, Name: dev.Name, MeshURL: dev.MeshURL, Level: levelWord(level)}
	if dev.Remote != nil {
		in.Platform, in.HostsSessions = dev.Remote.Platform, dev.Remote.HostsSessions
	}
	status, body, err := m.peerCall(r.Context(), host, http.MethodPost, "/devices/introduce", in)
	if err != nil {
		state, reason := dialFailure(host, err)
		writeError(w, http.StatusBadGateway, "host_"+string(state), host.Name+": "+reason)
		return
	}
	if status != http.StatusCreated {
		code, msg := errorBody(body)
		code, msg = paircode.CleanText(code), paircode.CleanText(msg)
		if status == http.StatusForbidden && code == "no_access" {
			code = "not_admin"
			msg = host.Name + " doesn't let this device administer it (" + msg + ")"
		}
		out := status
		if out < 400 || out >= 500 {
			out = http.StatusBadGateway
		}
		writeError(w, out, firstNonEmpty(code, "introduce_failed"), firstNonEmpty(msg, host.Name+" answered "+strconv.Itoa(status)))
		return
	}
	var row contracts.Peer
	_ = json.Unmarshal(body, &row)
	writeJSON(w, http.StatusCreated, map[string]any{
		"host":   PairHost{Name: host.Name, Fingerprint: host.Fingerprint},
		"device": PairHost{Name: dev.Name, Fingerprint: dev.Fingerprint},
		"level":  levelWord(level),
		"as":     row.Name,
	})
}

// hostRow is the paired row named by key (a name or a fingerprint) that
// this device can dial.
func (m *Manager) hostRow(ctx context.Context, w http.ResponseWriter, key string) (contracts.Peer, bool) {
	peers, err := m.data.Peers(ctx)
	if err != nil {
		writeDeviceDataError(w, err)
		return contracts.Peer{}, false
	}
	for _, p := range peers {
		match := p.Name == key
		if fp, err := devicekey.ParseFingerprint(key); err == nil && p.Fingerprint == string(fp) {
			match = true
		}
		if match {
			if !p.IsPaired() || p.MeshURL == "" {
				writeError(w, http.StatusUnprocessableEntity, "not_a_host", p.Name+" isn't a host this device can reach")
				return contracts.Peer{}, false
			}
			return p, true
		}
	}
	writeError(w, http.StatusNotFound, "device_not_found", "no device "+key+" is paired here")
	return contracts.Peer{}, false
}

// peerCall sends a JSON request to a peer's Mesh Service and reads the
// answer (bounded).
func (m *Manager) peerCall(ctx context.Context, p contracts.Peer, method, path string, in any) (int, []byte, error) {
	return m.peerCallWithin(ctx, 8*time.Second, p, method, path, in)
}

// peerCallWithin is peerCall bounded by d.
func (m *Manager) peerCallWithin(ctx context.Context, d time.Duration, p contracts.Peer, method, path string, in any) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	}
	req, client, err := m.peerRequest(ctx, method, p, path, body)
	if err != nil {
		return 0, nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, nil
}

func errorBody(raw []byte) (code, msg string) {
	var e struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.Code, e.Error
}

// --- Remove everywhere (D8) ------------------------------------------------------

// RemoveResult is POST /devices/{fp}/remove-everywhere.
type RemoveResult struct {
	Device PairHost `json:"device"`
	// Here is "removed", or "absent" (it wasn't paired here: a retry).
	Here string `json:"here"`
	// Hosts are the hosts this device administers (as each last said), and
	// how removing it there went.
	Hosts []HostRemoval `json:"hosts"`
	// CannotManage are the other hosts this device knows that don't let it
	// administer them: remove the device there by hand. Hosts this device
	// has never seen can't be checked at all.
	CannotManage []KnownHost `json:"cannot_manage"`
	// IntroducedByThis are the devices the removed one introduced here;
	// AlsoRemoved those removed with it (also_introduced).
	IntroducedByThis []PairHost `json:"introduced_by_this"`
	AlsoRemoved      []PairHost `json:"also_removed"`
}

// HostRemoval is one host's result: removed, offline (not reached; retry),
// failed (it answered with an error and may still have it paired),
// not_admin (it no longer lets this device administer it), or skipped (a
// paired device that doesn't host mads: nothing to remove there).
type HostRemoval struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Result      string `json:"result"`
	Error       string `json:"error,omitempty"`
	// IntroducedByThis are the devices it had introduced there, as that
	// host answered; AlsoRemoved counts those removed with it
	// (also_introduced).
	IntroducedByThis []PairHost `json:"introduced_by_this,omitempty"`
	AlsoRemoved      int        `json:"also_removed,omitempty"`
}

// KnownHost is a host this device can't manage, with what it lets this
// device do there ("" when never reached).
type KnownHost struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Level       string `json:"level,omitempty"`
}

func (m *Manager) removeEverywhere(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// Hosts, when given, retries only these (names or fingerprints),
		// and doesn't need the device to be paired here any more.
		Hosts []string `json:"hosts"`
		// AlsoIntroduced also removes the devices it introduced, here and
		// on each host (a compromised admin's introductions outlive it).
		AlsoIntroduced bool `json:"also_introduced"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
			return
		}
	}
	fp, err := devicekey.ParseFingerprint(r.PathValue("fp"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "bad_fingerprint", err.Error())
		return
	}
	if err := m.Reload(r.Context()); err != nil {
		writeDeviceDataError(w, err)
		return
	}
	m.mu.Lock()
	var target *contracts.Peer
	var hosts []contracts.Peer
	var skipped []HostRemoval
	for _, p := range m.all {
		if p.Fingerprint == string(fp) {
			t := p
			target = &t
			continue
		}
		if p.IsPaired() && p.MeshURL != "" && p.Remote != nil && p.Remote.HostsSessions != nil && !*p.Remote.HostsSessions {
			skipped = append(skipped, HostRemoval{Name: p.Name, Fingerprint: p.Fingerprint, Result: "skipped", Error: "it doesn't host mads, so it has no devices to remove"})
			continue
		}
		if p.IsPaired() && p.MeshURL != "" && (p.Remote == nil || p.Remote.HostsSessions == nil || *p.Remote.HostsSessions) {
			if rs, ok := m.remote[p.ID]; ok {
				rr := rs.remote
				p.Remote = &rr
			}
			hosts = append(hosts, p)
		}
	}
	all := make([]contracts.Peer, 0, len(m.all))
	for _, p := range m.all {
		all = append(all, p)
	}
	m.mu.Unlock()
	intro := introducedBy(all, string(fp))
	res := RemoveResult{Device: PairHost{Fingerprint: string(fp)}, Hosts: []HostRemoval{}, CannotManage: []KnownHost{},
		IntroducedByThis: hostsOf(intro), AlsoRemoved: []PairHost{}}
	switch {
	case target != nil && len(req.Hosts) == 0:
		res.Device.Name = target.Name
		if err := m.deleteRow(r.Context(), *target); err != nil {
			writeDeviceDataError(w, err)
			return
		}
		res.Here = "removed"
		m.mu.Lock()
		m.removed[string(fp)] = target.Name
		m.mu.Unlock()
		if req.AlsoIntroduced {
			res.AlsoRemoved = m.removeRows(r.Context(), intro)
		}
	case target != nil:
		res.Device.Name = target.Name
		res.Here = "kept"
	case len(req.Hosts) == 0 && !req.AlsoIntroduced:
		writeError(w, http.StatusNotFound, "device_not_found", "no device "+fp.Short()+" is paired here")
		return
	default:
		res.Here = "absent"
		m.mu.Lock()
		res.Device.Name = m.removed[string(fp)] // a retry keeps the name
		m.mu.Unlock()
		if req.AlsoIntroduced {
			res.AlsoRemoved = m.removeRows(r.Context(), intro)
		}
	}
	slices.SortFunc(hosts, func(a, b contracts.Peer) int { return strings.Compare(a.Name, b.Name) })
	wanted := func(p contracts.Peer) bool {
		if len(req.Hosts) == 0 {
			return true
		}
		for _, k := range req.Hosts {
			if k == p.Name || k == p.Fingerprint {
				return true
			}
			if f, err := devicekey.ParseFingerprint(k); err == nil && string(f) == p.Fingerprint {
				return true
			}
		}
		return false
	}
	var admins []contracts.Peer
	for _, h := range hosts {
		if !wanted(h) {
			continue
		}
		if h.Remote != nil && h.Remote.AsOf != nil && h.Remote.Level == contracts.GrantAdmin {
			admins = append(admins, h)
			continue
		}
		k := KnownHost{Name: h.Name, Fingerprint: h.Fingerprint}
		if h.Remote != nil && h.Remote.AsOf != nil {
			k.Level = levelWord(h.Remote.Level)
		}
		res.CannotManage = append(res.CannotManage, k)
	}
	// Every host at once, each bounded, and all of it inside the UI's own
	// 10 s budget: a host that doesn't answer in time reads offline (retry).
	rctx, cancel := context.WithTimeout(r.Context(), removeAllWithin)
	defer cancel()
	results := make([]HostRemoval, len(admins))
	var wg sync.WaitGroup
	for i, h := range admins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = m.removeOn(rctx, h, fp, req.AlsoIntroduced)
		}()
	}
	wg.Wait()
	res.Hosts = append(res.Hosts, results...)
	if len(req.Hosts) == 0 {
		slices.SortFunc(skipped, func(a, b HostRemoval) int { return strings.Compare(a.Name, b.Name) })
		res.Hosts = append(res.Hosts, skipped...)
	}
	writeJSON(w, http.StatusOK, res)
}

// Remove everywhere's budgets: each host, and the whole call (under the
// UI's 10 s). Vars so a test can shrink them.
var (
	removeHostWithin = 6 * time.Second
	removeAllWithin  = 8500 * time.Millisecond
)

// removeOn asks host to remove the device fp.
func (m *Manager) removeOn(ctx context.Context, host contracts.Peer, fp devicekey.Fingerprint, alsoIntroduced bool) HostRemoval {
	out := HostRemoval{Name: host.Name, Fingerprint: host.Fingerprint}
	path := "/devices/" + url.PathEscape(string(fp))
	if alsoIntroduced {
		path += "?also_introduced=1"
	}
	status, body, err := m.peerCallWithin(ctx, removeHostWithin, host, http.MethodDelete, path, nil)
	code, msg := errorBody(body)
	code, msg = paircode.CleanText(code), paircode.CleanText(msg)
	switch {
	case err != nil:
		_, reason := dialFailure(host, err)
		out.Result, out.Error = "offline", reason
	case status == http.StatusNoContent || status == http.StatusOK:
		out.Result = "removed"
		var d DeviceRemoval
		if json.Unmarshal(body, &d) == nil {
			out.AlsoRemoved = len(d.AlsoRemoved)
			for _, h := range d.IntroducedByThis {
				out.IntroducedByThis = append(out.IntroducedByThis, PairHost{Name: paircode.CleanText(h.Name), Fingerprint: h.Fingerprint})
			}
		}
	case status == http.StatusNotFound && code == "device_not_found":
		out.Result = "removed" // not paired there (any more)
	case status == http.StatusForbidden && code == "no_access":
		out.Result, out.Error = "not_admin", firstNonEmpty(msg, host.Name+" no longer lets this device administer it")
	default:
		out.Result, out.Error = "failed", firstNonEmpty(msg, host.Name+" answered "+strconv.Itoa(status))
	}
	return out
}
