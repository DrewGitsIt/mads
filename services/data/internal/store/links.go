package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
// different-worktree constraint, orchestrate-vs-live-pipeline-subject,
// at-most-one-orchestrate-holder-per-subject, and no loop of leaders.
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
			v.Add("worktree", "these sessions each have their own isolated copy of the repo, and linked sessions must share one — link them when starting the second session (it then works in the first one's copy), or land one session's work and link from a new session")
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
			excludeID := ""
			if existing != nil {
				excludeID = existing.ID
			}
			if err := l.checkOrchestrateInTx(tx, link.SubjectSessionID, excludeID, "grants", &v); err != nil {
				return err
			}
			// A self-link or a reverse pair is already refused above;
			// reporting it again as a two-session loop would be noise.
			if link.GrantingSessionID != link.SubjectSessionID && !reverseExists {
				if err := l.checkLeaderLoopInTx(tx, link.GrantingSessionID, link.SubjectSessionID, "grants", &v); err != nil {
					return err
				}
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

// checkOrchestrateInTx adds the refusals for granting orchestrate over
// subjectID to v under field: a live pipeline run (monitorable, not
// leadable), and an existing orchestrate holder other than excludeLinkID.
// Shared by Create and the side-effect-free ValidateStaged.
func (l *Links) checkOrchestrateInTx(tx *Tx, subjectID, excludeLinkID, field string, v *contracts.ValidationError) error {
	live, err := subjectPipelineLive(tx, subjectID)
	if err != nil {
		return err
	}
	if live {
		v.Add(field, "orchestrate is refused: the subject session has a live pipeline run (pipeline runs are monitorable, not leadable)")
	}
	msg, err := l.orchestrateHolderConflictInTx(tx, subjectID, excludeLinkID)
	if err != nil {
		return err
	}
	if msg != "" {
		v.Add(field, msg)
	}
	return nil
}

// StagedLinkCheck is one entry of ValidateStaged: a link whose grants are
// already resolved (preset expanded), with contracts.NewSessionPlaceholder
// on exactly one endpoint, and the field prefix its errors are reported under.
type StagedLinkCheck struct {
	Field string
	Link  contracts.Link
}

// ValidateStaged is the dry run of creating links for a session that does
// not exist yet: it applies the rules Create would apply to each entry —
// grant shape, cadence, counterpart exists, orchestrate vs. a live pipeline
// run, at most one orchestrate holder per subject, no loop of leaders — and
// writes nothing (the
// tx never appends, so it commits empty). Rules that need both rows
// (self-link, differing worktrees) can't fire for a brand-new endpoint, and
// an existing pair can't pre-exist, so they're skipped — but two staged
// entries can still be each other's opposite direction (new→X and X→new),
// which the second create would refuse, so that is checked among the
// entries. Entries are checked in order; a placeholder subject led by an
// earlier entry makes a later orchestrate entry the second leader, as
// sequential creates would. The new session has no links yet, so a loop of
// leaders through it needs two staged entries: it leads X, Y leads it, and X
// already reaches Y through leader links; the later of the two is refused.
// A missing counterpart returns ErrNotFound.
func (l *Links) ValidateStaged(ctx context.Context, checks []StagedLinkCheck) (*contracts.ValidationError, error) {
	var v contracts.ValidationError
	err := l.log.WithTx(ctx, func(tx *Tx) error {
		newLeader, newLeaderID := "", ""
		// ledByNew lists the counterparts staged entries have the new
		// session lead, for the loop-of-leaders check.
		var ledByNew []string
		// grantedBy records, per counterpart, the direction an earlier
		// entry staged with it: true when the new session grants.
		grantedBy := map[string]bool{}
		for _, c := range checks {
			link := c.Link
			verr := validateGrants(link.Grants)
			if verr == nil {
				verr = validateCadence(&link)
			}
			if verr != nil {
				for _, fe := range verr.Errors {
					v.Add(c.Field+"."+fe.Field, fe.Message)
				}
				continue
			}
			grantingNew := link.GrantingSessionID == contracts.NewSessionPlaceholder
			subjectNew := link.SubjectSessionID == contracts.NewSessionPlaceholder
			if grantingNew == subjectNew {
				v.Add(c.Field, "exactly one endpoint must be the new-session placeholder")
				continue
			}
			counterpartID := link.GrantingSessionID
			if grantingNew {
				counterpartID = link.SubjectSessionID
			}
			counterpart, err := l.sessions.getInTx(tx, counterpartID)
			if err != nil {
				return err
			}
			if prev, seen := grantedBy[counterpartID]; seen && prev != grantingNew {
				v.Add(c.Field+".direction", "another staged link with this session runs the opposite direction — peer/bidirectional links aren't supported")
				continue
			} else if !seen {
				grantedBy[counterpartID] = grantingNew
			}
			if !hasGrant(link.Grants, contracts.GrantOrchestrate) {
				continue
			}
			if grantingNew {
				if err := l.checkOrchestrateInTx(tx, counterpartID, "", c.Field+".grants", &v); err != nil {
					return err
				}
				ledByNew = append(ledByNew, counterpartID)
				if newLeaderID != "" {
					if err := l.checkStagedLeaderLoopInTx(tx, []string{counterpartID}, newLeaderID, c.Field+".grants", &v); err != nil {
						return err
					}
				}
				continue
			}
			if newLeader != "" {
				v.Add(c.Field+".grants", orchestrateHolderMessage(newLeader))
				continue
			}
			newLeader, newLeaderID = holderLabel(counterpartID, counterpart.Name), counterpartID
			if err := l.checkStagedLeaderLoopInTx(tx, ledByNew, newLeaderID, c.Field+".grants", &v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if v.Empty() {
		return nil, nil
	}
	return &v, nil
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
// change. The loop-of-leaders check runs only when the update adds
// orchestrate, so a cadence edit or a downgrade on a link that is already
// part of a stored loop still goes through.
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
			if !hasGrant(old.Grants, contracts.GrantOrchestrate) {
				if err := l.checkLeaderLoopInTx(tx, link.GrantingSessionID, link.SubjectSessionID, "grants", &v); err != nil {
					return err
				}
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
	name := ""
	if sess, err := l.sessions.getInTx(tx, holderID); err == nil {
		name = sess.Name
	}
	return orchestrateHolderMessage(holderLabel(holderID, name)), nil
}

// holderLabel names a holder session for a refusal message: quoted name
// plus id, or the id alone when the session has no name or is gone.
func holderLabel(id, name string) string {
	if name == "" {
		return id
	}
	return fmt.Sprintf("%q (%s)", name, id)
}

func orchestrateHolderMessage(holder string) string {
	return fmt.Sprintf("the subject is already led by %s — a subject may have at most one orchestrate holder", holder)
}

// checkLeaderLoopInTx adds to v, under field, the refusal for a new leader
// link grantingID → subjectID that would close a loop of leaders: subjectID
// already leads, directly or through others, grantingID.
func (l *Links) checkLeaderLoopInTx(tx *Tx, grantingID, subjectID, field string, v *contracts.ValidationError) error {
	path, err := leaderPathInTx(tx, subjectID, grantingID)
	if err != nil || path == nil {
		return err
	}
	v.Add(field, l.leaderLoopMessageInTx(tx, append([]string{grantingID}, path...)))
	return nil
}

// checkStagedLeaderLoopInTx is checkLeaderLoopInTx for staged links: the new
// session leads each of ledIDs and is led by leaderID, so a loop exists when
// any of ledIDs already reaches leaderID through leader links. Reports the
// first loop found.
func (l *Links) checkStagedLeaderLoopInTx(tx *Tx, ledIDs []string, leaderID, field string, v *contracts.ValidationError) error {
	for _, led := range ledIDs {
		path, err := leaderPathInTx(tx, led, leaderID)
		if err != nil {
			return err
		}
		if path != nil {
			loop := append([]string{contracts.NewSessionPlaceholder}, path...)
			v.Add(field, l.leaderLoopMessageInTx(tx, append(loop, contracts.NewSessionPlaceholder)))
			return nil
		}
	}
	return nil
}

// leaderLoopMessageInTx renders the refusal for loop, a session-id path
// that starts and ends on the same session, naming each step as holderLabel
// does (the staged placeholder reads as "the new session").
func (l *Links) leaderLoopMessageInTx(tx *Tx, loop []string) string {
	labels := make([]string, len(loop))
	for i, id := range loop {
		if id == contracts.NewSessionPlaceholder {
			labels[i] = "the new session"
			continue
		}
		name := ""
		if sess, err := l.sessions.getInTx(tx, id); err == nil {
			name = sess.Name
		}
		labels[i] = holderLabel(id, name)
	}
	return fmt.Sprintf("this would make a loop of leaders: %s — a session can't be led, directly or through others, by a session it leads",
		strings.Join(labels, " → "))
}

// leaderPathInTx returns the shortest chain of leader links (orchestrate,
// not implicit) from fromID to toID as session ids, both ends included, or
// nil when toID isn't reachable that way. Breadth-first, one query per
// visited session.
func leaderPathInTx(tx *Tx, fromID, toID string) ([]string, error) {
	parent := map[string]string{fromID: ""}
	queue := []string{fromID}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == toID {
			var path []string
			for id := cur; id != ""; id = parent[id] {
				path = append([]string{id}, path...)
			}
			return path, nil
		}
		led, err := ledSessionsInTx(tx, cur)
		if err != nil {
			return nil, err
		}
		for _, next := range led {
			if _, seen := parent[next]; seen {
				continue
			}
			parent[next] = cur
			queue = append(queue, next)
		}
	}
	return nil, nil
}

// ledSessionsInTx returns the subjects grantingID holds orchestrate over
// through non-implicit links, oldest link first.
func ledSessionsInTx(tx *Tx, grantingID string) ([]string, error) {
	rows, err := tx.Query(
		`SELECT subject_session_id, grants FROM links WHERE granting_session_id = ? AND implicit = 0 ORDER BY created_at`, grantingID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var subjectID, grantsJSON string
		if err := rows.Scan(&subjectID, &grantsJSON); err != nil {
			return nil, err
		}
		var grants []contracts.Grant
		if err := json.Unmarshal([]byte(grantsJSON), &grants); err != nil {
			return nil, err
		}
		if hasGrant(grants, contracts.GrantOrchestrate) {
			out = append(out, subjectID)
		}
	}
	return out, rows.Err()
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
// where coverage begins, so no explicit "dropped" marker is needed. The cap
// applies separately to on_signal backlog and to deferred pushes, so a
// flood of one can't evict the other.
const linkDigestCap = 32

// AppendDigest queues one entry on link id's mailbox (docs/LINKED-SESSIONS-
// DESIGN.md decision 4, the on_signal accumulation half; or a push deferred
// because its recipient was asleep, d.Deferred), enforcing linkDigestCap by
// dropping oldest-first within the entry's class. No event is emitted —
// mailbox traffic is internal plumbing between the Router's jobs and a
// later wake, not activity-feed material.
func (l *Links) AppendDigest(ctx context.Context, id string, d *contracts.LinkDigest) error {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	attribution := ""
	if d.Attribution != nil {
		raw, err := json.Marshal(d.Attribution)
		if err != nil {
			return err
		}
		attribution = string(raw)
	}
	return l.log.WithTx(ctx, func(tx *Tx) error {
		link, err := getLinkInTx(tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO link_digests (link_id, text, from_turn, to_turn, created_at, deferred, noop, attribution)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, d.Text, d.FromTurn, d.ToTurn, d.CreatedAt.Format(time.RFC3339Nano),
			boolInt(d.Deferred), boolInt(d.Noop), attribution,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`DELETE FROM link_digests WHERE link_id = ? AND deferred = ? AND id NOT IN
			 (SELECT id FROM link_digests WHERE link_id = ? AND deferred = ? ORDER BY id DESC LIMIT ?)`,
			id, boolInt(d.Deferred), id, boolInt(d.Deferred), linkDigestCap,
		); err != nil {
			return err
		}
		if d.Deferred {
			return appendWaitingChanged(tx, link.GrantingSessionID)
		}
		return nil
	})
}

// appendWaitingChanged emits session.updated for the recipient of a
// deferred push (or of its flush): the rail's "N updates waiting" count is
// derived from the mailbox (Session.WaitingUpdates), and session.updated is
// what makes every watching browser refetch the list.
func appendWaitingChanged(tx *Tx, sessionID string) error {
	return tx.Append(&contracts.Envelope{
		Type: contracts.SessionUpdated, SessionID: sessionID, Actor: "link-mailbox",
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DrainDigests atomically returns and clears link id's on_signal backlog,
// oldest first. Deferred entries are left alone (DrainDeferred's). An
// unknown link drains empty rather than erroring — the caller is always on
// a best-effort delivery path where the link may have been deleted
// mid-flight.
func (l *Links) DrainDigests(ctx context.Context, id string) ([]contracts.LinkDigest, error) {
	return l.drain(ctx, id, false)
}

// DrainDeferred atomically returns and clears the pushes deferred on link
// id because its recipient was asleep, oldest first. The delete rides the
// same transaction as the read, so two racing flushes can't both get an
// entry.
func (l *Links) DrainDeferred(ctx context.Context, id string) ([]contracts.LinkDigest, error) {
	return l.drain(ctx, id, true)
}

func (l *Links) drain(ctx context.Context, id string, deferred bool) ([]contracts.LinkDigest, error) {
	var out []contracts.LinkDigest
	err := l.log.WithTx(ctx, func(tx *Tx) error {
		rows, err := tx.Query(
			`SELECT text, from_turn, to_turn, created_at, deferred, noop, attribution
			 FROM link_digests WHERE link_id = ? AND deferred = ? ORDER BY id`, id, boolInt(deferred))
		if err != nil {
			return err
		}
		for rows.Next() {
			var (
				d                 contracts.LinkDigest
				createdAt, attrib string
				def, noop         int
			)
			if err := rows.Scan(&d.Text, &d.FromTurn, &d.ToTurn, &createdAt, &def, &noop, &attrib); err != nil {
				_ = rows.Close()
				return err
			}
			d.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
			d.Deferred, d.Noop = def != 0, noop != 0
			if attrib != "" {
				var a contracts.Attribution
				if json.Unmarshal([]byte(attrib), &a) == nil {
					d.Attribution = &a
				}
			}
			out = append(out, d)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM link_digests WHERE link_id = ? AND deferred = ?`, id, boolInt(deferred)); err != nil {
			return err
		}
		if deferred && len(out) > 0 {
			if link, err := getLinkInTx(tx, id); err == nil {
				return appendWaitingChanged(tx, link.GrantingSessionID)
			}
		}
		return nil
	})
	return out, err
}

// DeferredCounts returns, per granting (recipient) session, how many
// deferred pushes wait on its links — the rail's "N updates waiting".
func (l *Links) DeferredCounts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	rows, err := l.log.db.QueryContext(ctx,
		`SELECT k.granting_session_id, COUNT(*) FROM link_digests d
		 JOIN links k ON k.id = d.link_id WHERE d.deferred = 1 GROUP BY k.granting_session_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			id string
			n  int
		)
		if err := rows.Scan(&id, &n); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[id] = n
	}
	return out, errors.Join(rows.Err(), rows.Close())
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
