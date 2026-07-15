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
			if p.Settings.SynthesisMode != "" && !p.Settings.SynthesisMode.Valid() {
				v.Add("settings.synthesis_mode", fmt.Sprintf("unknown synthesis mode %q", p.Settings.SynthesisMode))
			}
			if p.Settings.AgentSelectionMode != "" && !p.Settings.AgentSelectionMode.Valid() {
				v.Add("settings.agent_selection_mode", fmt.Sprintf("unknown agent selection mode %q", p.Settings.AgentSelectionMode))
			}
			if p.Settings.DefaultModelTier != "" && !p.Settings.DefaultModelTier.Valid() {
				v.Add("settings.default_model_tier", fmt.Sprintf("unknown model tier %q", p.Settings.DefaultModelTier))
			}
			if v.Empty() {
				return nil
			}
			return &v
		},
	}
}

// ActivateProfile merges a profile's non-zero ProfileSettings fields onto
// the current Config and persists the result, all in one transaction —
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

		mergeSettings(&cfg, &profile.Settings)
		if verr := contracts.ValidateConfig(&cfg); !verr.Empty() {
			return verr
		}
		if err := writeConfig(tx, &cfg, actor); err != nil {
			return err
		}
		merged = cfg
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &merged, nil
}

// mergeSettings overlays every non-zero field of s onto cfg.
func mergeSettings(cfg *contracts.Config, s *contracts.ProfileSettings) {
	if s.SynthesisMode != "" {
		cfg.SynthesisMode = s.SynthesisMode
	}
	if s.AgentSelectionMode != "" {
		cfg.AgentSelectionMode = s.AgentSelectionMode
	}
	if s.DefaultModelTier != "" {
		cfg.DefaultModelTier = s.DefaultModelTier
	}
	if s.DefaultWorkspace != "" {
		cfg.DefaultWorkspace = s.DefaultWorkspace
	}
	if s.DefaultProvider != "" {
		cfg.DefaultProvider = s.DefaultProvider
	}
	if s.DefaultTrust != "" {
		cfg.DefaultTrust = s.DefaultTrust
	}
	if s.DefaultTemplate != "" {
		cfg.DefaultTemplate = s.DefaultTemplate
	}
	if s.FastModel != "" {
		cfg.FastModel = s.FastModel
	}
	if s.ConcurrencyLimit != 0 {
		cfg.ConcurrencyLimit = s.ConcurrencyLimit
	}
}
