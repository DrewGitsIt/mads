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

// Retros is the retro store (spec §7): retrospective analyses and the DPAs
// they produced.
type Retros struct {
	log *Log
}

// NewRetros wraps the event log's database.
func NewRetros(log *Log) *Retros {
	return &Retros{log: log}
}

// Create stores a completed retro and announces it. Unlike Sessions.Create,
// the ID is not minted here: the caller (the Router's retro job) mints it up
// front via job.NewRetroID so progress events can carry it before the row
// exists.
func (s *Retros) Create(ctx context.Context, r *contracts.Retro) error {
	if r.ID == "" {
		var v contracts.ValidationError
		v.Add("id", "required")
		return &v
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		now := time.Now().UTC()
		r.CreatedAt, r.UpdatedAt = now, now
		if err := s.write(tx, r); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"retro_id": r.ID, "dpa_count": len(r.DPAs)})
		return tx.Append(&contracts.Envelope{
			Type: contracts.RetroCompleted, Actor: "pipeline", Payload: payload,
		})
	})
}

// Get returns one retro.
func (s *Retros) Get(ctx context.Context, id string) (contracts.Retro, error) {
	var data string
	err := s.log.db.QueryRowContext(ctx, "SELECT data FROM retros WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return contracts.Retro{}, fmt.Errorf("retros: %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return contracts.Retro{}, err
	}
	var r contracts.Retro
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		return contracts.Retro{}, fmt.Errorf("retros: corrupt data for %s: %w", id, err)
	}
	return r, nil
}

// List returns every retro, newest first.
func (s *Retros) List(ctx context.Context) ([]contracts.Retro, error) {
	rows, err := s.log.db.QueryContext(ctx, "SELECT data FROM retros ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	var out []contracts.Retro
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var r contracts.Retro
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// AcceptDPA marks a DPA accepted and announces it.
func (s *Retros) AcceptDPA(ctx context.Context, retroID, dpaID string) error {
	return s.setDPAStatus(ctx, retroID, dpaID, contracts.DPAAccepted, contracts.RetroDPAAccepted)
}

// RejectDPA marks a DPA rejected and announces it.
func (s *Retros) RejectDPA(ctx context.Context, retroID, dpaID string) error {
	return s.setDPAStatus(ctx, retroID, dpaID, contracts.DPARejected, contracts.RetroDPARejected)
}

func (s *Retros) setDPAStatus(ctx context.Context, retroID, dpaID string, status contracts.DPAStatus, eventType contracts.EventType) error {
	return s.log.WithTx(ctx, func(tx *Tx) error {
		r, err := s.getInTx(tx, retroID)
		if err != nil {
			return err
		}
		idx, err := findDPA(r, dpaID)
		if err != nil {
			return err
		}
		r.DPAs[idx].Status = status
		r.UpdatedAt = time.Now().UTC()
		if err := s.write(tx, r); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"retro_id": retroID, "dpa_id": dpaID})
		return tx.Append(&contracts.Envelope{Type: eventType, Actor: "user", Payload: payload})
	})
}

// EditDPA applies non-empty fields from edit to a DPA, marks it edited, and
// returns the updated DPA.
func (s *Retros) EditDPA(ctx context.Context, retroID, dpaID string, edit contracts.DPAEdit) (contracts.DPA, error) {
	var updated contracts.DPA
	err := s.log.WithTx(ctx, func(tx *Tx) error {
		r, err := s.getInTx(tx, retroID)
		if err != nil {
			return err
		}
		idx, err := findDPA(r, dpaID)
		if err != nil {
			return err
		}
		dpa := &r.DPAs[idx]
		if edit.Problem != "" {
			dpa.Problem = edit.Problem
		}
		if edit.Evidence != "" {
			dpa.Evidence = edit.Evidence
		}
		if edit.RootCause != "" {
			dpa.RootCause = edit.RootCause
		}
		if edit.RemediationLayer != "" {
			dpa.RemediationLayer = edit.RemediationLayer
		}
		if edit.SuggestedAction != "" {
			dpa.SuggestedAction = edit.SuggestedAction
		}
		dpa.Status = contracts.DPAEdited
		r.UpdatedAt = time.Now().UTC()
		if err := s.write(tx, r); err != nil {
			return err
		}
		updated = *dpa
		payload, _ := json.Marshal(map[string]string{"retro_id": retroID, "dpa_id": dpaID})
		return tx.Append(&contracts.Envelope{Type: contracts.RetroDPAEdited, Actor: "user", Payload: payload})
	})
	if err != nil {
		return contracts.DPA{}, err
	}
	return updated, nil
}

// SetFollowUpSession links a retro to the follow-up session created from its
// accepted DPAs.
func (s *Retros) SetFollowUpSession(ctx context.Context, retroID, sessionID string) error {
	return s.log.WithTx(ctx, func(tx *Tx) error {
		r, err := s.getInTx(tx, retroID)
		if err != nil {
			return err
		}
		r.FollowUpSessionID = sessionID
		r.UpdatedAt = time.Now().UTC()
		if err := s.write(tx, r); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"retro_id": retroID, "session_id": sessionID})
		return tx.Append(&contracts.Envelope{Type: contracts.RetroFollowUpCreated, Actor: "pipeline", Payload: payload})
	})
}

func findDPA(r *contracts.Retro, dpaID string) (int, error) {
	for i, d := range r.DPAs {
		if d.ID == dpaID {
			return i, nil
		}
	}
	return 0, fmt.Errorf("retros: dpa %s: %w", dpaID, ErrNotFound)
}

func (s *Retros) getInTx(tx *Tx, id string) (*contracts.Retro, error) {
	var data string
	err := tx.QueryRow("SELECT data FROM retros WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("retros: %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var r contracts.Retro
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Retros) write(tx *Tx, r *contracts.Retro) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO retros (id, data, created_at, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		r.ID, string(data), r.CreatedAt.Format(time.RFC3339Nano), r.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}
