package contracts

import (
	"encoding/json"
	"reflect"
	"sort"
	"time"
)

// ProfileSnapshot is a full-config snapshot, captured per-field
// (profiles-v2): keyed by the same JSON field name Config uses, present
// only for fields captured at save time. Storing per-field rather than a
// frozen Config blob means a Config field introduced after a snapshot was
// saved is simply absent from it — activating that snapshot leaves the
// live value for that field untouched, no migration needed when Config
// grows.
type ProfileSnapshot map[string]json.RawMessage

// ProfileExcludedFields lists the Config JSON field names never captured in
// a profile snapshot — install state, not a profile-able setting.
// onboarding_complete and update_feed_url track this install's own
// progress; active_profile_id is written by activation itself, never one of
// the fields a snapshot restores.
var ProfileExcludedFields = map[string]bool{
	"onboarding_complete": true,
	"update_feed_url":     true,
	"active_profile_id":   true,
}

// Profile is a named, switchable full-config snapshot (profiles-v2):
// activating one restores every field the snapshot captured onto the live
// Config, leaving excluded fields and any field the snapshot predates
// untouched.
type Profile struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Snapshot    ProfileSnapshot `json:"snapshot"`
	System      bool            `json:"system,omitempty"` // ships with mads; editable, not deletable
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// configFields decomposes cfg into its raw per-field JSON representation,
// keyed by JSON tag — the shared decomposition SnapshotConfig, ApplySnapshot,
// and ProfileDrift all build on, so field boundaries always come from
// Config's own json tags rather than a second, drift-prone list.
func configFields(cfg *Config) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// SnapshotConfig captures every non-excluded field of cfg into a sparse
// profile snapshot — the "save current settings as profile" / "update
// profile" capture path (profiles-v2).
func SnapshotConfig(cfg *Config) (ProfileSnapshot, error) {
	fields, err := configFields(cfg)
	if err != nil {
		return nil, err
	}
	snap := make(ProfileSnapshot, len(fields))
	for k, v := range fields {
		if ProfileExcludedFields[k] {
			continue
		}
		snap[k] = v
	}
	return snap, nil
}

// ApplySnapshot returns a copy of cfg with every field snap captured
// overlaid; any field snap doesn't mention — an excluded field, or one
// introduced after snap was saved — keeps cfg's own current value
// (profiles-v2 activation semantics).
func ApplySnapshot(cfg *Config, snap ProfileSnapshot) (*Config, error) {
	fields, err := configFields(cfg)
	if err != nil {
		return nil, err
	}
	for k, v := range snap {
		if ProfileExcludedFields[k] {
			continue // defense in depth: snapshots shouldn't carry these anyway
		}
		fields[k] = v
	}
	merged, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	var out Config
	if err := json.Unmarshal(merged, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ProfileDrift reports which fields snap captures differ from cfg's current
// value — the "does live Config differ from the active profile's snapshot"
// answer (profiles-v2), sorted for stable output. A field snap doesn't
// mention (introduced after the snapshot was saved) never counts as drift —
// there's nothing captured to compare it against. Nil means no drift.
func ProfileDrift(cfg *Config, snap ProfileSnapshot) ([]string, error) {
	fields, err := configFields(cfg)
	if err != nil {
		return nil, err
	}
	var drifted []string
	for k, v := range snap {
		cur, ok := fields[k]
		if !ok || !jsonEqual(cur, v) {
			drifted = append(drifted, k)
		}
	}
	sort.Strings(drifted)
	return drifted, nil
}

// jsonEqual compares two JSON values semantically — key order and
// whitespace don't matter, only the decoded value does.
func jsonEqual(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
