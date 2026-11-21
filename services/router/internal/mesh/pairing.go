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
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/paircode"
)

// Pairing by code (docs/MESH-AUTH-DESIGN.md D6), the Router's part.
//
// On the host:
//   - POST /devices/pairing-code makes a code: a fresh secret, registered
//     at the Data Service by its hash only, printed with this host's name,
//     its Mesh Service's address (the newest mesh.listening) and its
//     fingerprint. The secret is in this one answer and nowhere else.
//   - The joiner presents it to this host's Mesh Service (/mesh/pair),
//     which opens a request; GET /devices/pair-requests lists them and
//     approve / decline answer them (the Data Service pins the device).
//
// On the joiner:
//   - POST /devices/pair {code} dials the host's Mesh Service over mutual
//     TLS with this device's key, pinned to the code's fingerprint, and
//     presents the secret with this device's name and platform.
//   - It then asks the host for its answer every PairPoll until the
//     request is approved, declined or expires. Approved, it pins the host
//     here, granting it nothing (its user chooses that afterwards), and
//     views it if the host lets this device view.
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
}

// PairHost names a host in the pairing answers.
type PairHost struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	MeshURL     string `json:"mesh_url,omitempty"`
}

// SelfDevice is GET /devices/self: this device as other devices see it.
type SelfDevice struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Short       string `json:"short,omitempty"`
	Platform    string `json:"platform"`
	// MeshURL is where other devices reach this one: its Mesh Service's
	// newest mesh.listening. Empty: no Mesh Service has run here, so this
	// device can pair with hosts but can't host (or make a pairing code).
	MeshURL       string `json:"mesh_url,omitempty"`
	HostsSessions bool   `json:"hosts_sessions"`
	// RequireTailnet is "Only accept devices on my tailnet": this host's
	// stored setting (PUT /devices/settings), followed live by its Mesh
	// Service; unset, what the Mesh Service last reported running with;
	// with neither, the default (true). Always present.
	RequireTailnet *bool `json:"require_tailnet,omitempty"`
	// RequireTailnetLocked: the Mesh Service was started with an explicit
	// -require-tailnet, which overrides the setting (RequireTailnet is
	// then the flag's value), so Settings can't change it.
	RequireTailnetLocked bool `json:"require_tailnet_locked,omitempty"`
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
	// PeerID is the row this device pinned the host as, once approved.
	PeerID string `json:"peer_id,omitempty"`
	// Error says what went wrong pinning the host after approval.
	Error string `json:"error,omitempty"`
	// Self is this device's short fingerprint: what the host's approval
	// prompt should show, for the user to compare before approving.
	Self string `json:"self"`
}

type join struct {
	mu      sync.Mutex
	status  JoinStatus
	code    paircode.Code
	started time.Time
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

// --- This device ---------------------------------------------------------------

type selfCache struct {
	mu   sync.Mutex
	at   time.Time
	l    contracts.MeshListener
	have bool
}

const selfCacheFor = 10 * time.Second

// selfMesh is this node's Mesh Service as it last announced itself
// (mesh.listening), if it ever has.
func (m *Manager) selfMesh(ctx context.Context) (contracts.MeshListener, bool, error) {
	m.self.mu.Lock()
	defer m.self.mu.Unlock()
	if !m.self.at.IsZero() && time.Since(m.self.at) < selfCacheFor {
		return m.self.l, m.self.have, nil
	}
	// The newest written by the Mesh Service itself (actor "mesh"): a
	// mesh.listening from anyone else is ignored.
	types := []string{string(contracts.MeshListening)}
	var l contracts.MeshListener
	have := false
	var since int64
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
			if ev.Actor == "mesh" && json.Unmarshal(ev.Payload, &cand) == nil && cand.MeshURL != "" {
				l, have = cand, true
			}
		}
		since = evs[len(evs)-1].Seq
	}
	m.self.at, m.self.l, m.self.have = time.Now(), l, have
	return l, have, nil
}

// platform is this device's platform as its hello reports it (its Mesh
// Service's -platform), else this Router's: one answer everywhere.
func (m *Manager) platform(ctx context.Context) string {
	if l, ok, err := m.selfMesh(ctx); err == nil && ok && l.Platform != "" {
		return l.Platform
	}
	return m.opts.Platform
}

func (m *Manager) selfDevice(ctx context.Context) SelfDevice {
	d := SelfDevice{Name: m.opts.SelfName, Platform: m.platform(ctx)}
	if m.opts.Identity != nil {
		fp := m.opts.Identity.Fingerprint()
		d.Fingerprint, d.Short = string(fp), fp.Short()
	}
	l, ok, err := m.selfMesh(ctx)
	if err == nil && ok {
		d.MeshURL, d.HostsSessions = l.MeshURL, l.HostsSessions
	}
	rt, locked := m.requireTailnet(ctx, l, err == nil && ok)
	d.RequireTailnetLocked = locked
	d.RequireTailnet = &rt
	return d
}

// requireTailnet is "Only accept devices on my tailnet" as this host's
// Mesh Service applies it now: an explicit -require-tailnet (locked)
// wins; else the stored setting; else what the Mesh Service last ran
// with; else on. A settings read that fails reads as on: this is a check,
// so it fails closed. l is the Mesh Service's newest mesh.listening, if
// have.
func (m *Manager) requireTailnet(ctx context.Context, l contracts.MeshListener, have bool) (on, locked bool) {
	if have && l.RequireTailnetFlag {
		return l.RequireTailnet, true
	}
	s, err := m.data.MeshSettings(ctx)
	switch {
	case err != nil:
		return true, false
	case s.RequireTailnet != nil:
		return *s.RequireTailnet, false
	case have:
		return l.RequireTailnet, false
	}
	return true, false
}

// MeshSettingsRequest is PUT /devices/settings.
type MeshSettingsRequest struct {
	RequireTailnet *bool `json:"require_tailnet"`
}

// setMeshSettings is PUT /devices/settings: this host's own user turning
// "Only accept devices on my tailnet" on or off. The Data Service stores
// it (device.settings_changed, in the device activity log) and the Mesh
// Service follows it. Answers with this device as GET /devices/self.
func (m *Manager) setMeshSettings(w http.ResponseWriter, r *http.Request) {
	var body MeshSettingsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	if body.RequireTailnet == nil {
		writeError(w, http.StatusUnprocessableEntity, "bad_request", "require_tailnet is required: true or false")
		return
	}
	if l, ok, err := m.selfMesh(r.Context()); err == nil && ok && l.RequireTailnetFlag {
		writeError(w, http.StatusConflict, "require_tailnet_locked",
			"this device's Mesh Service was started with -require-tailnet, which overrides this setting: restart it without the flag to set it here")
		return
	}
	if _, err := m.data.SetMeshSettings(r.Context(), contracts.MeshSettings{RequireTailnet: body.RequireTailnet}); err != nil {
		writeDeviceDataError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m.selfDevice(r.Context()))
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
	code := paircode.Encode(paircode.Code{Host: m.opts.SelfName, MeshURL: l.MeshURL, Fingerprint: fp, ExpiresAt: rec.ExpiresAt, Secret: secret})
	writeJSON(w, http.StatusCreated, PairingCodeResponse{
		ID: rec.ID, Code: code, ExpiresAt: rec.ExpiresAt,
		Host: PairHost{Name: m.opts.SelfName, Fingerprint: string(fp), MeshURL: l.MeshURL},
	})
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
	_ = m.Reload(r.Context())
	writeJSON(w, http.StatusOK, out)
}

// --- The joiner's side ---------------------------------------------------------

// pairCodeError is a code refused here, before anything is dialled.
type pairCodeError struct {
	status int
	code   string
	msg    string
}

func (e *pairCodeError) Error() string { return e.msg }

// startJoin presents a pasted code to its host and starts waiting for the
// answer.
func (m *Manager) startJoin(ctx context.Context, raw string) (JoinStatus, error) {
	code, err := paircode.Parse(raw)
	var verr *paircode.VersionError
	switch {
	case errors.As(err, &verr):
		return JoinStatus{}, &pairCodeError{http.StatusUnprocessableEntity, "code_version", verr.Error()}
	case err != nil:
		return JoinStatus{}, &pairCodeError{http.StatusUnprocessableEntity, "bad_code", err.Error()}
	case m.opts.Identity == nil:
		return JoinStatus{}, &pairCodeError{http.StatusServiceUnavailable, "no_device_key", errNoIdentity.Error()}
	case code.Fingerprint == m.opts.Identity.Fingerprint():
		return JoinStatus{}, &pairCodeError{http.StatusUnprocessableEntity, "pair_self", "that's this device's own pairing code: paste it on the other device"}
	}
	// A minute's grace for clocks that disagree; the host decides anyway.
	if m.opts.Now().After(code.ExpiresAt.Add(time.Minute)) {
		return JoinStatus{}, &pairCodeError{http.StatusUnprocessableEntity, "code_expired", "that code has expired or was already used; ask for a new one"}
	}
	peers, err := m.data.Peers(ctx)
	if err != nil {
		return JoinStatus{}, &pairCodeError{http.StatusBadGateway, "data_unavailable", "this device's Data Service isn't answering"}
	}
	for _, p := range peers {
		if p.Fingerprint == string(code.Fingerprint) {
			return JoinStatus{}, &pairCodeError{http.StatusConflict, "already_paired",
				p.Name + " is already paired here; remove it first to pair again"}
		}
	}
	host := contracts.Peer{Name: code.Host, MeshURL: code.MeshURL, Fingerprint: string(code.Fingerprint)}
	body := map[string]any{
		"secret":   base64.RawURLEncoding.EncodeToString(code.Secret),
		"name":     m.opts.SelfName,
		"platform": m.platform(ctx),
	}
	if l, ok, err := m.selfMesh(ctx); err == nil && ok {
		body["mesh_url"], body["hosts_sessions"] = l.MeshURL, l.HostsSessions
	}
	raw2, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, client, err := m.peerRequest(ctx, http.MethodPost, host, "/mesh/pair", bytes.NewReader(raw2))
	if err != nil {
		return JoinStatus{}, joinDialError(host, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return JoinStatus{}, joinDialError(host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusCreated {
		var e struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		status := resp.StatusCode
		if status < 400 || status >= 500 {
			status = http.StatusBadGateway
		}
		return JoinStatus{}, &pairCodeError{status, firstNonEmpty(paircode.CleanText(e.Code), "pair_failed"), firstNonEmpty(paircode.CleanText(e.Error), paircode.CleanText(code.Host)+" answered "+resp.Status)}
	}
	var st struct {
		RequestID string              `json:"request_id"`
		State     contracts.PairState `json:"state"`
		ExpiresAt time.Time           `json:"expires_at"`
	}
	if err := json.Unmarshal(b, &st); err != nil || st.RequestID == "" {
		return JoinStatus{}, &pairCodeError{http.StatusBadGateway, "pair_failed", paircode.CleanText(code.Host) + " answered something that isn't a pairing request"}
	}
	key := code.Fingerprint.Short() + ":" + st.RequestID
	j := &join{code: code, started: m.opts.Now(), status: JoinStatus{
		ID: key, RequestID: st.RequestID, State: contracts.PairPending, ExpiresAt: st.ExpiresAt, Self: m.opts.Identity.Fingerprint().Short(),
		Host: PairHost{Name: paircode.CleanText(code.Host), Fingerprint: string(code.Fingerprint), MeshURL: code.MeshURL},
	}}
	m.mu.Lock()
	for k, old := range m.joins {
		if m.opts.Now().Sub(old.started) > joinMemory {
			delete(m.joins, k)
		}
	}
	m.joins[key] = j
	m.mu.Unlock()
	go m.waitJoin(m.bgCtx(), j)
	return j.get(), nil
}

func joinDialError(host contracts.Peer, err error) error {
	var pin *devicekey.PinError
	if errors.As(err, &pin) {
		return &pairCodeError{http.StatusBadGateway, "host_key_mismatch",
			"the device answering at " + host.MeshURL + " isn't the " + paircode.CleanText(host.Name) + " in the code (" + pin.Error() + "); nothing was paired"}
	}
	state, reason := dialFailure(host, err)
	return &pairCodeError{http.StatusBadGateway, "host_" + string(state), "can't reach " + paircode.CleanText(host.Name) + " at " + host.MeshURL + ": " + reason}
}

// joinGrace is how long past a request's expiry the joiner keeps asking,
// for a host whose clock runs behind.
const joinGrace = 30 * time.Second

// waitJoin asks the host for its answer until there is one.
func (m *Manager) waitJoin(ctx context.Context, j *join) {
	host := contracts.Peer{Name: j.code.Host, MeshURL: j.code.MeshURL, Fingerprint: string(j.code.Fingerprint)}
	id := j.get().RequestID
	t := time.NewTicker(m.opts.PairPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st, code, err := m.pollJoin(ctx, host, id)
		switch {
		case err == nil && code == http.StatusOK:
			switch st.State {
			case contracts.PairApproved:
				m.pinHost(ctx, j, st.Level)
				return
			case contracts.PairDeclined, contracts.PairExpired:
				j.set(func(s *JoinStatus) { s.State = st.State })
				return
			}
		case err == nil && code == http.StatusNotFound:
			j.set(func(s *JoinStatus) { s.State = contracts.PairExpired })
			return
		}
		if m.opts.Now().After(j.get().ExpiresAt.Add(joinGrace)) {
			j.set(func(s *JoinStatus) { s.State = contracts.PairExpired })
			return
		}
	}
}

type polled struct {
	State contracts.PairState `json:"state"`
	Level string              `json:"level"`
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

// pinHost pins an approving host here: granted nothing (this device's user
// chooses), viewed if the host lets this device view and its hello says
// so now, with what it granted recorded as what it lets this device do.
func (m *Manager) pinHost(ctx context.Context, j *join, granted string) {
	level := contracts.GrantLevel(granted)
	if granted == "none" || !level.Valid() {
		level = contracts.GrantNone
	}
	now := m.opts.Now().UTC()
	yes := true
	row := contracts.Peer{
		Name:        pinName(j.code.Host, j.code.Fingerprint),
		MeshURL:     j.code.MeshURL,
		Fingerprint: string(j.code.Fingerprint),
		Pairing:     &contracts.PeerPairing{How: contracts.PairCode},
		Remote:      &contracts.PeerRemote{Level: level, AsOf: &now, HostsSessions: &yes},
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
	}
	row.Viewed = &viewed
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
		log.Printf("mesh: pin %s after it approved this device: %v", j.code.Host, err)
		j.set(func(s *JoinStatus) {
			s.State, s.Level = contracts.PairApproved, levelWord(level)
			s.Error = "approved, but this device couldn't pin " + j.code.Host + ": " + err.Error()
		})
		return
	}
	_ = m.Reload(ctx)
	j.set(func(s *JoinStatus) { s.State, s.Level, s.PeerID = contracts.PairApproved, levelWord(level), row.ID })
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
