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

// pipelineTerminalEventTypes is queried (most recent first) to determine
// whether a session's pipeline run is still live — there is no persisted
// "is this a pipeline session" field, so this reconstructs it from the
// event log the same way Metrics.pipelineRunsForSession does for M8.
var pipelineTerminalEventTypes = []string{
	string(contracts.PipelineStarted),
	string(contracts.PipelineCompleted),
	string(contracts.PipelineCancelled),
	string(contracts.PipelineFailed),
}

// Links is the linked-sessions store (docs/LINKED-SESSIONS-DESIGN.md): a
// directed grant set from one session (granting) to another (subject).
// Deliberately not a Registry[T] — links have no name and need lookups by
// both endpoint columns plus multi-row business rules (the single-orchestrate
// -holder constraint), so this follows sessions.go's bespoke-store pattern.
type Links struct {
	log      *Log
	sessions *Sessions
}

// NewLinks wires the link store to the session store it validates against.
func NewLinks(log *Log, sessions *Sessions) *Links {
	return &Links{log: log, sessions: sessions}
}

func validateGrants(grants []contracts.Grant) *contracts.ValidationError {
	var v contracts.ValidationError
	if len(grants) == 0 {
		v.Add("grants", "must not be empty")
		return &v
	}
	for _, g := range grants {
		if !g.Valid() {
			v.Add("grants", fmt.Sprintf("unknown grant %q", g))
		}
	}
	if v.Empty() {
		return nil
	}
	return &v
}

// validateCadence defaults an unset cadence to on_signal (the spec default:
// wake the monitor only when the subject needs attention) and rejects
// anything unrecognized.
func validateCadence(link *contracts.Link) *contracts.ValidationError {
	if link.Cadence == "" {
		link.Cadence = contracts.CadenceOnSignal
	}
	if !link.Cadence.Valid() {
		var v contracts.ValidationError
		v.Add("cadence", fmt.Sprintf("unknown cadence %q", link.Cadence))
		return &v
	}
	return nil
}

// Create validates and stores a link, emitting link.created. Validation
// order (docs/LINKED-SESSIONS-DESIGN.md, existence first — everything else
// needs both rows): grant shape, both sessions exist, no self-link, no
// reverse-direction duplicate (leader→subject only), the same-workspace
// different-worktree constraint, orchestrate-vs-live-pipeline-subject, and
// at-most-one-orchestrate-holder-per-subject.
//
// A directed pair holds at most one link, so a create for a pair that
// already has one merges into it instead of adding a parallel row (two rows
// both carrying read would double every digest and signal):
//   - same grant set, and cadence unset or unchanged: idempotent success. The
//     existing link is returned and no event is emitted.
//   - otherwise the existing link is updated in place (same id) to the
//     requested grants, and to the requested cadence if one was given, with
//     link.updated. Orchestrate checks run when the update adds orchestrate.
//
// Either way *link is overwritten with the stored row.
func (l *Links) Create(ctx context.Context, link *contracts.Link, actor string) error {
	if verr := validateGrants(link.Grants); verr != nil {
		return verr
	}
	cadenceGiven := link.Cadence != ""
	if verr := validateCadence(link); verr != nil {
		return verr
	}
	return l.log.WithTx(ctx, func(tx *Tx) error {
		granting, err := l.sessions.getInTx(tx, link.GrantingSessionID)
		if err != nil {
			return err
		}
		subject, err := l.sessions.getInTx(tx, link.SubjectSessionID)
		if err != nil {
			return err
		}

		var v contracts.ValidationError
		if link.GrantingSessionID == link.SubjectSessionID {
			v.Add("subject_session_id", "a session cannot link to itself")
		}

		reverseExists, err := linkExistsInTx(tx, link.SubjectSessionID, link.GrantingSessionID)
		if err != nil {
			return err
		}
		if reverseExists {
			v.Add("direction", "a link already exists in the opposite direction — peer/bidirectional links aren't supported")
		}

		if worktreeConflict(granting, subject) {
			v.Add("worktree", "sessions are isolated in different worktrees of the same workspace — linked sessions must not operate in different worktrees of the same repo")
		}

		existing, err := pairLinkInTx(tx, link.GrantingSessionID, link.SubjectSessionID)
		if err != nil {
			return err
		}
		if existing != nil && sameGrantSet(existing.Grants, link.Grants) &&
			(!cadenceGiven || existing.Cadence == link.Cadence) && v.Empty() {
			*link = *existing
			return nil
		}

		addsOrchestrate := hasGrant(link.Grants, contracts.GrantOrchestrate) &&
			(existing == nil || !hasGrant(existing.Grants, contracts.GrantOrchestrate))
		if addsOrchestrate {
			live, err := subjectPipelineLive(tx, link.SubjectSessionID)
			if err != nil {
				return err
			}
			if live {
				v.Add("grants", "orchestrate is refused: the subject session has a live pipeline run (pipeline runs are monitorable, not leadable)")
			}
			excludeID := ""
			if existing != nil {
				excludeID = existing.ID
			}
			if msg, err := l.orchestrateHolderConflictInTx(tx, link.SubjectSessionID, excludeID); err != nil {
				return err
			} else if msg != "" {
				v.Add("grants", msg)
			}
		}

		if !v.Empty() {
			return &v
		}

		if existing != nil {
			return mergeIntoExistingLinkInTx(tx, existing, link, cadenceGiven, actor)
		}

		if link.ID == "" {
			link.ID = NewID("lnk")
		}
		now := time.Now().UTC()
		link.CreatedAt, link.UpdatedAt = now, now
		link.OrchestrateStatus = initialOrchestrateStatus(link.Grants)
		if err := writeLink(tx, link); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"granting_session_id": link.GrantingSessionID,
			"subject_session_id":  link.SubjectSessionID,
			"grants":              link.Grants,
		})
		return tx.Append(&contracts.Envelope{
			Type: contracts.LinkCreated, SessionID: link.SubjectSessionID, Actor: actor, Payload: payload,
		})
	})
}

// mergeIntoExistingLinkInTx is Create's same-pair path: it rewrites existing
// in place (keeping its id and created_at) with the requested grants, and
// with the requested cadence only if the caller gave one, then emits
// link.updated exactly as Update does. *link ends up as the stored row.
func mergeIntoExistingLinkInTx(tx *Tx, existing, link *contracts.Link, cadenceGiven bool, actor string) error {
	merged := *existing
	merged.Grants = link.Grants
	if cadenceGiven {
		merged.Cadence = link.Cadence
	}
	merged.UpdatedAt = time.Now().UTC()
	merged.OrchestrateStatus = nextOrchestrateStatus(existing, merged.Grants)
	if err := writeLink(tx, &merged); err != nil {
		return err
	}
	*link = merged
	payload, _ := json.Marshal(map[string]any{
		"granting_session_id": merged.GrantingSessionID,
		"subject_session_id":  merged.SubjectSessionID,
		"grants":              merged.Grants,
	})
	return tx.Append(&contracts.Envelope{
		Type: contracts.LinkUpdated, SessionID: merged.SubjectSessionID, Actor: actor, Payload: payload,
	})
}

// sameGrantSet compares grant sets, ignoring order and duplicates.
func sameGrantSet(a, b []contracts.Grant) bool {
	for _, g := range a {
		if !hasGrant(b, g) {
			return false
		}
	}
	for _, g := range b {
		if !hasGrant(a, g) {
			return false
		}
	}
	return true
}

// pairLinkInTx returns the non-implicit link from grantingID to subjectID,
// if one exists. If an older database already holds parallel rows for the
// pair, the oldest is the one merged into.
func pairLinkInTx(tx *Tx, grantingID, subjectID string) (*contracts.Link, error) {
	link, err := scanLinkRow(tx.QueryRow(
		`SELECT id, granting_session_id, subject_session_id, grants, cadence, implicit, orchestrate_status, orchestrate_error, created_at, updated_at
		 FROM links WHERE granting_session_id = ? AND subject_session_id = ? AND implicit = 0
		 ORDER BY created_at LIMIT 1`,
		grantingID, subjectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return link, nil
}

// initialOrchestrateStatus decides the OrchestrateStatus a link should carry
// right after writeLink for a create, or for an update that just added the
// orchestrate grant where none was held before. Every such link starts
// pending_restart — the store-defaulted half of "no silent nothing": even if
// the runner-driven live-refresh call after this write never happens (crash,
// no running child, unreached code path), the record already reads honestly
// as not-yet-live rather than blank.
func initialOrchestrateStatus(grants []contracts.Grant) contracts.OrchestrateStatus {
	if hasGrant(grants, contracts.GrantOrchestrate) {
		return contracts.OrchestratePending
	}
	return ""
}

// nextOrchestrateStatus computes the OrchestrateStatus an Update should
// write: cleared if the orchestrate grant was dropped, preserved across a
// grant-preserving update (so a live "active" or terminal "unsupported"
// status set by RefreshOrchestrate survives an unrelated cadence/other-grant
// edit), and reset to pending_restart when the grant is newly added.
func nextOrchestrateStatus(old *contracts.Link, newGrants []contracts.Grant) contracts.OrchestrateStatus {
	if !hasGrant(newGrants, contracts.GrantOrchestrate) {
		return ""
	}
	if hasGrant(old.Grants, contracts.GrantOrchestrate) && old.OrchestrateStatus != "" {
		return old.OrchestrateStatus
	}
	return contracts.OrchestratePending
}

// Update changes an existing link's grant set, emitting link.updated.
// GrantingSessionID/SubjectSessionID are immutable after creation — callers
// (the API layer) must load them from the stored row before calling Update.
// Re-validates grant shape, orchestrate-vs-live-pipeline, and the
// single-orchestrate-holder rule (excluding this link's own row); does not
// re-check existence/self-link/direction/worktree since the endpoints can't
// change.
func (l *Links) Update(ctx context.Context, link *contracts.Link, actor string) error {
	if verr := validateGrants(link.Grants); verr != nil {
		return verr
	}
	if verr := validateCadence(link); verr != nil {
		return verr
	}
	return l.log.WithTx(ctx, func(tx *Tx) error {
		old, err := getLinkInTx(tx, link.ID)
		if err != nil {
			return err
		}
		link.GrantingSessionID = old.GrantingSessionID
		link.SubjectSessionID = old.SubjectSessionID

		var v contracts.ValidationError
		if hasGrant(link.Grants, contracts.GrantOrchestrate) {
			live, err := subjectPipelineLive(tx, link.SubjectSessionID)
			if err != nil {
				return err
			}
			if live {
				v.Add("grants", "orchestrate is refused: the subject session has a live pipeline run (pipeline runs are monitorable, not leadable)")
			}
			if msg, err := l.orchestrateHolderConflictInTx(tx, link.SubjectSessionID, link.ID); err != nil {
				return err
			} else if msg != "" {
				v.Add("grants", msg)
			}
		}
		if !v.Empty() {
			return &v
		}

		link.CreatedAt = old.CreatedAt
		link.UpdatedAt = time.Now().UTC()
		link.OrchestrateStatus = nextOrchestrateStatus(old, link.Grants)
		// The reason travels with a carried-over "failed" and nothing else.
		link.OrchestrateError = ""
		if link.OrchestrateStatus == contracts.OrchestrateFailed {
			link.OrchestrateError = old.OrchestrateError
		}
		if err := writeLink(tx, link); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"granting_session_id": link.GrantingSessionID,
			"subject_session_id":  link.SubjectSessionID,
			"grants":              link.Grants,
		})
		return tx.Append(&contracts.Envelope{
			Type: contracts.LinkUpdated, SessionID: link.SubjectSessionID, Actor: actor, Payload: payload,
		})
	})
}

// Delete removes a link. No cascade concerns — nothing references a link's
// id.
func (l *Links) Delete(ctx context.Context, id, actor string) error {
	return l.log.WithTx(ctx, func(tx *Tx) error {
		old, err := getLinkInTx(tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM links WHERE id = ?", id); err != nil {
			return err
		}
		// The mailbox has no reason to outlive its link — unlike sessions,
		// where deletion deliberately leaves links dangling.
		if _, err := tx.Exec("DELETE FROM link_digests WHERE link_id = ?", id); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{
			"granting_session_id": old.GrantingSessionID,
			"subject_session_id":  old.SubjectSessionID,
		})
		return tx.Append(&contracts.Envelope{
			Type: contracts.LinkDeleted, SessionID: old.SubjectSessionID, Actor: actor, Payload: payload,
		})
	})
}

// SetOrchestrateStatus records whether a link's orchestrate grant has
// materialized as live tools — the write side the runner's Start-time
// materialization and RefreshOrchestrate cycle call after actually spawning
// or cycling the granting session's provider process. Rejects links with no
// orchestrate grant (status is meaningless there) and no-ops (no redundant
// link.updated envelope) if the status already matches.
//
// errMsg is the reason for a "failed" status and is ignored otherwise: every
// other status clears it.
func (l *Links) SetOrchestrateStatus(ctx context.Context, id string, status contracts.OrchestrateStatus, errMsg, actor string) (*contracts.Link, error) {
	if status != contracts.OrchestrateFailed {
		errMsg = ""
	}
	if !status.Valid() {
		var v contracts.ValidationError
		v.Add("orchestrate_status", fmt.Sprintf("unknown status %q", status))
		return nil, &v
	}
	var out *contracts.Link
	err := l.log.WithTx(ctx, func(tx *Tx) error {
		link, err := getLinkInTx(tx, id)
		if err != nil {
			return err
		}
		if !hasGrant(link.Grants, contracts.GrantOrchestrate) {
			var v contracts.ValidationError
			v.Add("orchestrate_status", "link does not carry the orchestrate grant")
			return &v
		}
		if link.OrchestrateStatus == status && link.OrchestrateError == errMsg {
			out = link
			return nil
		}
		link.OrchestrateStatus = status
		link.OrchestrateError = errMsg
		link.UpdatedAt = time.Now().UTC()
		if err := writeLink(tx, link); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"granting_session_id": link.GrantingSessionID,
			"subject_session_id":  link.SubjectSessionID,
			"grants":              link.Grants,
		})
		if err := tx.Append(&contracts.Envelope{
			Type: contracts.LinkUpdated, SessionID: link.SubjectSessionID, Actor: actor, Payload: payload,
		}); err != nil {
			return err
		}
		out = link
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns one link.
func (l *Links) Get(ctx context.Context, id string) (*contracts.Link, error) {
	return scanLink(l.log.db.QueryRowContext(ctx,
		`SELECT id, granting_session_id, subject_session_id, grants, cadence, implicit, orchestrate_status, orchestrate_error, created_at, updated_at
		 FROM links WHERE id = ?`, id))
}

// ForSession returns links where sessionID is on either side (granting or
// subject), ordered by creation. No existence check on the session itself —
// mirrors FileChanges/Commits: an unknown or linkless session just returns
// an empty result.
func (l *Links) ForSession(ctx context.Context, sessionID string) ([]contracts.Link, error) {
	rows, err := l.log.db.QueryContext(ctx,
		`SELECT id, granting_session_id, subject_session_id, grants, cadence, implicit, orchestrate_status, orchestrate_error, created_at, updated_at
		 FROM links WHERE granting_session_id = ? OR subject_session_id = ? ORDER BY created_at`,
		sessionID, sessionID)
	if err != nil {
		return nil, err
	}
	var out []contracts.Link
	for rows.Next() {
		link, err := scanLinkRow(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, *link)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// CreateImplicitHandRaiser creates (or returns the existing) system-granted
// self-referential hand-raiser link for sessionID
// (docs/LINKED-SESSIONS-DESIGN.md decision 6): {read,inject} from sessionID
// onto itself. Deliberately skips Create's self-link/reverse-direction/
// worktree checks — those exist to protect distinct-session links; a
// same-session record is what this method is *for*. Idempotent: a second
// call for a session that already has one returns the existing row
// untouched (no duplicate link.created).
func (l *Links) CreateImplicitHandRaiser(ctx context.Context, sessionID, actor string) (*contracts.Link, error) {
	var out *contracts.Link
	err := l.log.WithTx(ctx, func(tx *Tx) error {
		if _, err := l.sessions.getInTx(tx, sessionID); err != nil {
			return err
		}
		existing, err := implicitHandRaiserInTx(tx, sessionID)
		if err != nil {
			return err
		}
		if existing != nil {
			out = existing
			return nil
		}
		link := &contracts.Link{
			ID:                NewID("lnk"),
			GrantingSessionID: sessionID,
			SubjectSessionID:  sessionID,
			Grants:            contracts.PresetGrants[contracts.PresetHandRaiser],
			Cadence:           contracts.CadencePerTurn,
			Implicit:          true,
		}
		now := time.Now().UTC()
		link.CreatedAt, link.UpdatedAt = now, now
		if err := writeLink(tx, link); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"granting_session_id": link.GrantingSessionID,
			"subject_session_id":  link.SubjectSessionID,
			"grants":              link.Grants,
		})
		if err := tx.Append(&contracts.Envelope{
			Type: contracts.LinkCreated, SessionID: link.SubjectSessionID, Actor: actor, Payload: payload,
		}); err != nil {
			return err
		}
		out = link
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RemoveImplicitHandRaiser deletes sessionID's implicit hand-raiser link, if
// any (decision 6: auto-removed when the run completes). No-op, successful
// if none exists.
func (l *Links) RemoveImplicitHandRaiser(ctx context.Context, sessionID, actor string) error {
	return l.log.WithTx(ctx, func(tx *Tx) error {
		existing, err := implicitHandRaiserInTx(tx, sessionID)
		if err != nil {
			return err
		}
		if existing == nil {
			return nil
		}
		if _, err := tx.Exec("DELETE FROM links WHERE id = ?", existing.ID); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{
			"granting_session_id": existing.GrantingSessionID,
			"subject_session_id":  existing.SubjectSessionID,
		})
		return tx.Append(&contracts.Envelope{
			Type: contracts.LinkDeleted, SessionID: existing.SubjectSessionID, Actor: actor, Payload: payload,
		})
	})
}

// implicitHandRaiserInTx looks up sessionID's implicit hand-raiser link, if
// any — backs both CreateImplicitHandRaiser's idempotency check and
// RemoveImplicitHandRaiser's lookup.
func implicitHandRaiserInTx(tx *Tx, sessionID string) (*contracts.Link, error) {
	link, err := scanLinkRow(tx.QueryRow(
		`SELECT id, granting_session_id, subject_session_id, grants, cadence, implicit, orchestrate_status, orchestrate_error, created_at, updated_at
		 FROM links WHERE granting_session_id = ? AND subject_session_id = ? AND implicit = 1`,
		sessionID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return link, nil
}

func hasGrant(grants []contracts.Grant, g contracts.Grant) bool {
	for _, v := range grants {
		if v == g {
			return true
		}
	}
	return false
}

// worktreeConflict reports the same-workspace-different-worktree constraint
// (docs/LINKED-SESSIONS-DESIGN.md "Unchanged from §1.12": linked sessions
// must not operate in different worktrees of the same repo). contracts.Session
// has no persisted "which repo is this" field — WorktreeDir is the isolated
// worktree path (<root>/<sessionID>, see pkg/sessionwt), not the origin repo,
// and the origin repo path is never written back onto the session record.
// Workspace (the declared workspace name) is the closest available proxy for
// "same repo": two sessions from the same workspace share that workspace's
// pinned primary repo (spec §17: "exactly one repo must be primary"). Known
// gap: two ad-hoc sessions isolated from the same directory without a shared
// Workspace record aren't caught here — there's no schema field to catch
// that case today (the currently-off MultiRepo flag's territory, out of
// scope for this task).
func worktreeConflict(a, b *contracts.Session) bool {
	if a.WorktreeDir == "" || b.WorktreeDir == "" {
		return false
	}
	if a.Workspace == "" || a.Workspace != b.Workspace {
		return false
	}
	return a.WorktreeDir != b.WorktreeDir
}

// subjectPipelineLive reports whether sessionID's most recent pipeline
// lifecycle event is pipeline.started (i.e. no terminal event followed it) —
// reconstructed from the event log the same way
// Metrics.pipelineRunsForSession does for M8.
func subjectPipelineLive(tx *Tx, sessionID string) (bool, error) {
	var typ string
	err := tx.QueryRow(
		`SELECT type FROM events WHERE session_id = ? AND type IN (?, ?, ?, ?) ORDER BY seq DESC LIMIT 1`,
		sessionID, pipelineTerminalEventTypes[0], pipelineTerminalEventTypes[1],
		pipelineTerminalEventTypes[2], pipelineTerminalEventTypes[3],
	).Scan(&typ)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return typ == string(contracts.PipelineStarted), nil
}

// orchestrateHolderConflictInTx returns the validation message for
// subjectID already having an orchestrate holder other than excludeLinkID
// (the link being updated or merged into), or "" if it has none. The message
// names the holder session so the refusal is actionable. A holder whose
// session record is gone (session deletion leaves links dangling) is named
// by id alone.
func (l *Links) orchestrateHolderConflictInTx(tx *Tx, subjectID, excludeLinkID string) (string, error) {
	holderID, err := orchestrateHolderInTx(tx, subjectID, excludeLinkID)
	if err != nil || holderID == "" {
		return "", err
	}
	holder := holderID
	if sess, err := l.sessions.getInTx(tx, holderID); err == nil && sess.Name != "" {
		holder = fmt.Sprintf("%q (%s)", sess.Name, holderID)
	}
	return fmt.Sprintf("the subject is already led by %s — a subject may have at most one orchestrate holder", holder), nil
}

// orchestrateHolderInTx returns the granting session id of subjectID's
// orchestrate-carrying link other than excludeLinkID, or "" if there is
// none.
func orchestrateHolderInTx(tx *Tx, subjectID, excludeLinkID string) (string, error) {
	rows, err := tx.tx.Query(
		`SELECT id, granting_session_id, grants FROM links WHERE subject_session_id = ? ORDER BY created_at`, subjectID)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, grantingID, grantsJSON string
		if err := rows.Scan(&id, &grantingID, &grantsJSON); err != nil {
			return "", err
		}
		if id == excludeLinkID {
			continue
		}
		var grants []contracts.Grant
		if err := json.Unmarshal([]byte(grantsJSON), &grants); err != nil {
			return "", err
		}
		if hasGrant(grants, contracts.GrantOrchestrate) {
			return grantingID, nil
		}
	}
	return "", rows.Err()
}

func linkExistsInTx(tx *Tx, grantingID, subjectID string) (bool, error) {
	var n int
	err := tx.QueryRow(
		`SELECT COUNT(*) FROM links WHERE granting_session_id = ? AND subject_session_id = ?`,
		grantingID, subjectID,
	).Scan(&n)
	return n > 0, err
}

// linkDigestCap bounds one link's mailbox: appending past it drops the
// oldest entries first. The kept rows' FromTurn anchors still tell a reader
// where coverage begins, so no explicit "dropped" marker is needed.
const linkDigestCap = 32

// AppendDigest queues one compressed digest on link id's mailbox
// (docs/LINKED-SESSIONS-DESIGN.md decision 4, the on_signal accumulation
// half), enforcing linkDigestCap by dropping oldest-first. No event is
// emitted — mailbox traffic is internal plumbing between the Router's
// digest job and the next signal wake, not activity-feed material.
func (l *Links) AppendDigest(ctx context.Context, id string, d *contracts.LinkDigest) error {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	return l.log.WithTx(ctx, func(tx *Tx) error {
		if _, err := getLinkInTx(tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO link_digests (link_id, text, from_turn, to_turn, created_at) VALUES (?, ?, ?, ?, ?)`,
			id, d.Text, d.FromTurn, d.ToTurn, d.CreatedAt.Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
		_, err := tx.Exec(
			`DELETE FROM link_digests WHERE link_id = ? AND id NOT IN
			 (SELECT id FROM link_digests WHERE link_id = ? ORDER BY id DESC LIMIT ?)`,
			id, id, linkDigestCap,
		)
		return err
	})
}

// DrainDigests atomically returns and clears link id's mailbox, oldest
// first. An unknown link drains empty rather than erroring — the caller is
// always on a best-effort delivery path where the link may have been
// deleted mid-flight.
func (l *Links) DrainDigests(ctx context.Context, id string) ([]contracts.LinkDigest, error) {
	var out []contracts.LinkDigest
	err := l.log.WithTx(ctx, func(tx *Tx) error {
		rows, err := tx.Query(
			`SELECT text, from_turn, to_turn, created_at FROM link_digests WHERE link_id = ? ORDER BY id`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var (
				d         contracts.LinkDigest
				createdAt string
			)
			if err := rows.Scan(&d.Text, &d.FromTurn, &d.ToTurn, &createdAt); err != nil {
				_ = rows.Close()
				return err
			}
			d.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
			out = append(out, d)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM link_digests WHERE link_id = ?`, id)
		return err
	})
	return out, err
}

func getLinkInTx(tx *Tx, id string) (*contracts.Link, error) {
	return scanLink(tx.QueryRow(
		`SELECT id, granting_session_id, subject_session_id, grants, cadence, implicit, orchestrate_status, orchestrate_error, created_at, updated_at
		 FROM links WHERE id = ?`, id))
}

func writeLink(tx *Tx, link *contracts.Link) error {
	grantsJSON, err := json.Marshal(link.Grants)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO links (id, granting_session_id, subject_session_id, grants, cadence, implicit, orchestrate_status, orchestrate_error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   grants = excluded.grants, cadence = excluded.cadence, orchestrate_status = excluded.orchestrate_status,
		   orchestrate_error = excluded.orchestrate_error, updated_at = excluded.updated_at`,
		link.ID, link.GrantingSessionID, link.SubjectSessionID, string(grantsJSON), string(link.Cadence),
		boolToInt(link.Implicit), string(link.OrchestrateStatus), link.OrchestrateError,
		link.CreatedAt.Format(time.RFC3339Nano), link.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanLink(row rowScanner) (*contracts.Link, error) {
	link, err := scanLinkRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("links: not found: %w", ErrNotFound)
	}
	return link, err
}

func scanLinkRow(row rowScanner) (*contracts.Link, error) {
	var (
		link              contracts.Link
		grantsJSON        string
		cadence           string
		implicit          int
		orchestrateStatus string
		createdAt         string
		updatedAt         string
	)
	if err := row.Scan(&link.ID, &link.GrantingSessionID, &link.SubjectSessionID, &grantsJSON, &cadence, &implicit, &orchestrateStatus, &link.OrchestrateError, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	link.Cadence = contracts.LinkCadence(cadence)
	link.Implicit = implicit != 0
	link.OrchestrateStatus = contracts.OrchestrateStatus(orchestrateStatus)
	if err := json.Unmarshal([]byte(grantsJSON), &link.Grants); err != nil {
		return nil, err
	}
	var err error
	if link.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, err
	}
	if link.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return nil, err
	}
	return &link, nil
}
