package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Device activity (docs/MESH-AUTH-DESIGN.md D8): what Settings →
// Paired devices shows. Two kinds of event make it up:
//   - device.* events, the registry's audit trail (paired, unpaired,
//     grant_changed, introduced, refused, code_created, pair_requested,
//     pair_declined), whose payload names the device by fingerprint;
//   - any event whose actor is a paired device ("device:<name>/<short
//     fingerprint>", set by the Mesh Service and carried by the Router), a
//     device's own actions here.
//
// deviceActorTerm is the partial index's expression (migration 0036): a
// query must use it verbatim for SQLite to use that index.
const deviceActorTerm = "substr(actor, 1, 7) = 'device:'"

// deviceTypeTerm is device.* as a range idx_events_type serves (LIKE is
// case-insensitive in SQLite, so it can't use the index).
const deviceTypeTerm = "type >= 'device.' AND type < 'device/'"

// DeviceActivityFilter selects GET /devices/activity.
type DeviceActivityFilter struct {
	// Fingerprint narrows to one device: device.* events naming it, and
	// events whose actor is it (by its short form, which survives a
	// rename). Empty: every device.
	Fingerprint string
	// Short is Fingerprint's short form (pkg/devicekey), as the actor
	// carries it.
	Short string
	// Before, when nonzero, returns only events with seq below it: the
	// next page.
	Before int64
	Limit  int
}

// MaxDeviceActivity caps one page.
const MaxDeviceActivity = 500

// DeviceActivity is the device activity log, newest first.
func (l *Log) DeviceActivity(ctx context.Context, f DeviceActivityFilter) ([]contracts.Envelope, error) {
	if f.Limit <= 0 || f.Limit > MaxDeviceActivity {
		f.Limit = MaxDeviceActivity
	}
	before := f.Before
	if before <= 0 {
		before = 1<<63 - 1
	}
	// Two indexed walks back from the newest, merged: device.* by type (a
	// range, which idx_events_type serves; LIKE is case-insensitive and
	// can't), and device actors by the partial index.
	q1 := `SELECT seq, timestamp, type, session_id, task_id, actor, payload FROM events
	       WHERE ` + deviceTypeTerm + ` AND seq < ?`
	args1 := []any{before}
	if f.Fingerprint != "" {
		q1 += ` AND json_extract(payload, '$.fingerprint') = ?`
		args1 = append(args1, f.Fingerprint)
	}
	q1 += ` ORDER BY seq DESC LIMIT ?`
	args1 = append(args1, f.Limit)

	q2 := `SELECT seq, timestamp, type, session_id, task_id, actor, payload FROM events
	       WHERE ` + deviceActorTerm + ` AND seq < ?`
	args2 := []any{before}
	if f.Fingerprint != "" {
		q2 += ` AND actor LIKE ?`
		args2 = append(args2, "device:%/"+f.Short)
	}
	q2 += ` ORDER BY seq DESC LIMIT ?`
	args2 = append(args2, f.Limit)

	a, err := l.scanEvents(ctx, q1, args1...)
	if err != nil {
		return nil, err
	}
	b, err := l.scanEvents(ctx, q2, args2...)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	out := make([]contracts.Envelope, 0, len(a)+len(b))
	for _, env := range append(a, b...) {
		if !seen[env.Seq] {
			seen[env.Seq] = true
			out = append(out, env)
		}
	}
	slices.SortFunc(out, func(x, y contracts.Envelope) int {
		switch {
		case x.Seq > y.Seq:
			return -1
		case x.Seq < y.Seq:
			return 1
		}
		return 0
	})
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// lastUsedQuery reads each device actor's newest event off
// idx_events_device_actor_last, in actor order: no sort.
const lastUsedQuery = `SELECT actor, MAX(seq), timestamp FROM events WHERE ` + deviceActorTerm + ` GROUP BY actor`

// DeviceLastUsed is each device actor's newest event: when each device
// last did something here. Computed from the log on read; nothing is
// written per request.
func (l *Log) DeviceLastUsed(ctx context.Context) ([]contracts.DeviceLastUsed, error) {
	rows, err := l.db.QueryContext(ctx, lastUsedQuery)
	if err != nil {
		return nil, fmt.Errorf("log: device last used: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []contracts.DeviceLastUsed{}
	for rows.Next() {
		var (
			u  contracts.DeviceLastUsed
			ts string
		)
		if err := rows.Scan(&u.Actor, &u.Seq, &ts); err != nil {
			return nil, fmt.Errorf("log: scan: %w", err)
		}
		if u.At, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			return nil, fmt.Errorf("log: bad timestamp on seq %d: %w", u.Seq, err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (l *Log) scanEvents(ctx context.Context, q string, args ...any) ([]contracts.Envelope, error) {
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("log: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []contracts.Envelope
	for rows.Next() {
		var (
			env               contracts.Envelope
			ts                string
			sessionID, taskID sql.NullString
			payload           sql.NullString
		)
		if err := rows.Scan(&env.Seq, &ts, &env.Type, &sessionID, &taskID, &env.Actor, &payload); err != nil {
			return nil, fmt.Errorf("log: scan: %w", err)
		}
		if env.Timestamp, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			return nil, fmt.Errorf("log: bad timestamp on seq %d: %w", env.Seq, err)
		}
		env.SessionID, env.TaskID = sessionID.String, taskID.String
		if payload.Valid {
			env.Payload = storedPayload(env.Seq, payload.String)
		}
		out = append(out, env)
	}
	return out, rows.Err()
}

// MeshSettingsActor is the only actor device.settings_changed is read
// from: SetMeshSettings writes under it, with the user who made the change
// in the payload (contracts.MeshSettingsChange.By), and POST /events
// refuses device.* outright, so nothing else can stand in for a setting.
const MeshSettingsActor = "settings"

// meshSettingsScan bounds how many older events MeshSettings looks back
// through past ones it can't read.
const meshSettingsScan = 100

// MeshSettings is this host's mesh settings: the newest readable
// device.settings_changed written by SetMeshSettings, the zero value when
// there's none (docs/MESH-AUTH-DESIGN.md, "Only accept devices on my
// tailnet"). The event is the only copy: the log is the setting's history
// and its current value at once, the way mesh.listening is this host's
// address. One it can't decode (or that sets nothing) is skipped for the
// next older one, so a damaged row can't wedge the setting.
func (l *Log) MeshSettings(ctx context.Context) (contracts.MeshSettings, error) {
	evs, err := l.scanEvents(ctx, `SELECT seq, timestamp, type, session_id, task_id, actor, payload FROM events
	       WHERE type = ? AND actor = ? ORDER BY seq DESC LIMIT ?`, string(contracts.DeviceSettingsChanged), MeshSettingsActor, meshSettingsScan)
	if err != nil {
		return contracts.MeshSettings{}, err
	}
	for _, ev := range evs {
		var c contracts.MeshSettingsChange
		if json.Unmarshal(ev.Payload, &c) != nil || c.RequireTailnet == nil {
			log.Printf("store: skipping unreadable device.settings_changed seq %d", ev.Seq)
			continue
		}
		return c.MeshSettings, nil
	}
	return contracts.MeshSettings{}, nil
}

// SetMeshSettings records s as this host's mesh settings, made by `by`.
// The caller checks authority: only this host's own user changes them.
func (l *Log) SetMeshSettings(ctx context.Context, s contracts.MeshSettings, by string) error {
	raw, err := json.Marshal(contracts.MeshSettingsChange{MeshSettings: s, By: by})
	if err != nil {
		return err
	}
	return l.Append(ctx, &contracts.Envelope{Type: contracts.DeviceSettingsChanged, Actor: MeshSettingsActor, Payload: raw})
}
