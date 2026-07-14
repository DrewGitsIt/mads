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

// SetCapabilityHealth updates a capability's recorded health, appending
// capability.health_changed ONLY when Healthy actually flips (spec §12.6) —
// so concurrent sessions repeatedly reporting the same status don't flood
// the event log. reason may change on a re-report without healthy changing;
// that updates CheckedAt/Reason silently, no event.
func SetCapabilityHealth(ctx context.Context, caps *Registry[contracts.Capability], idOrName string, healthy bool, reason, actor string) (*contracts.Capability, error) {
	var out contracts.Capability
	err := caps.log.WithTx(ctx, func(tx *Tx) error {
		var id, data string
		err := tx.QueryRow(
			"SELECT id, data FROM capabilities WHERE id = ? OR name = ?", idOrName, idOrName,
		).Scan(&id, &data)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("capabilities: %s: %w", idOrName, ErrNotFound)
		}
		if err != nil {
			return err
		}

		var cur contracts.Capability
		if err := json.Unmarshal([]byte(data), &cur); err != nil {
			return fmt.Errorf("capabilities: corrupt data for %s: %w", idOrName, err)
		}

		flipped := cur.Health.Healthy != healthy
		cur.Health = contracts.CapabilityHealth{
			Healthy:   healthy,
			Reason:    reason,
			CheckedAt: time.Now().UTC(),
		}

		newData, err := json.Marshal(&cur)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE capabilities SET data = ? WHERE id = ?", string(newData), id); err != nil {
			return err
		}

		if flipped {
			payload, _ := json.Marshal(map[string]any{
				"capability": cur.Name,
				"healthy":    healthy,
				"reason":     reason,
			})
			if err := tx.Append(&contracts.Envelope{
				Type: contracts.CapabilityHealthChanged, Actor: actor, Payload: payload,
			}); err != nil {
				return err
			}
		}

		out = cur
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
