package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/pkg/paircode"
)

// This device's mesh network (docs/MESH-NETWORK-DESIGN.md U1, U2): what
// Set up mesh and the Mesh network card show (GET /devices/self's mesh),
// and where the user chooses it (PUT /devices/settings). Its own networks
// are read here (a local read; the Tailscale helper names its network),
// merged with what the Mesh Service last reported listening on.

// SelfDevice is GET /devices/self: this device as other devices see it.
type SelfDevice struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Short       string `json:"short,omitempty"`
	Platform    string `json:"platform"`
	// MeshURL is where other devices reach this one first (its Mesh
	// Service's first address). Empty: it listens nowhere (no Mesh Service
	// has run here, or no mesh network is chosen yet).
	MeshURL       string `json:"mesh_url,omitempty"`
	HostsSessions bool   `json:"hosts_sessions"`
	// RequireTailnet is the Tailscale identity check (mesh.tailnet_check),
	// kept for one release for the earlier UI; RequireTailnetLocked: the
	// Mesh Service was started with an explicit -require-tailnet.
	RequireTailnet       *bool `json:"require_tailnet,omitempty"`
	RequireTailnetLocked bool  `json:"require_tailnet_locked,omitempty"`
	// Mesh is this device's mesh network.
	Mesh SelfMesh `json:"mesh"`
}

// SelfMesh is this device's mesh network, as Set up mesh and the Mesh
// network card show it.
type SelfMesh struct {
	// Configured: a mesh network is chosen (else Settings shows the Set up
	// mesh empty state).
	Configured bool `json:"configured"`
	// Networks are the networks this device is on now, each with what it
	// means, plus a chosen one it isn't on now (available false). Sorted:
	// Tailscale, other VPNs, LANs, public.
	Networks []NetworkView `json:"networks"`
	Port     int           `json:"port"`
	// TailnetCheck is the Tailscale identity check as set;
	// TailnetCheckStatus as the Mesh Service applies it: on, off, or
	// unavailable (on, and tailscaled can't be asked: the Tailscale
	// network refuses everyone). TailnetCheckLocked: -require-tailnet.
	TailnetCheck       bool   `json:"tailnet_check"`
	TailnetCheckStatus string `json:"tailnet_check_status,omitempty"`
	TailnetCheckLocked bool   `json:"tailnet_check_locked,omitempty"`
	// Public is "Also reachable from the internet".
	Public bool `json:"public"`
	// DiscoverableUntil is the end of the setup window, while it's open.
	DiscoverableUntil *time.Time `json:"discoverable_until,omitempty"`
	// WindowClosedReason says why the last window closed early when it
	// wasn't this device's user: "abandoned_pairings" (several pairings were
	// started and never finished: someone may be trying check codes).
	WindowClosedReason string `json:"window_closed_reason,omitempty"`
	// PairingStarted is the devices that started pairing in the current
	// (or last) window, newest first: "a device started pairing", shown
	// even when no request follows (R1).
	PairingStarted []PairingStarted `json:"pairing_started"`
	// Listening is where the Mesh Service listens now, as it last
	// reported (shown only under troubleshooting).
	Listening []contracts.NetAddress `json:"listening"`
	// ServiceSeen: this device's Mesh Service has reported at all.
	ServiceSeen bool `json:"service_seen"`
	// ServiceAnswering: it answers on an address it reported, checked
	// now (T5); false when it reported addresses and none answers (it has
	// stopped), absent when it reported none.
	ServiceAnswering *bool `json:"service_answering,omitempty"`
	// Tailscale is tailscaled's state here (absent: not installed).
	Tailscale *TailscaleView `json:"tailscale,omitempty"`
}

// PairingStarted is one device.pair_started.
type PairingStarted struct {
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	Short       string    `json:"short"`
	Remote      string    `json:"remote,omitempty"`
	Network     string    `json:"network,omitempty"`
	At          time.Time `json:"at"`
}

// NetworkView is one network in SelfMesh. Meaning, WhoCanKnock and Risk
// are keys the UI maps to its words (docs/MESH-NETWORK-DESIGN.md, the
// routes table).
type NetworkView struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Iface string `json:"iface,omitempty"`
	// Meaning: vpn_anywhere | local_only | internet.
	Meaning string `json:"meaning"`
	// WhoCanKnock: tailnet_only | vpn_members | anyone_on_network |
	// anyone_on_shared_network | anyone_on_internet.
	WhoCanKnock string `json:"who_can_knock"`
	// Risk: low | medium | high.
	Risk        string `json:"risk"`
	Recommended bool   `json:"recommended,omitempty"`
	Shared      bool   `json:"shared,omitempty"`
	Wireless    bool   `json:"wireless,omitempty"`
	SSID        string `json:"ssid,omitempty"`
	// Chosen: a mesh network; Available: this device is on it now;
	// Listening: the Mesh Service listens there now; Error why not.
	Chosen    bool   `json:"chosen"`
	Available bool   `json:"available"`
	Listening bool   `json:"listening"`
	Error     string `json:"error,omitempty"`
}

// TailscaleView is tailscaled's state, for the Mesh network card's
// sign-in prompt and key-expiry note.
type TailscaleView struct {
	Present      bool   `json:"present"`
	BackendState string `json:"backend_state,omitempty"`
	// NeedsLogin: "Tailscale needs you to sign in again"; AuthURL, when
	// tailscaled has one (POST /devices/tailscale/login starts one).
	NeedsLogin bool   `json:"needs_login"`
	AuthURL    string `json:"auth_url,omitempty"`
	DNSName    string `json:"dns_name,omitempty"`
	Tailnet    string `json:"tailnet,omitempty"`
	// KeyExpiry is this device's key expiry; KeyExpiryOff: expiry is off
	// (Tailscale's advice for a machine reached remotely).
	KeyExpiry    *time.Time `json:"key_expiry,omitempty"`
	KeyExpiryOff bool       `json:"key_expiry_off"`
	// AdminURL is where key expiry is turned off.
	AdminURL string `json:"admin_url"`
	// ControlURL is the control server this node signs in with (its
	// prefs): a sign-in link (AuthURL) may only point at it, or at
	// Tailscale's own login server. Absent when unknown.
	ControlURL string `json:"control_url,omitempty"`
	// Endpoint says which tailscaled answered (socket, macos-standalone,
	// macos-app-store); Error why none did.
	Endpoint string `json:"endpoint,omitempty"`
	Error    string `json:"error,omitempty"`
}

// tailscaleAdminURL is Tailscale's machines page (where key expiry is
// turned off).
const tailscaleAdminURL = "https://login.tailscale.com/admin/machines"

// networkView is n with what it means.
func networkView(n meshnet.Network) NetworkView {
	v := NetworkView{ID: n.ID, Kind: string(n.Kind), Title: n.Label, Iface: n.Iface, Shared: n.Shared, Wireless: n.Wireless, SSID: n.SSID, Available: true}
	switch {
	case n.ID == meshnet.TailscaleID:
		v.Meaning, v.WhoCanKnock, v.Risk, v.Recommended = "vpn_anywhere", "tailnet_only", "low", true
	case n.Kind == meshnet.KindOverlay:
		v.Meaning, v.WhoCanKnock, v.Risk = "vpn_anywhere", "vpn_members", "low"
	case n.Kind == meshnet.KindLAN && n.Shared:
		v.Meaning, v.WhoCanKnock, v.Risk = "local_only", "anyone_on_shared_network", "high"
	case n.Kind == meshnet.KindLAN:
		v.Meaning, v.WhoCanKnock, v.Risk = "local_only", "anyone_on_network", "medium"
	default:
		v.Meaning, v.WhoCanKnock, v.Risk = "internet", "anyone_on_internet", "high"
	}
	return v
}

func (m *Manager) selfDevice(ctx context.Context) SelfDevice {
	d := SelfDevice{Name: m.opts.SelfName, Platform: m.platform(ctx)}
	if m.opts.Identity != nil {
		fp := m.opts.Identity.Fingerprint()
		d.Fingerprint, d.Short = string(fp), fp.Short()
	}
	l, have, err := m.selfMesh(ctx)
	have = have && err == nil
	if have {
		d.HostsSessions = l.HostsSessions
		if addrs := selfAddresses(l); len(addrs) > 0 {
			d.MeshURL = contracts.MeshURLOf(addrs[0].Addr)
		}
	}
	settings, serr := m.data.MeshSettings(ctx)
	sm := SelfMesh{Networks: []NetworkView{}, Listening: []contracts.NetAddress{}, ServiceSeen: have, Port: meshnet.DefaultPort}
	if serr == nil {
		sm.Configured, sm.Port, sm.Public = len(settings.Chosen()) > 0, settings.MeshPort(), settings.Public
		sm.TailnetCheck = settings.TailnetCheck()
	}
	chosen := map[string]bool{}
	for _, c := range settings.Chosen() {
		chosen[c.ID] = true
	}
	reported := map[string]contracts.ListeningNetwork{}
	if have {
		for _, n := range l.Networks {
			reported[n.ID] = n
		}
		sm.Listening = append(sm.Listening, selfAddresses(l)...)
		if len(sm.Listening) > 0 {
			alive := m.serviceAnswers(ctx, sm.Listening)
			sm.ServiceAnswering = &alive
		}
		sm.TailnetCheckStatus = l.TailnetCheckStatus
		sm.TailnetCheckLocked = l.RequireTailnetFlag
		if l.RequireTailnetFlag {
			sm.TailnetCheck = l.RequireTailnet
		}
	}
	seen := map[string]bool{}
	for _, n := range m.localNetworks() {
		v := networkView(n)
		v.Chosen = chosen[n.ID]
		if r, ok := reported[n.ID]; ok {
			v.Listening, v.Error = r.Listening, r.Error
		}
		// Listening on one of its addresses is listening on it, whatever id
		// the Mesh Service's own detection gave it.
		for _, a := range sm.Listening {
			if ip, ok := meshnet.FromNet(a).IP(); ok && slices.Contains(n.Addrs, ip) {
				v.Listening, v.Error = true, ""
			}
		}
		if v.Listening && sm.ServiceAnswering != nil && !*sm.ServiceAnswering {
			v.Listening, v.Error = false, notAnswering
		}
		sm.Networks = append(sm.Networks, v)
		seen[n.ID] = true
	}
	for _, c := range settings.Chosen() {
		if seen[c.ID] {
			continue
		}
		v := NetworkView{ID: c.ID, Title: firstNonEmpty(c.Name, labelForID(c.ID)), Chosen: true, Error: "this device isn't on it now"}
		if r, ok := reported[c.ID]; ok {
			v.Kind, v.Title = r.Kind, firstNonEmpty(r.Label, v.Title)
		}
		n := meshnet.Network{ID: c.ID, Kind: meshnet.Kind(v.Kind), Label: v.Title}
		if c.ID == meshnet.TailscaleID {
			n.Kind = meshnet.KindOverlay
		} else if strings.HasPrefix(c.ID, "lan:") {
			n.Kind = meshnet.KindLAN
		}
		nv := networkView(n)
		nv.Chosen, nv.Available, nv.Error = true, false, v.Error
		sm.Networks = append(sm.Networks, nv)
	}
	sm.PairingStarted = []PairingStarted{}
	if w, err := m.data.Discoverable(ctx); err == nil {
		if w.Open(m.opts.Now()) {
			t := w.Until
			sm.DiscoverableUntil = &t
		} else {
			sm.WindowClosedReason = w.Reason
		}
		if !w.OpenedAt.IsZero() {
			sm.PairingStarted = m.pairingStarted(ctx, w.OpenedAt)
		}
	}
	sm.Tailscale = m.tailscaleView(ctx, l, have)
	d.Mesh = sm
	// Before a mesh network is chosen, the check reads as what it will be
	// when Tailscale is: on, unless turned off.
	rt := sm.TailnetCheck
	if !sm.Configured && !sm.TailnetCheckLocked {
		rt = serr != nil || settings.RequireTailnet == nil || *settings.RequireTailnet
	}
	d.RequireTailnet, d.RequireTailnetLocked = &rt, sm.TailnetCheckLocked
	return d
}

// pairingStarted is the device.pair_started since a window opened, newest
// first, at most 20.
func (m *Manager) pairingStarted(ctx context.Context, since time.Time) []PairingStarted {
	out := []PairingStarted{}
	evs, err := m.data.Events(ctx, 0, "", []string{string(contracts.DevicePairStarted)})
	if err != nil {
		return out
	}
	for i := len(evs) - 1; i >= 0 && len(out) < 20; i-- {
		ev := evs[i]
		if ev.Actor != "mesh" || ev.Timestamp.Before(since) {
			continue
		}
		var d contracts.DeviceDiscovery
		if json.Unmarshal(ev.Payload, &d) != nil {
			continue
		}
		out = append(out, PairingStarted{Name: paircode.CleanText(d.Name), Fingerprint: d.Fingerprint, Short: devicekey.Fingerprint(d.Fingerprint).Short(),
			Remote: d.Remote, Network: d.Network, At: ev.Timestamp})
	}
	return out
}

// notAnswering is a network the Mesh Service said it listens on that it
// doesn't answer on now.
const notAnswering = "the Mesh Service isn't answering there (is it running?)"

// noGatewayHW is why a LAN without a known router hardware address isn't
// chosen.
const noGatewayHW = "this network's router hardware address isn't known, so another network with the same addresses couldn't be told apart from it; try again in a moment (once this device has talked to its router), or use Tailscale"

func labelForID(id string) string {
	switch {
	case id == meshnet.TailscaleID:
		return "Tailscale"
	case strings.HasPrefix(id, "lan:"):
		return "Local network"
	case strings.HasPrefix(id, "vpn:"):
		return "VPN (" + strings.TrimPrefix(id, "vpn:") + ")"
	case strings.HasPrefix(id, "public:"):
		return "Public address"
	}
	return id
}

// tailscaleView is tailscaled's state: read here when this Router has the
// LocalAPI, else as the Mesh Service last reported it.
func (m *Manager) tailscaleView(ctx context.Context, l contracts.MeshListener, have bool) *TailscaleView {
	v := m.tailscaleViewRaw(ctx, l, have)
	if v != nil && v.AuthURL != "" && !httpsLink(v.AuthURL) {
		v.AuthURL = ""
	}
	return v
}

func (m *Manager) tailscaleViewRaw(ctx context.Context, l contracts.MeshListener, have bool) *TailscaleView {
	v := &TailscaleView{AdminURL: tailscaleAdminURL}
	if m.opts.Tailscale != nil {
		st, err := m.opts.Tailscale.Status(ctx, false)
		if err == nil {
			v.Present, v.BackendState, v.NeedsLogin, v.AuthURL = true, st.BackendState, st.NeedsLogin(), st.AuthURL
			if st.Self != nil {
				v.DNSName, v.KeyExpiry, v.KeyExpiryOff = st.Self.DNSName, st.Self.KeyExpiry, st.Self.KeyExpiry == nil
			}
			if st.CurrentTailnet != nil {
				v.Tailnet = st.CurrentTailnet.Name
			}
			if e, ok := m.opts.Tailscale.Endpoint(); ok {
				v.Endpoint = e.Variant
			}
			if v.NeedsLogin {
				if cu, err := m.opts.Tailscale.ControlURL(ctx); err == nil {
					v.ControlURL = cu
				}
			}
			return v
		}
		v.Error = err.Error()
	}
	if have && l.Tailscale != nil {
		t := l.Tailscale
		v.Present, v.BackendState, v.NeedsLogin, v.AuthURL = t.Present, t.BackendState, t.NeedsLogin, t.AuthURL
		v.DNSName, v.Tailnet, v.KeyExpiry, v.KeyExpiryOff, v.Endpoint = t.DNSName, t.Tailnet, t.KeyExpiry, t.KeyExpiryOff, t.Endpoint
		if t.Error != "" {
			v.Error = t.Error
		}
	}
	if !v.Present && v.Error == "" {
		return nil
	}
	return v
}

// MeshSettingsRequest is PUT /devices/settings.
type MeshSettingsRequest struct {
	// Networks is the mesh network's id (the MVP chooses exactly one), or
	// none ([]: stop listening).
	Networks *[]string `json:"networks"`
	// TailnetCheck is the Tailscale identity check (Tailscale chosen only).
	TailnetCheck *bool `json:"tailnet_check"`
	// Public is "Also reachable from the internet"; turning it on needs
	// ConfirmPublic.
	Public        *bool `json:"public"`
	ConfirmPublic bool  `json:"confirm_public"`
	// RequireTailnet is the earlier form of TailnetCheck.
	RequireTailnet *bool `json:"require_tailnet"`
}

// setMeshSettings is PUT /devices/settings: this host's own user choosing
// its mesh network, the Tailscale check, and internet exposure. The Data
// Service stores it (the mesh_settings row, device.settings_changed in the
// activity log) and the Mesh Service follows it. Answers with this device
// as GET /devices/self.
func (m *Manager) setMeshSettings(w http.ResponseWriter, r *http.Request) {
	var body MeshSettingsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if body.Networks == nil && body.TailnetCheck == nil && body.Public == nil && body.RequireTailnet == nil {
		writeError(w, http.StatusUnprocessableEntity, "bad_request", "give networks, tailnet_check or public")
		return
	}
	if body.TailnetCheck == nil {
		body.TailnetCheck = body.RequireTailnet
	}
	l, have, _ := m.selfMesh(r.Context())
	if body.TailnetCheck != nil && have && l.RequireTailnetFlag {
		writeError(w, http.StatusConflict, "require_tailnet_locked",
			"this device's Mesh Service was started with -require-tailnet, which overrides this setting: restart it without the flag to set it here")
		return
	}
	cur, err := m.data.MeshSettings(r.Context())
	if err != nil {
		writeDeviceDataError(w, err)
		return
	}
	// The earlier form alone ("Only accept devices on my tailnet").
	if body.Networks == nil && body.Public == nil && body.RequireTailnet != nil {
		if _, err := m.data.SetTailnetCheck(r.Context(), *body.RequireTailnet); err != nil {
			writeDeviceDataError(w, err)
			return
		}
		m.forgetSelf()
		writeJSON(w, http.StatusOK, m.selfDevice(r.Context()))
		return
	}
	next := cur
	now := m.opts.Now().UTC()
	if body.Networks != nil {
		ids := *body.Networks
		if len(ids) > 1 {
			writeError(w, http.StatusUnprocessableEntity, "one_network",
				"choose one mesh network: more than one isn't offered yet (docs/MESH-NETWORK-DESIGN.md, One network, not several)")
			return
		}
		local := map[string]meshnet.Network{}
		for _, n := range m.localNetworks() {
			local[n.ID] = n
		}
		next.Networks = []contracts.NetworkChoice{}
		for _, id := range ids {
			if old, ok := cur.Choice(id); ok && old.Listen {
				next.Networks = append(next.Networks, old)
				continue
			}
			n, ok := local[id]
			if !ok {
				writeError(w, http.StatusUnprocessableEntity, "unknown_network", "this device isn't on a network "+id+" now")
				return
			}
			if n.Kind == meshnet.KindPublic {
				writeError(w, http.StatusUnprocessableEntity, "public_not_a_mesh_network",
					"a public address isn't offered as the mesh network: use public (Also reachable from the internet) under Advanced options")
				return
			}
			if n.Kind == meshnet.KindLAN && n.GatewayHW == "" {
				// Without its router's hardware address another network with
				// the same addresses (a café's 192.168.1.0/24) would pass as
				// this one (round-2 R10).
				writeError(w, http.StatusUnprocessableEntity, "gateway_unknown", noGatewayHW)
				return
			}
			next.Networks = append(next.Networks, contracts.NetworkChoice{ID: id, Name: n.SSID, SSID: n.SSID, Listen: true, GatewayHW: n.GatewayHW,
				TailnetCheck: id == meshnet.TailscaleID, DecidedAt: now}) // the check is on by default
		}
	}
	if body.TailnetCheck != nil {
		found := false
		for i := range next.Networks {
			if next.Networks[i].ID == meshnet.TailscaleID {
				next.Networks[i].TailnetCheck, found = *body.TailnetCheck, true
			}
		}
		if !found && *body.TailnetCheck && body.Networks != nil {
			writeError(w, http.StatusUnprocessableEntity, "tailnet_check_needs_tailscale", "the Tailscale identity check applies only with Tailscale as the mesh network")
			return
		}
		next.RequireTailnet = body.TailnetCheck
	}
	if body.Public != nil {
		if *body.Public && !next.Public && !body.ConfirmPublic {
			writeError(w, http.StatusUnprocessableEntity, "confirm_public",
				"being reachable from the internet lets anyone who finds the port try to connect and see this device's fingerprint; confirm with confirm_public")
			return
		}
		next.Public = *body.Public
	}
	if _, err := m.data.SetMeshSettings(r.Context(), next); err != nil {
		writeDeviceDataError(w, err)
		return
	}
	m.forgetSelf()
	writeJSON(w, http.StatusOK, m.selfDevice(r.Context()))
}

// windowClosed answers a renewal of a window that has closed with the
// real reason (final pass 5): closed for abandoned pairings, past its 30
// minutes, or just closed.
func (m *Manager) windowClosed(w http.ResponseWriter, r *http.Request) {
	msg, reason := "the setup window has closed: open it again", ""
	if d, err := m.data.Discoverable(r.Context()); err == nil {
		switch {
		case d.Reason == "abandoned_pairings":
			reason = d.Reason
			msg = "the setup window was closed because several pairings were started and never finished (someone may be trying check codes): open it again to go on"
		case !d.OpenedAt.IsZero() && !m.opts.Now().Before(d.OpenedAt.Add(contracts.DiscoverableAbsoluteMax)):
			msg = "the setup window has closed (it stays open at most 30 minutes): open it again"
		}
	}
	extra := map[string]any{}
	if reason != "" {
		extra["reason"] = reason
	}
	writeErrorExtra(w, http.StatusConflict, "window_closed", msg, extra)
}

// DiscoverableRequest is POST /devices/discoverable.
type DiscoverableRequest struct {
	On bool `json:"on"`
	// Renew extends the window open now (the dialog's minute timer); a
	// window past 30 minutes since it was opened isn't renewed
	// (window_closed) and must be opened again.
	Renew bool `json:"renew,omitempty"`
}

// setDiscoverable is POST /devices/discoverable: Set up mesh / Add devices
// opening (or renewing, every minute while open) this device's setup
// window, at most ten minutes without renewal, or closing it. Local only:
// a window is never opened remotely (S6).
func (m *Manager) setDiscoverable(w http.ResponseWriter, r *http.Request) {
	var body DiscoverableRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	until := time.Time{}
	if body.On {
		until = m.opts.Now().Add(contracts.DiscoverableMax)
	} else {
		m.stopSearch()
	}
	var d contracts.Discoverable
	var err error
	if body.On && body.Renew {
		d, err = m.data.RenewDiscoverable(r.Context(), until)
	} else {
		d, err = m.data.SetDiscoverable(r.Context(), until)
	}
	var conflict *dataclient.ConflictError
	if errors.As(err, &conflict) && conflict.Code == "window_closed" {
		m.windowClosed(w, r)
		return
	}
	if err != nil {
		writeDeviceDataError(w, err)
		return
	}
	out := map[string]any{"on": d.Open(m.opts.Now())}
	if !d.Until.IsZero() {
		out["until"] = d.Until
	}
	writeJSON(w, http.StatusOK, out)
}

// tailscaleLogin is POST /devices/tailscale/login: start tailscaled's
// interactive sign-in here and answer its sign-in link (the Mesh network
// card's "Sign in to Tailscale"). Local only.
func (m *Manager) tailscaleLogin(w http.ResponseWriter, r *http.Request) {
	if m.opts.Tailscale == nil {
		writeError(w, http.StatusConflict, "no_tailscale", "this device's Router can't reach tailscaled")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	st, err := m.opts.Tailscale.Status(ctx, false)
	if err != nil {
		writeError(w, http.StatusBadGateway, "tailscale_unavailable", "tailscaled isn't answering: "+err.Error())
		return
	}
	if !st.NeedsLogin() {
		// Signed in: starting a sign-in now would only log it out (S8).
		writeError(w, http.StatusConflict, "not_needed", "Tailscale is signed in here ("+firstNonEmpty(st.BackendState, "running")+")")
		return
	}
	if st.AuthURL == "" {
		if err := m.opts.Tailscale.StartLogin(ctx); err != nil {
			writeError(w, http.StatusBadGateway, "tailscale_login_failed", "tailscaled didn't start a sign-in: "+err.Error())
			return
		}
		for st.AuthURL == "" && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(250 * time.Millisecond):
			}
			if s, err := m.opts.Tailscale.Status(ctx, false); err == nil {
				st = s
				if !s.NeedsLogin() && s.BackendState == "Running" {
					break
				}
			}
		}
	}
	if st.AuthURL != "" && !httpsLink(st.AuthURL) {
		writeError(w, http.StatusBadGateway, "unexpected_auth_url", "Tailscale gave an unexpected sign-in link: sign in from the Tailscale app instead")
		return
	}
	out := map[string]any{"auth_url": st.AuthURL, "backend_state": st.BackendState, "needs_login": st.NeedsLogin()}
	// Where a sign-in link may point (the UI opens no other).
	if cu, err := m.opts.Tailscale.ControlURL(ctx); err == nil && cu != "" {
		out["control_url"] = cu
	}
	writeJSON(w, http.StatusOK, out)
}

// httpsLink says whether a sign-in link from tailscaled is one a page may
// be handed: https, with a host and no user info. (The UI also holds it
// to Tailscale's or this node's control server.)
func httpsLink(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// --- Addresses this device's user adds (mads mesh address add) ------------------------

// AddressRequest is POST /devices/{fp}/addresses.
type AddressRequest struct {
	Addr string `json:"addr"`
}

// addAddress adds an address (source user) to a device's row: S2's
// exception, it may be private and outside this device's networks (a VPC
// peer through a route), never loopback, link-local or metadata.
func (m *Manager) addAddress(w http.ResponseWriter, r *http.Request) {
	var body AddressRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	addr := strings.TrimSpace(body.Addr)
	if err := m.policy.CheckAddress(addr); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "bad_address", err.Error())
		return
	}
	cur, ok := m.rowByFingerprint(r.Context(), w, r.PathValue("fp"))
	if !ok {
		return
	}
	var out contracts.Peer
	err := m.editPeer(r.Context(), cur.ID, func(p *contracts.Peer) (bool, error) {
		if p.Fingerprint != cur.Fingerprint {
			return false, errNotEditable
		}
		list := p.Candidates()
		for i := range list {
			if strings.EqualFold(list[i].Addr, addr) {
				list[i].Source = contracts.SourceUser
				p.Addresses = list
				out = *p
				return false, nil
			}
		}
		if len(list) >= contracts.MaxPeerAddresses {
			// Room is made by dropping the oldest host-reported address.
			drop := slices.IndexFunc(list, func(a contracts.MeshAddress) bool {
				return a.Source != contracts.SourceUser && (p.LastGood == nil || !strings.EqualFold(a.Addr, p.LastGood.Addr))
			})
			if drop < 0 {
				return false, errTooManyAddresses
			}
			list = slices.Delete(list, drop, drop+1)
		}
		c := meshnet.Candidate{Via: meshnet.DirectVia, Addr: addr}
		list = append(list, contracts.MeshAddress{NetAddress: contracts.NetAddress{Via: meshnet.DirectVia, Addr: addr, Kind: string(meshnet.InferKind(c))},
			Source: contracts.SourceUser, AddedAt: m.opts.Now().UTC()})
		p.Addresses = list
		out = *p
		return false, nil
	})
	switch {
	case errors.Is(err, errTooManyAddresses):
		writeError(w, http.StatusUnprocessableEntity, "too_many_addresses", "this device already has 8 addresses you added; remove one first")
		return
	case errors.Is(err, errNotEditable):
		writeError(w, http.StatusConflict, "conflict", "the device was re-paired meanwhile; reload")
		return
	case err != nil:
		writeDeviceDataError(w, err)
		return
	}
	m.invalidate(cur.Fingerprint, "an address was added")
	_ = m.Reload(r.Context())
	writeJSON(w, http.StatusOK, out)
}

var errTooManyAddresses = errors.New("too many addresses")

// removeAddress removes an address this device's user added.
func (m *Manager) removeAddress(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("addr")
	cur, ok := m.rowByFingerprint(r.Context(), w, r.PathValue("fp"))
	if !ok {
		return
	}
	var out contracts.Peer
	err := m.editPeer(r.Context(), cur.ID, func(p *contracts.Peer) (bool, error) {
		if p.Fingerprint != cur.Fingerprint {
			return false, errNotEditable
		}
		list := p.Candidates()
		i := slices.IndexFunc(list, func(a contracts.MeshAddress) bool { return strings.EqualFold(a.Addr, addr) })
		switch {
		case i < 0:
			return false, errAddressNotFound
		case list[i].Source != contracts.SourceUser:
			return false, errNotUserAdded
		case len(list) == 1 && p.IsViewed():
			return false, errLastAddress
		}
		p.Addresses = slices.Delete(list, i, i+1)
		if p.LastGood != nil && strings.EqualFold(p.LastGood.Addr, addr) {
			p.LastGood = nil
		}
		out = *p
		return false, nil
	})
	switch {
	case errors.Is(err, errAddressNotFound):
		writeError(w, http.StatusNotFound, "address_not_found", "that device has no address "+addr)
		return
	case errors.Is(err, errNotUserAdded):
		writeError(w, http.StatusConflict, "not_user_added", "only an address you added can be removed; the device reports the others itself")
		return
	case errors.Is(err, errLastAddress):
		writeError(w, http.StatusConflict, "last_address", "that's the only address for a device this one views")
		return
	case errors.Is(err, errNotEditable):
		writeError(w, http.StatusConflict, "conflict", "the device was re-paired meanwhile; reload")
		return
	case err != nil:
		writeDeviceDataError(w, err)
		return
	}
	m.invalidate(cur.Fingerprint, "an address was removed")
	_ = m.Reload(r.Context())
	writeJSON(w, http.StatusOK, out)
}

var (
	errAddressNotFound = errors.New("no such address")
	errNotUserAdded    = errors.New("not user added")
	errLastAddress     = errors.New("last address")
)

// redial is POST /devices/{fp}/redial: Try again now, re-racing at once.
func (m *Manager) redial(w http.ResponseWriter, r *http.Request) {
	cur, ok := m.rowByFingerprint(r.Context(), w, r.PathValue("fp"))
	if !ok {
		return
	}
	m.invalidate(cur.Fingerprint, "try again now")
	probe := m.ProbePeer(r.Context(), cur)
	m.mu.Lock()
	_, viewed := m.peers[cur.ID]
	m.mu.Unlock()
	if viewed {
		m.observe(r.Context(), cur.ID, probe)
	} else {
		m.observePaired(cur.ID, probe)
	}
	m.noteRemote(r.Context(), cur, probe)
	path, addrs := m.pathStatus(cur)
	writeJSON(w, http.StatusOK, map[string]any{"reachability": probe.State, "reason": probe.Reason, "path": path, "addresses": addrs})
}

// diagnosis refines s's state (F4, the Tailscale helpers): a device that
// stopped answering over Tailscale after its key's last reported expiry
// "probably signed out of Tailscale"; one connected with its key expiring
// within 7 days gets the early warning.
func diagnosis(s contracts.PeerStatus, now time.Time) string {
	r := s.Remote
	if r == nil || r.TailscaleKeyExpiry == nil {
		return ""
	}
	exp := *r.TailscaleKeyExpiry
	switch s.Reachability {
	case contracts.PeerConnected:
		if exp.Sub(now) <= 7*24*time.Hour {
			return contracts.DiagnosisTailscaleKeyExpiring
		}
	case contracts.PeerUnreachable:
		if !now.After(exp) {
			return ""
		}
		viaTailscale := false
		for _, a := range s.Addresses {
			if a.State == contracts.AddrNotOnNetwork {
				continue
			}
			if a.Network == meshnet.TailscaleID || isTailnetIP(a.MeshAddress) || strings.HasSuffix(strings.ToLower(meshnet.FromNet(a.NetAddress).Host()), ".ts.net") {
				viaTailscale = true
			} else if a.State != contracts.AddrUntried {
				return "" // it was tried elsewhere too: not just Tailscale
			}
		}
		if viaTailscale || len(s.Addresses) == 0 {
			return contracts.DiagnosisTailscaleSignedOut
		}
	}
	return ""
}
