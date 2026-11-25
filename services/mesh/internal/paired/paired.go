// Package paired is the Mesh Service's copy of this host's paired devices
// (docs/MESH-AUTH-DESIGN.md D3): the mesh_peers rows with a pinned
// fingerprint, read from the Data Service, the state's sole owner, and kept
// current by following peer.* events (the same cursor-then-list-then-stream
// loop as the Router's mesh manager).
//
// It is who may reach this node: the Mesh Service looks every caller up
// here by the fingerprint of its TLS client certificate's key (A4) and
// authorises it by the grant it finds (A5). A device that isn't here is
// refused. After each reload the OnReload hook runs, so requests already
// in flight (long-lived streams) can be ended when their device was
// unpaired or its grant changed.
//
// The listener doesn't wait for the Data Service: Run retries with backoff
// while it is down. Until the first load succeeds (Loaded) the Mesh Service
// refuses every device, since it can't tell who is paired.
package paired

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
)

// peerEvents are the changes that can alter the directory: the peer rows,
// this host's mesh settings (device.settings_changed: its mesh networks
// and the Tailscale check) and its setup window (device.discoverable).
// Other device.* audit events always commit beside a peer.* one, so they
// needn't be followed.
var peerEvents = []string{string(contracts.PeerCreated), string(contracts.PeerUpdated), string(contracts.PeerDeleted),
	string(contracts.DeviceSettingsChanged), string(contracts.DeviceDiscoverable)}

// Directory is the in-memory paired-device list, by fingerprint.
type Directory struct {
	data *dataclient.Client
	// minBackoff and maxBackoff bound the retry delay while the Data
	// Service is down; a stream that lasted healthyAfter resets it.
	minBackoff, maxBackoff, healthyAfter time.Duration

	mu     sync.RWMutex
	byFP   map[string]contracts.Peer
	loaded bool
	// summary is the last list logged, so a reload that changes nothing
	// stays quiet.
	summary string
	// onReload runs after every successful reload (OnReload).
	onReload func()
	// freshAt is the last successful list read; staleLogged when its age
	// was last logged (warnStale).
	freshAt, staleLogged time.Time
	// settings is this host's mesh settings as last read, and onSettings
	// runs when they change (OnSettings).
	settings     contracts.MeshSettings
	haveSettings bool
	onSettings   func(contracts.MeshSettings)
	// window is this host's setup window as last read, and
	// onDiscoverable runs when it changes (OnDiscoverable).
	window         contracts.Discoverable
	haveWindow     bool
	onDiscoverable func(contracts.Discoverable)
}

// retryLogEvery is how many failed attempts in a row go by between log
// lines (the first is always logged).
const retryLogEvery = 20

// MaxBackoff bounds how long the directory waits between attempts to
// reload and resubscribe. The list is who may reach this host, so a
// revocation must not wait out a long backoff: after a Data Service
// restart (or a few short-lived streams) the cached list is refreshed
// within this. The Data Service is on loopback and a retry is one list
// read, so a short cap is cheap; a periodic poll beside the stream was the
// alternative, but it adds steady load and still needs this retry for a
// failed read.
const MaxBackoff = 5 * time.Second

// New builds a Directory reading from data, which must carry the install
// token (the Data Service's front door refuses anything else).
func New(data *dataclient.Client) *Directory {
	return &Directory{data: data, minBackoff: time.Second, maxBackoff: MaxBackoff, healthyAfter: 5 * time.Second, byFP: map[string]contracts.Peer{}}
}

// Run loads the list and follows peer.* changes until ctx ends, retrying
// with backoff (at most MaxBackoff) whenever the Data Service can't be
// read, a failed reload included.
func (d *Directory) Run(ctx context.Context) {
	backoff := d.minBackoff
	// failures counts attempts since the list last read. While the Data
	// Service stays down only the first and every retryLogEvery-th are
	// logged, and its return once: retrying every few seconds for hours
	// mustn't fill the log.
	failures := 0
	wait := func(err error, what string) bool {
		failures++
		if failures == 1 || failures%retryLogEvery == 0 {
			log.Printf("mesh: paired devices: %s: %v (attempt %d; retrying in %s)", what, err, failures, backoff)
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, d.maxBackoff)
		return true
	}
	for ctx.Err() == nil {
		// Cursor first, then the list: a change landing between the two is
		// both in the list and replayed, never in neither.
		since, cursorErr := d.data.LatestSeq(ctx, "", peerEvents)
		// The list is read whatever the cursor did: with the event log
		// unreadable (a damaged row, a broken stream), reloading on every
		// retry is what keeps a revocation from waiting on it. Following
		// resumes once the cursor reads.
		err := d.Reload(ctx)
		if err == nil {
			d.fresh()
			err = cursorErr
		}
		if err != nil {
			d.warnStale()
			if ctx.Err() != nil || !wait(err, "load") {
				return
			}
			continue
		}
		if failures > 0 {
			log.Printf("mesh: paired devices: read again after %d failed attempts", failures)
			failures = 0
		}
		streamCtx, stopStream := context.WithCancel(ctx)
		ch, err := d.data.Stream(streamCtx, since, "", peerEvents)
		if err != nil {
			stopStream()
			if ctx.Err() != nil || !wait(err, "subscribe") {
				return
			}
			continue
		}
		started := time.Now()
		ended := errors.New("event stream ended")
		for range ch {
			// Only a stream that delivers (or lasted, below) proves the
			// service healthy: one that opens and closes at once keeps
			// backing off.
			backoff = d.minBackoff
			if err := d.Reload(ctx); err == nil {
				d.fresh()
			} else {
				// The event that said the list changed is consumed: serving
				// on would keep a list known to be stale (a revocation
				// lost). Start over — cursor, list, stream — until the
				// list reads.
				ended = err
				break
			}
		}
		stopStream()
		// The stream ended (the Data Service restarted, or a token
		// rotation closed it), or a reload failed: start over from a fresh
		// cursor, after the backoff — reset if the stream held a while, so
		// endings spread over hours don't add up.
		if time.Since(started) >= d.healthyAfter {
			backoff = d.minBackoff
		}
		if ctx.Err() != nil || !wait(ended, "follow") {
			return
		}
	}
}

// staleAfter is how long the list may go unconfirmed (no successful read)
// before the directory says so loudly: it keeps serving the last list (a
// key it doesn't know is still refused), but a revocation since may not
// have landed.
const staleAfter = 60 * time.Second

// fresh notes a successful list read.
func (d *Directory) fresh() {
	d.mu.Lock()
	d.freshAt, d.staleLogged = time.Now(), time.Time{}
	d.mu.Unlock()
}

// warnStale logs, at most once a minute, that the list hasn't been
// confirmed for staleAfter.
func (d *Directory) warnStale() {
	d.mu.Lock()
	since := time.Since(d.freshAt)
	logIt := !d.freshAt.IsZero() && since >= staleAfter && time.Since(d.staleLogged) >= time.Minute
	if logIt {
		d.staleLogged = time.Now()
	}
	d.mu.Unlock()
	if logIt {
		log.Printf("mesh: paired devices: STALE: the list hasn't been read for %s (the Data Service is down or failing); serving the last one, so a revocation since may not have applied", since.Round(time.Second))
	}
}

// OnReload sets f to run after every successful reload, outside the
// directory's lock. Set it before Run.
func (d *Directory) OnReload(f func()) {
	d.mu.Lock()
	d.onReload = f
	d.mu.Unlock()
}

// OnSettings sets f to run with this host's mesh settings after the first
// successful reload, and after every reload that finds them changed,
// outside the directory's lock. Set it before Run.
func (d *Directory) OnSettings(f func(contracts.MeshSettings)) {
	d.mu.Lock()
	d.onSettings = f
	d.mu.Unlock()
}

// OnDiscoverable sets f to run with this host's setup window after the
// first successful reload, and after every reload that finds it changed,
// outside the directory's lock. Set it before Run.
func (d *Directory) OnDiscoverable(f func(contracts.Discoverable)) {
	d.mu.Lock()
	d.onDiscoverable = f
	d.mu.Unlock()
}

// Settings is this host's mesh settings as last read, and whether they
// have been.
func (d *Directory) Settings() (contracts.MeshSettings, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.settings, d.haveSettings
}

// Reload replaces the directory from the Data Service, then re-reads this
// host's mesh settings. The list comes first and stands on its own: a
// revocation applies whatever happens to the settings read. That read
// never fails the reload; on an error the last good settings stay (or,
// never read, the Mesh Service's default), and it's logged.
func (d *Directory) Reload(ctx context.Context) error {
	list, err := d.data.Peers(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]contracts.Peer, len(list))
	var lines []string
	for _, p := range list {
		if !p.IsPaired() {
			continue
		}
		next[p.Fingerprint] = p
		level := string(p.Level())
		if level == "" {
			level = "none"
		}
		lines = append(lines, p.Name+" "+devicekey.Fingerprint(p.Fingerprint).Short()+" "+level)
	}
	slices.Sort(lines)
	summary := strings.Join(lines, ", ")
	d.mu.Lock()
	first := !d.loaded
	changed := first || summary != d.summary
	d.byFP, d.loaded, d.summary = next, true, summary
	hook := d.onReload
	d.mu.Unlock()
	if changed {
		if summary == "" {
			summary = "none"
		}
		log.Printf("mesh: paired devices (%d): %s", len(next), summary)
	}
	if hook != nil {
		hook()
	}
	d.reloadSettings(ctx)
	d.reloadWindow(ctx)
	return nil
}

// reloadWindow re-reads this host's setup window and calls
// OnDiscoverable's hook when it changed. An error keeps the last one (a
// window that can't be read again closes on its own clock).
func (d *Directory) reloadWindow(ctx context.Context) {
	w, err := d.data.Discoverable(ctx)
	if err != nil {
		log.Printf("mesh: this host's setup window: %v", err)
		return
	}
	d.mu.Lock()
	var hook func(contracts.Discoverable)
	if !d.haveWindow || !d.window.Until.Equal(w.Until) {
		d.window, d.haveWindow = w, true
		hook = d.onDiscoverable
	}
	d.mu.Unlock()
	if hook != nil {
		hook(w)
	}
}

// reloadSettings re-reads this host's mesh settings and calls OnSettings's
// hook when they changed (or on the first read). An error keeps what was
// read last.
func (d *Directory) reloadSettings(ctx context.Context) {
	settings, err := d.data.MeshSettings(ctx)
	if err != nil {
		d.mu.RLock()
		have := d.haveSettings
		d.mu.RUnlock()
		keep := "the Mesh Service's default"
		if have {
			keep = "the last ones read"
		}
		log.Printf("mesh: this host's mesh settings: %v (keeping %s)", err, keep)
		return
	}
	d.mu.Lock()
	var settingsHook func(contracts.MeshSettings)
	if !d.haveSettings || !sameSettings(d.settings, settings) {
		d.settings, d.haveSettings = settings, true
		settingsHook = d.onSettings
	}
	d.mu.Unlock()
	if settingsHook != nil {
		settingsHook(settings)
	}
}

// Lookup is the paired device with fingerprint fp, if any.
func (d *Directory) Lookup(fp string) (contracts.Peer, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	p, ok := d.byFP[fp]
	return p, ok
}

// Len is how many devices are paired.
func (d *Directory) Len() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.byFP)
}

// Loaded reports whether the list has been read at least once.
func (d *Directory) Loaded() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.loaded
}

func sameSettings(a, b contracts.MeshSettings) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}
