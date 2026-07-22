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

// synthesizerActor is the actor stamped on every proposal-lifecycle event
// and on every task/note mutation Approve performs — the synthesizer is the
// only writer of proposals, so unlike Tasks/Retros there is no caller-
// supplied actor to thread through.
const synthesizerActor = "synthesizer"

// Proposals is the synthesis proposal store (spec §4): create_task/add_note/
// update_task suggestions awaiting review. Approve applies a proposal
// through Tasks — the same store, and the same actor convention, that a
// human or the UI would use — so a proposal never has a side door into the
// task tables.
type Proposals struct {
	log   *Log
	tasks *Tasks
}

// NewProposals wraps the event log's database and the Tasks store Approve
// applies changes through.
func NewProposals(log *Log, tasks *Tasks) *Proposals {
	return &Proposals{log: log, tasks: tasks}
}

// validateProposal checks a proposal for storage, reporting every problem
// at once (spec §10.3).
func validateProposal(p *contracts.Proposal) *contracts.ValidationError {
	var v contracts.ValidationError
	if p.SessionID == "" {
		v.Add("session_id", "required")
	}
	if !p.Type.Valid() {
		v.Add("type", fmt.Sprintf("unknown type %q", p.Type))
	}
	switch p.Type {
	case contracts.ProposalCreateTask:
		if p.Task == nil || p.Task.Title == "" {
			v.Add("task.title", "required for create_task")
		}
	case contracts.ProposalAddNote:
		if p.TaskID == "" {
			v.Add("task_id", "required for add_note")
		}
		if p.Note == nil || p.Note.Content == "" {
			v.Add("note.content", "required for add_note")
		}
	case contracts.ProposalUpdateTask:
		if p.TaskID == "" {
			v.Add("task_id", "required for update_task")
		}
		if p.Update == nil {
			v.Add("update", "required for update_task")
		}
	}
	if v.Empty() {
		return nil
	}
	return &v
}

// Create stores a new proposal, assigning its ID and timestamps, and emits
// synthesis.proposal_created. Status is always proposed at creation —
// auto_approve, a config-level preference, is the caller's job (the
// synthesis job) to apply as a follow-up Approve call, not this store's.
func (s *Proposals) Create(ctx context.Context, p *contracts.Proposal) error {
	p.Status = contracts.ProposalProposed
	p.Source = synthesizerActor
	if verr := validateProposal(p); verr != nil {
		return verr
	}
	return s.log.WithTx(ctx, func(tx *Tx) error {
		if p.ID == "" {
			p.ID = NewID("prop")
		}
		now := time.Now().UTC()
		p.CreatedAt, p.UpdatedAt = now, now
		if err := s.write(tx, p); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"proposal_id": p.ID, "type": string(p.Type)})
		return tx.Append(&contracts.Envelope{
			Type: contracts.SynthesisProposalCreated, SessionID: p.SessionID,
			Actor: synthesizerActor, Payload: payload,
		})
	})
}

// Get returns one proposal.
func (s *Proposals) Get(ctx context.Context, id string) (*contracts.Proposal, error) {
	var data string
	err := s.log.db.QueryRowContext(ctx, "SELECT data FROM proposals WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("proposals: %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var p contracts.Proposal
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return nil, fmt.Errorf("proposals: corrupt data for %s: %w", id, err)
	}
	return &p, nil
}

// List returns a session's proposals, oldest first.
func (s *Proposals) List(ctx context.Context, sessionID string) ([]contracts.Proposal, error) {
	rows, err := s.log.db.QueryContext(ctx,
		"SELECT data FROM proposals WHERE session_id = ? ORDER BY created_at, id", sessionID)
	if err != nil {
		return nil, fmt.Errorf("proposals: list: %w", err)
	}
	var out []contracts.Proposal
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var p contracts.Proposal
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// Approve applies a proposed change through the Tasks store (actor
// "synthesizer") and marks the proposal approved. Non-proposed proposals
// (already approved or dismissed) are rejected rather than re-applied.
func (s *Proposals) Approve(ctx context.Context, id string) (*contracts.Proposal, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Status != contracts.ProposalProposed {
		verr := &contracts.ValidationError{}
		verr.Add("status", fmt.Sprintf("proposal is %s, not proposed", p.Status))
		return nil, verr
	}

	switch p.Type {
	case contracts.ProposalCreateTask:
		t := &contracts.Task{
			SessionID:          p.SessionID,
			Title:              p.Task.Title,
			Description:        p.Task.Description,
			AcceptanceCriteria: p.Task.AcceptanceCriteria,
			Scope:              p.Task.Scope,
			Source:             synthesizerActor,
		}
		if err := s.tasks.Create(ctx, t, synthesizerActor); err != nil {
			return nil, err
		}
		p.AppliedTaskID = t.ID
	case contracts.ProposalAddNote:
		n := &contracts.Note{
			Type:    p.Note.Type,
			Content: p.Note.Content,
			Source:  synthesizerActor,
		}
		if err := s.tasks.AddNote(ctx, p.TaskID, n, synthesizerActor); err != nil {
			return nil, err
		}
	case contracts.ProposalUpdateTask:
		t, err := s.tasks.Get(ctx, p.TaskID)
		if err != nil {
			return nil, err
		}
		if p.Update.Title != "" {
			t.Title = p.Update.Title
		}
		if p.Update.Description != "" {
			t.Description = p.Update.Description
		}
		if p.Update.AcceptanceCriteria != "" {
			t.AcceptanceCriteria = p.Update.AcceptanceCriteria
		}
		if p.Update.Scope != "" {
			t.Scope = p.Update.Scope
		}
		if err := s.tasks.Update(ctx, t, synthesizerActor); err != nil {
			return nil, err
		}
	}

	p.Status = contracts.ProposalApproved
	if err := s.log.WithTx(ctx, func(tx *Tx) error {
		p.UpdatedAt = time.Now().UTC()
		if err := s.write(tx, p); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"proposal_id": p.ID})
		return tx.Append(&contracts.Envelope{
			Type: contracts.SynthesisProposalApproved, SessionID: p.SessionID,
			Actor: synthesizerActor, Payload: payload,
		})
	}); err != nil {
		return nil, err
	}
	return p, nil
}

// Dismiss marks a proposal dismissed without applying any change.
func (s *Proposals) Dismiss(ctx context.Context, id string) (*contracts.Proposal, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Status != contracts.ProposalProposed {
		verr := &contracts.ValidationError{}
		verr.Add("status", fmt.Sprintf("proposal is %s, not proposed", p.Status))
		return nil, verr
	}
	p.Status = contracts.ProposalDismissed
	if err := s.log.WithTx(ctx, func(tx *Tx) error {
		p.UpdatedAt = time.Now().UTC()
		if err := s.write(tx, p); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"proposal_id": p.ID})
		return tx.Append(&contracts.Envelope{
			Type: contracts.SynthesisProposalDismissed, SessionID: p.SessionID,
			Actor: synthesizerActor, Payload: payload,
		})
	}); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Proposals) write(tx *Tx, p *contracts.Proposal) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO proposals (id, session_id, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		p.ID, p.SessionID, string(data), p.CreatedAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}
