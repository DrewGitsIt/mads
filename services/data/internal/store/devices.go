package store

import (
	"context"
	"database/sql"
	"fmt"
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
//     device's own actions here;
//   - a paired device's path changing while connected
//     (peer.reachability_changed from and to connected), by its peer id.
//
// Discovery probes and the setup window's renewals are left out.
//
// deviceActorTerm is the partial index's expression (migration 0036): a
// query must use it verbatim for SQLite to use that index.
const deviceActorTerm = "substr(actor, 1, 7) = 'device:'"

// deviceTypeTerm is device.* as a range idx_events_type serves (LIKE is
// case-insensitive in SQLite, so it can't use the index).
const deviceTypeTerm = "type >= 'device.' AND type < 'device/'"

// deviceShownTerm leaves out the device.* events the log doesn't show, so
// they never push shown ones off a page: discovery probes (audit only) and
// the setup window's minute renewals.
const deviceShownTerm = "type != 'device.discovered' AND NOT (type = 'device.discoverable' AND coalesce(json_extract(payload, '$.renew'), 0) = 1)"

// pathChangeTerm is a connected device's path changing ("Now reached via
// Tailscale (was home network)"): peer.reachability_changed from and to
// connected.
const pathChangeTerm = "type = 'peer.reachability_changed' AND json_extract(payload, '$.from') = 'connected' AND json_extract(payload, '$.to') = 'connected'"

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
	       WHERE ` + deviceTypeTerm + ` AND ` + deviceShownTerm + ` AND seq < ?`
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

	q3 := `SELECT seq, timestamp, type, session_id, task_id, actor, payload FROM events
	       WHERE ` + pathChangeTerm + ` AND seq < ?`
	args3 := []any{before}
	if f.Fingerprint != "" {
		q3 += ` AND json_extract(payload, '$.peer_id') IN (SELECT id FROM mesh_peers WHERE json_extract(data, '$.fingerprint') = ?)`
		args3 = append(args3, f.Fingerprint)
	}
	q3 += ` ORDER BY seq DESC LIMIT ?`
	args3 = append(args3, f.Limit)

	a, err := l.scanEvents(ctx, q1, args1...)
	if err != nil {
		return nil, err
	}
	b, err := l.scanEvents(ctx, q2, args2...)
	if err != nil {
		return nil, err
	}
	c, err := l.scanEvents(ctx, q3, args3...)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	out := make([]contracts.Envelope, 0, len(a)+len(b)+len(c))
	for _, env := range append(append(a, b...), c...) {
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
