package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Config is the single-row global configuration store (MADS-UX §3.7
// Settings). Unlike the registries, there's exactly one row — no id/name
// lookups, no delete.
type Config struct {
	log *Log
}

// NewConfig returns the config store.
func NewConfig(log *Log) *Config {
	return &Config{log: log}
}

// Get returns the current configuration.
func (c *Config) Get(ctx context.Context) (*contracts.Config, error) {
	var data string
	err := c.log.db.QueryRowContext(ctx, "SELECT data FROM config WHERE id = 1").Scan(&data)
	if err != nil {
		return nil, fmt.Errorf("config: get: %w", err)
	}
	// Start from DefaultConfig() rather than the zero Config: a field added
	// after a row was written (e.g. stream_speed on a pre-existing install)
	// is simply absent from the stored JSON, and unmarshaling onto the zero
	// value would leave it at the Go zero value instead of its documented
	// default.
	cfg := contracts.DefaultConfig()
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("config: corrupt data: %w", err)
	}
	return &cfg, nil
}

// Update validates and replaces the configuration, announcing the change.
func (c *Config) Update(ctx context.Context, cfg *contracts.Config, actor string) error {
	if verr := contracts.ValidateConfig(cfg); !verr.Empty() {
		return verr
	}
	return c.log.WithTx(ctx, func(tx *Tx) error {
		return writeConfig(tx, cfg, actor)
	})
}

// writeConfig performs the row write and event append inside an open
// transaction — shared with the profile-activation merge, which updates
// Config as part of a larger transaction rather than calling Update.
func writeConfig(tx *Tx, cfg *contracts.Config, actor string) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(
		`UPDATE config SET data = ?, updated_at = ? WHERE id = 1`, string(data), now,
	); err != nil {
		return err
	}
	return tx.Append(&contracts.Envelope{Type: contracts.ConfigUpdated, Actor: actor})
}
