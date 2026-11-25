package mesh

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
	"github.com/drewkidwell/mads/pkg/paircode"
)

// Half-paired devices (docs/MESH-NETWORK-DESIGN.md F6), the Router's part.
// A pairing is two rows, one on each device; this keeps them in step:
//   - R1: removing a device first asks it to forget this one
//     (/mesh/forget, forgetWithin), then deletes the row here whatever it
//     answered; the result says how it went there.
//   - R3: whether each paired device has this one paired, as it last
//     answered (pairing_there on GET /devices), from the probe loop, and at
//     once for every paired row with POST /devices/check.
//   - R4: a row this device doesn't view (the other device used this one)
//     whose device no longer has this one paired is removed here
//     (device.unpaired, by "reconcile"): it only takes away access the
//     other device already gave up. A viewed row is kept, to re-pair. Only
//     a pairing the other device was once seen to have (remote
//     paired_there_at, the paired_at it was seen with) is undone: an introduction, or
//     a pairing whose other side hasn't pinned yet, isn't one it gave up.

// thereState is what a device last said about having this one paired.
type thereState struct {
	state  string
	reason string
	at     time.Time
}

// pairGuard is how long after a pairing a not_paired reads as unknown:
// the other device's Mesh Service follows new pairings from its event log
// a moment after its Data Service writes them.
const pairGuard = 30 * time.Second

// Budgets: a forget before a removal (R1), and POST /devices/check as a
// whole. Vars so a test can shrink them.
var (
	forgetWithin = 5 * time.Second
	checkWithin  = 6 * time.Second
)

// isPinMismatch reports whether err is a different key answering: on the
// path in use, or at every address tried.
func isPinMismatch(err error) bool {
	var pin *devicekey.PinError
	var race *meshnet.RaceError
	switch {
	case errors.As(err, &race):
		n := 0
		for _, a := range race.Attempts {
			if errors.As(a.Err, &pin) {
				n++
			}
		}
		return n > 0 && n == startedCount(race.Attempts)
	case errors.As(err, &pin):
		return true
	}
	return false
}

// classifyThere reads a probe of p as R3's table: a hello is paired;
// unpaired_device is not_paired (that device read its list, and this key
// isn't on it), unless p was paired here less than pairGuard ago; a
// different key answering is other_key; anything else is unknown, with
// the reason.
func (m *Manager) classifyThere(p contracts.Peer, probe Probe) (string, string) {
	switch {
	case probe.Status == http.StatusOK && probe.Hello.MeshAPI != 0:
		return contracts.PairingTherePaired, ""
	case probe.PinMismatch:
		return contracts.PairingThereOtherKey, firstNonEmpty(probe.Reason, "a different device answered")
	case probe.Status == http.StatusForbidden && probe.Code == "unpaired_device":
		if at := m.pairedAt(p); !at.IsZero() && m.opts.Now().Sub(at) < pairGuard {
			return contracts.PairingThereUnknown, "paired moments ago: " + firstNonEmpty(p.Name, "it") + " may not have read it yet"
		}
		return contracts.PairingThereNotPaired, firstNonEmpty(p.Name, "it") + " no longer has this device paired"
	}
	return contracts.PairingThereUnknown, firstNonEmpty(probe.Reason, "no answer")
}

// pairedAt is when p was last paired here: its row's record, or this
// Router's own pinning (a re-pairing in place keeps the row's).
func (m *Manager) pairedAt(p contracts.Peer) time.Time {
	var at time.Time
	if p.Pairing != nil {
		at = p.Pairing.PairedAt
	}
	m.mu.Lock()
	if t, ok := m.pinnedAt[p.Fingerprint]; ok && t.After(at) {
		at = t
	}
	m.mu.Unlock()
	return at
}

// pairedAtOf is p's row's own pairing time (zero without one).
func pairedAtOf(p contracts.Peer) time.Time {
	if p.Pairing == nil {
		return time.Time{}
	}
	return p.Pairing.PairedAt
}

// notePinned records that fp was paired (or re-paired) just now.
func (m *Manager) notePinned(fp string) {
	if fp == "" {
		return
	}
	m.mu.Lock()
	m.pinnedAt[fp] = m.opts.Now()
	delete(m.there, fp)
	m.mu.Unlock()
}

// noteApproved records that this Router approved fp's pairing request
// just now (it's the host of that pairing).
func (m *Manager) noteApproved(fp string) {
	if fp == "" {
		return
	}
	m.mu.Lock()
	m.approvedAt[fp] = m.opts.Now()
	m.mu.Unlock()
}

// heldOffLocked reports whether p is a device whose pairing request this
// Router approved less than pairGuard ago. It isn't dialled until then:
// its Router pins this device only after it reads the answer (and, for a
// discovered pairing, after its user confirms the check code), and its
// Mesh Service follows that a moment later; dialled before, it refuses
// this key (unpaired_device), and records a device.refused for a pairing
// that worked. The probe loop and POST /devices/check wait; this device
// learns nothing from such an answer anyway (pairGuard reads it as
// unknown). m.mu must be held.
func (m *Manager) heldOffLocked(p contracts.Peer) bool {
	at, ok := m.approvedAt[p.Fingerprint]
	if ok && m.opts.Now().Sub(at) >= pairGuard {
		delete(m.approvedAt, p.Fingerprint)
		ok = false
	}
	return ok
}

// noteThere folds a probe of the paired row p into what it says about
// having this device paired; with reconcile, a grant-only row it no
// longer has paired is removed here (R4). It reports the state.
func (m *Manager) noteThere(ctx context.Context, p contracts.Peer, probe Probe, reconcile bool) string {
	if !p.IsPaired() {
		return ""
	}
	state, reason := m.classifyThere(p, probe)
	m.mu.Lock()
	m.there[p.Fingerprint] = thereState{state: state, reason: reason, at: m.opts.Now().UTC()}
	m.mu.Unlock()
	switch {
	case state == contracts.PairingTherePaired:
		m.notePairedThere(ctx, p)
	case reconcile && state == contracts.PairingThereNotPaired && !p.IsViewed() && m.confirmedThere(p):
		m.reconcileRemove(ctx, p)
	}
	return state
}

// confirmedThere reports whether p's device was seen to have this one
// paired since p's current pairing (R4's precondition).
func (m *Manager) confirmedThere(p contracts.Peer) bool {
	at := pairedAtOf(p)
	if at.IsZero() {
		return false
	}
	m.mu.Lock()
	seen, ok := m.pairedThere[p.Fingerprint]
	m.mu.Unlock()
	if ok && seen.Equal(at) {
		return true
	}
	return p.Remote != nil && p.Remote.PairedThereAt != nil && p.Remote.PairedThereAt.Equal(at)
}

// notePairedThere records that p's device has this one paired, once per
// pairing. The row keeps it too (remote.paired_there_at, written with the
// rest of what the device answered: noteRemote), so it outlives a restart.
func (m *Manager) notePairedThere(_ context.Context, p contracts.Peer) {
	at := pairedAtOf(p)
	if at.IsZero() || m.confirmedThere(p) {
		return
	}
	m.mu.Lock()
	m.pairedThere[p.Fingerprint] = at
	m.mu.Unlock()
}

// reconcileRemove deletes a grant-only row whose device no longer has
// this one paired (R4), if it's still as read.
func (m *Manager) reconcileRemove(ctx context.Context, p contracts.Peer) {
	cur, err := m.data.Peer(ctx, p.ID)
	// Still the pairing that was probed: a re-pairing in place meanwhile
	// stamps a new paired_at, and is the guard's to judge.
	if err != nil || cur.Fingerprint != p.Fingerprint || cur.IsViewed() || !pairedAtOf(cur).Equal(pairedAtOf(p)) || !m.confirmedThere(cur) {
		return
	}
	if err := m.data.DeletePeerReconciled(ctx, cur); err != nil {
		if !errors.Is(err, dataclient.ErrNotFound) && !errors.Is(err, dataclient.ErrConflict) {
			log.Printf("mesh: remove %s (it no longer has this device paired): %v", p.Name, err)
		}
		return
	}
	log.Printf("mesh: removed %s: it no longer has this device paired", p.Name)
	m.mu.Lock()
	delete(m.there, p.Fingerprint)
	m.mu.Unlock()
	_ = m.Reload(ctx)
}

// thereOf is the last check of fp, if any.
func (m *Manager) thereOfLocked(fp string) (thereState, bool) {
	t, ok := m.there[fp]
	return t, ok
}

// checkAll checks every paired row now, all at once, within checkWithin,
// removing the grant-only rows found not_paired (R4).
func (m *Manager) checkAll(ctx context.Context) error {
	if err := m.Reload(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	rows := make([]contracts.Peer, 0, len(m.all))
	var held []contracts.Peer
	for _, p := range m.all {
		switch {
		case !p.IsPaired():
		case m.heldOffLocked(p):
			held = append(held, p)
		default:
			rows = append(rows, p)
		}
	}
	m.mu.Unlock()
	for _, p := range held {
		m.noteThere(ctx, p, Probe{Reason: "paired moments ago: " + firstNonEmpty(p.Name, "it") + " may not have pinned this device yet"}, false)
	}
	cctx, cancel := context.WithTimeout(ctx, checkWithin)
	defer cancel()
	var wg sync.WaitGroup
	for _, p := range rows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probe := m.ProbePeer(cctx, p)
			// The removal isn't held to the check's clock.
			m.noteThere(context.WithoutCancel(ctx), p, probe, true)
		}()
	}
	wg.Wait()
	return nil
}

// checkDevices serves POST /devices/check: every paired row checked now,
// and the list as it stands after (R3, R4).
func (m *Manager) checkDevices(w http.ResponseWriter, r *http.Request) {
	if err := m.checkAll(r.Context()); err != nil {
		writeDeviceDataError(w, err)
		return
	}
	out, err := m.deviceRows(r.Context(), false)
	if err != nil {
		writeDeviceDataError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// Forget results (R1): removed there, it didn't have this device paired,
// it couldn't be reached, or it answered with an error.
const (
	thereRemoved   = "removed"
	thereNotPaired = "not_paired"
	thereOffline   = "offline"
	thereFailed    = "failed"
)

// forgetOn asks p to forget this device (/mesh/forget), within
// forgetWithin, before this device removes it (R1).
func (m *Manager) forgetOn(ctx context.Context, p contracts.Peer) (string, string) {
	if !p.IsPaired() {
		return thereOffline, "no key is pinned for " + firstNonEmpty(p.Name, "it") + ", so it can't be asked"
	}
	status, body, err := m.peerCallWithin(ctx, forgetWithin, p, http.MethodPost, "/mesh/forget", nil)
	code, msg := errorBody(body)
	code, msg = paircode.CleanText(code), paircode.CleanText(msg)
	switch {
	case err != nil && isPinMismatch(err):
		_, reason := dialFailure(p, err)
		return thereFailed, reason
	case err != nil:
		_, reason := dialFailure(p, err)
		return thereOffline, reason
	case status == http.StatusNoContent || status == http.StatusOK:
		return thereRemoved, ""
	case status == http.StatusForbidden && code == "unpaired_device":
		return thereNotPaired, ""
	}
	return thereFailed, firstNonEmpty(msg, firstNonEmpty(p.Name, "it")+" answered "+strconv.Itoa(status))
}

// pairingOf is a found device's pairing (R5): whether it's paired here,
// and whether it says it has this device paired. An older device doesn't
// say (there nil): then this device's last check stands in. fresh is a
// row here paired less than pairGuard ago: its device may not have read
// it yet, so it isn't read as half-paired (as R3's not_paired isn't).
func pairingOf(here bool, there *bool, checked string, fresh bool) string {
	t := false
	switch {
	case here && fresh:
		t = true
	case there != nil:
		t = *there
	case here:
		t = checked != contracts.PairingThereNotPaired
	}
	switch {
	case here && t:
		return "both"
	case here:
		return "here_only"
	case t:
		return "there_only"
	}
	return "none"
}

// retryableJoin marks a join failure worth retrying with the same code
// (R7): the host couldn't be reached or answered, or wasn't ready, or
// asked this device to slow down (retryAfter, when it said how long).
func retryableJoin(err error, retryAfter time.Duration) error {
	var pe *pairCodeError
	if !errors.As(err, &pe) {
		return err
	}
	switch pe.code {
	case "host_unreachable", "devices_unavailable", "pairing_unavailable", "rate_limited":
	default:
		return err
	}
	if pe.extra == nil {
		pe.extra = map[string]any{}
	}
	pe.extra["retryable"] = true
	if pe.code == "rate_limited" && retryAfter > 0 {
		pe.extra["retry_after"] = int(retryAfter.Round(time.Second).Seconds())
	}
	return pe
}

// retryAfterOf reads a Retry-After in seconds.
func retryAfterOf(h http.Header) time.Duration {
	if secs, err := strconv.Atoi(h.Get("Retry-After")); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}
