package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// This host's mesh settings (docs/MESH-NETWORK-DESIGN.md, "The host's
// network choices"): the mesh_settings row (migration 0038), written with
// device.settings_changed in the same transaction (the outbox pattern the
// registries use). The row is the setting; the event is its history in
// the device activity log, and what the Mesh Service follows to re-read
// it. Only this host's own user writes it (the caller checks authority);
// the Mesh Service never does.

// MeshSettingsActor is the only actor device.settings_changed and
// device.discoverable are written under: the store's own, with the user
// who made the change in the payload (By), and POST /events refuses
// device.* outright, so nothing else can stand in for either.
const MeshSettingsActor = "settings"

// MeshSettings is this host's mesh settings: the row's data. A row that
// can't be decoded reads as no mesh network (listening nowhere fails
// closed) and is reported.
func (l *Log) MeshSettings(ctx context.Context) (contracts.MeshSettings, error) {
	var data string
	err := l.db.QueryRowContext(ctx, `SELECT data FROM mesh_settings WHERE id = 1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return contracts.MeshSettings{Networks: []contracts.NetworkChoice{}}, nil
	}
	if err != nil {
		return contracts.MeshSettings{}, fmt.Errorf("mesh settings: %w", err)
	}
	var s contracts.MeshSettings
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		return contracts.MeshSettings{}, fmt.Errorf("mesh settings: corrupt row: %w", err)
	}
	if s.Networks == nil {
		s.Networks = []contracts.NetworkChoice{}
	}
	return s, nil
}

// ValidateMeshSettings checks s's shape.
func ValidateMeshSettings(s contracts.MeshSettings) *contracts.ValidationError {
	var v contracts.ValidationError
	if len(s.Networks) > contracts.MaxNetworkChoices {
		v.Add("networks", fmt.Sprintf("at most %d", contracts.MaxNetworkChoices))
	}
	listening := 0
	for _, n := range s.Networks {
		if n.Listen {
			listening++
		}
	}
	if listening > 1 {
		// The MVP's one mesh network, held here whatever a client sends.
		v.Add("networks", "one mesh network at most")
	}
	seen := map[string]bool{}
	for i, n := range s.Networks {
		f := fmt.Sprintf("networks[%d]", i)
		switch {
		case n.ID == "" || len(n.ID) > 128:
			v.Add(f+".id", "required, at most 128 characters")
		case strings.ContainsFunc(n.ID, func(c rune) bool { return c > unicode.MaxASCII || !unicode.IsPrint(c) || c == ' ' }):
			v.Add(f+".id", "printable ASCII without spaces")
		case seen[n.ID]:
			v.Add(f+".id", "listed twice")
		}
		seen[n.ID] = true
		if strings.HasPrefix(n.ID, "public:") {
			v.Add(f+".id", "a public address isn't a mesh network (that's public)")
		}
		if n.TailnetCheck && n.ID != "tailscale" {
			v.Add(f+".tailnet_check", "only the Tailscale network has an identity check")
		}
		if len(n.Name) > 64 || strings.ContainsFunc(n.Name, func(c rune) bool { return !unicode.IsGraphic(c) || unicode.Is(unicode.Cf, c) }) {
			v.Add(f+".name", "at most 64 printable characters")
		}
		if len(n.SSID) > 64 {
			v.Add(f+".ssid", "at most 64 bytes")
		}
		if len(n.GatewayHW) > 32 {
			v.Add(f+".gateway_hw", "too long")
		}
	}
	if s.Port != 0 && (s.Port < 1024 || s.Port > 65535) {
		v.Add("port", "1024 to 65535, or 0 for 17380")
	}
	if v.Empty() {
		return nil
	}
	return &v
}

// SetMeshSettings replaces this host's mesh settings, made by `by`, and
// records device.settings_changed in the same transaction. The caller
// checks authority: only this host's own user changes them.
// require_tailnet is written beside the networks for a rollback (an older
// build reads only it): the Tailscale network's check; without Tailscale
// chosen, the value given, else on.
func (l *Log) SetMeshSettings(ctx context.Context, s contracts.MeshSettings, by string) (contracts.MeshSettings, error) {
	if s.Networks == nil {
		s.Networks = []contracts.NetworkChoice{}
	}
	if verr := ValidateMeshSettings(s); verr != nil {
		return contracts.MeshSettings{}, verr
	}
	rt := true
	switch c, ok := s.Choice("tailscale"); {
	case ok && c.Listen:
		rt = c.TailnetCheck
	case s.RequireTailnet != nil:
		rt = *s.RequireTailnet
	}
	s.RequireTailnet = &rt
	data, err := json.Marshal(s)
	if err != nil {
		return contracts.MeshSettings{}, err
	}
	payload, err := json.Marshal(contracts.MeshSettingsChange{MeshSettings: s, By: by})
	if err != nil {
		return contracts.MeshSettings{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err = l.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(`INSERT INTO mesh_settings (id, data, updated_at) VALUES (1, ?, ?)
			ON CONFLICT (id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`, string(data), now); err != nil {
			return err
		}
		return tx.Append(&contracts.Envelope{Type: contracts.DeviceSettingsChanged, Actor: MeshSettingsActor, Payload: payload})
	})
	if err != nil {
		return contracts.MeshSettings{}, err
	}
	return s, nil
}

// discoverableScan bounds how far back Discoverable looks.
const discoverableScan = 20

// Discoverable is this host's setup window as last set (the newest
// device.discoverable under the store's actor; closed when there's none).
// It's in-memory state on the Mesh Service; this is what it reads at
// start, and what the Data Service checks a discovered pairing request
// against.
func (l *Log) Discoverable(ctx context.Context) (contracts.Discoverable, error) {
	evs, err := l.scanEvents(ctx, `SELECT seq, timestamp, type, session_id, task_id, actor, payload FROM events
	       WHERE type = ? AND actor = ? ORDER BY seq DESC LIMIT ?`, string(contracts.DeviceDiscoverable), MeshSettingsActor, discoverableScan)
	if err != nil {
		return contracts.Discoverable{}, err
	}
	for _, ev := range evs {
		var d contracts.Discoverable
		if json.Unmarshal(ev.Payload, &d) == nil {
			return d, nil
		}
	}
	return contracts.Discoverable{}, nil
}

// ErrWindowClosed is a renewal of a setup window that isn't open (closed,
// run out, or past DiscoverableAbsoluteMax): it must be opened again.
var ErrWindowClosed = errors.New("the setup window isn't open: open it again")

// SetDiscoverable opens (until in the future, at most
// contracts.DiscoverableMax from now and DiscoverableAbsoluteMax from when
// it was first opened), renews (renew: only a window open now), or closes
// (zero) this host's setup window, made by `by`. The caller checks
// authority.
func (l *Log) SetDiscoverable(ctx context.Context, until time.Time, by string, renew bool) (contracts.Discoverable, error) {
	return l.SetDiscoverableReason(ctx, until, by, renew, "")
}

// SetDiscoverableReason is SetDiscoverable, saying why a window closed
// (reason, only on closing).
func (l *Log) SetDiscoverableReason(ctx context.Context, until time.Time, by string, renew bool, reason string) (contracts.Discoverable, error) {
	now := time.Now().UTC()
	cur, err := l.Discoverable(ctx)
	if err != nil {
		return contracts.Discoverable{}, err
	}
	d := contracts.Discoverable{By: by}
	switch {
	case until.IsZero():
		d.OpenedAt, d.Reason = cur.OpenedAt, reason
		if cur.Open(now) {
			d.ClosedAt = &now
		}
	case renew:
		if !cur.Open(now) || (!cur.OpenedAt.IsZero() && !now.Before(cur.OpenedAt.Add(contracts.DiscoverableAbsoluteMax))) {
			return contracts.Discoverable{}, ErrWindowClosed
		}
		d.OpenedAt = cur.OpenedAt
		if d.OpenedAt.IsZero() {
			d.OpenedAt = now
		}
		d.Until = until.UTC()
	default:
		d.OpenedAt, d.Until = now, until.UTC()
	}
	if !d.Until.IsZero() {
		if max := now.Add(contracts.DiscoverableMax); d.Until.After(max) {
			d.Until = max
		}
		if max := d.OpenedAt.Add(contracts.DiscoverableAbsoluteMax); d.Until.After(max) {
			d.Until = max
		}
		if !d.Until.After(now) {
			d.Until = time.Time{}
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return contracts.Discoverable{}, err
	}
	if err := l.Append(ctx, &contracts.Envelope{Type: contracts.DeviceDiscoverable, Actor: MeshSettingsActor, Payload: raw}); err != nil {
		return contracts.Discoverable{}, err
	}
	return d, nil
}
