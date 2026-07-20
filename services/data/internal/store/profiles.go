package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// NewProfiles returns the profile registry (MADS-UX §10.5).
func NewProfiles(log *Log) *Registry[contracts.Profile] {
	return &Registry[contracts.Profile]{
		log: log, table: "profiles", prefix: "prf",
		created: contracts.ProfileCreated, updated: contracts.ProfileUpdated, deleted: contracts.ProfileDeleted,
		id:     func(p *contracts.Profile) *string { return &p.ID },
		name:   func(p *contracts.Profile) string { return p.Name },
		system: func(p *contracts.Profile) bool { return p.System },
		times: func(p *contracts.Profile) (*time.Time, *time.Time) {
			return &p.CreatedAt, &p.UpdatedAt
		},
		validate: func(p *contracts.Profile) *contracts.ValidationError {
			var v contracts.ValidationError
			if p.Name == "" {
				v.Add("name", "required")
			}
			if v.Empty() {
				return nil
			}
			return &v
		},
	}
}

// ActivateProfile restores a profile's captured snapshot fields onto the
// current Config and persists the result, all in one transaction —
// activation is a single atomic config change, not a separate profile
// event plus a config event racing other writers.
func ActivateProfile(ctx context.Context, log *Log, profileID, actor string) (*contracts.Config, error) {
	var merged contracts.Config
	err := log.WithTx(ctx, func(tx *Tx) error {
		var profileData string
		err := tx.QueryRow("SELECT data FROM profiles WHERE id = ? OR name = ?", profileID, profileID).Scan(&profileData)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("profiles: %s: %w", profileID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		var profile contracts.Profile
		if err := json.Unmarshal([]byte(profileData), &profile); err != nil {
			return fmt.Errorf("profiles: corrupt data for %s: %w", profileID, err)
		}

		var configData string
		if err := tx.QueryRow("SELECT data FROM config WHERE id = 1").Scan(&configData); err != nil {
			return fmt.Errorf("config: get: %w", err)
		}
		var cfg contracts.Config
		if err := json.Unmarshal([]byte(configData), &cfg); err != nil {
			return fmt.Errorf("config: corrupt data: %w", err)
		}

		applied, err := contracts.ApplySnapshot(&cfg, profile.Snapshot)
		if err != nil {
			return fmt.Errorf("profiles: apply snapshot: %w", err)
		}
		applied.ActiveProfileID = profile.ID
		if verr := contracts.ValidateConfig(applied); !verr.Empty() {
			return verr
		}
		if err := writeConfig(tx, applied, actor); err != nil {
			return err
		}
		merged = *applied
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &merged, nil
}

// CaptureConfigSnapshot reads the current Config and captures it into a
// sparse profile snapshot (profiles-v2 "save current settings as profile" /
// "update profile" capture path).
func CaptureConfigSnapshot(ctx context.Context, log *Log) (contracts.ProfileSnapshot, error) {
	var data string
	if err := log.db.QueryRowContext(ctx, "SELECT data FROM config WHERE id = 1").Scan(&data); err != nil {
		return nil, fmt.Errorf("config: get: %w", err)
	}
	var cfg contracts.Config
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("config: corrupt data: %w", err)
	}
	return contracts.SnapshotConfig(&cfg)
}
