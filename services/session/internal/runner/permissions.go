package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// defaultPermissionTimeout is how long an unanswered permission request sits
// pending before RequestPermission resolves it to a deny on its own — the
// task's "~10 min" bound. r.PermissionTimeout overrides it (tests use a much
// shorter value to exercise the timeout path without an actual 10-minute
// wait).
const defaultPermissionTimeout = 10 * time.Minute

// pendingPermission is one parked PreToolUse hook call awaiting a decision.
// resolved carries exactly one value ("allow" or "deny") — RequestPermission
// blocks reading it.
type pendingPermission struct {
	req      provider.PermissionRequest
	resolved chan string
	// toolUseID is the wire ID of the tool call this park blocked, tied
	// best-effort by r.toolStatus (the hook wire carries only the tool
	// name); "" when no announced call matched. Lets an allow move the
	// call's synthesized status pending → in_progress.
	toolUseID string
}

// RequestPermission is called by the session service's POST /permission-hook
// handler for every PreToolUse call while trust leaves the tool asking (see
// providers/claudecode/permission.go — the hook subprocess's own blocking
// HTTP call is what "parks" the tool call; there is no separate connection
// to park in-process). It registers a new pending request, broadcasts an
// EventPermissionRequest (status "pending") on the hub so the chat UI can
// render a card, and blocks until ResolvePermission answers it or the
// timeout elapses — at which point it resolves to "deny" on its own and
// broadcasts an "expired" status. The return value is exactly claude's
// hookSpecificOutput.permissionDecision ("allow" or "deny").
func (r *Runner) RequestPermission(tool string, input json.RawMessage) string {
	r.permMu.Lock()
	r.permSeq++
	id := fmt.Sprintf("%s-perm-%d", r.sessionID, r.permSeq)
	pp := &pendingPermission{
		req:      provider.PermissionRequest{ID: id, Tool: tool, Input: input, Status: "pending"},
		resolved: make(chan string, 1),
	}
	r.permPending[id] = pp
	r.permMu.Unlock()

	r.hub.broadcast(provider.Event{Kind: provider.EventPermissionRequest, Permission: &pp.req})
	// Persisted (unlike the hub frame, which only live viewers see) so a
	// session that sat parked awaiting a decision is diagnosable from the
	// event log after the fact.
	r.appendPermissionEvent(contracts.SessionPermissionRequested,
		map[string]string{"request_id": id, "tool": tool})

	// Status synthesis (pkg/toolstatus): the parked call is "pending" until
	// answered. Best-effort by tool name — the hook can fire before the
	// pump has processed the announcing tool_use line, in which case no
	// call matches and the card alone tells the story.
	if up, ok := r.toolStatus.PermissionParked(tool); ok {
		r.permMu.Lock() // finishPermission reads toolUseID under the same lock
		pp.toolUseID = up.ToolUseID
		r.permMu.Unlock()
		r.appendConversation(contracts.SessionToolStatusChanged, contracts.ToolStatusChange{
			ToolUseID:       up.ToolUseID,
			Status:          up.Status,
			ParentToolUseID: up.ParentToolUseID,
		})
	}

	timeout := r.PermissionTimeout
	if timeout <= 0 {
		timeout = defaultPermissionTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case decision := <-pp.resolved:
		return decision
	case <-timer.C:
		r.finishPermission(id, "deny", "expired")
		return "deny"
	}
}

// ResolvePermission answers a pending request from the chat UI's Allow/Deny
// card (POST /permission-requests/{id}/resolve). decision must be "allow" or
// "deny". Returns false if id is unknown or already resolved (a stale or
// double click, or a request that already timed out) — not an error worth
// surfacing to the caller.
func (r *Runner) ResolvePermission(id, decision string) bool {
	if decision != "allow" && decision != "deny" {
		return false
	}
	status := "denied"
	if decision == "allow" {
		status = "allowed"
	}
	return r.finishPermission(id, decision, status)
}

// finishPermission resolves the pending request exactly once: deleting it
// from permPending under permMu is what arbitrates the resolve-vs-timeout
// race (whichever call reaches the lock first deletes the entry and wins;
// the other finds it already gone and is a no-op), so no separate
// once.Do/atomic guard is needed.
func (r *Runner) finishPermission(id, decision, status string) bool {
	r.permMu.Lock()
	pp, ok := r.permPending[id]
	if ok {
		delete(r.permPending, id)
	}
	var toolUseID string
	if ok {
		toolUseID = pp.toolUseID
	}
	r.permMu.Unlock()
	if !ok {
		return false
	}
	pp.req.Status = status
	pp.resolved <- decision
	r.hub.broadcast(provider.Event{Kind: provider.EventPermissionRequest, Permission: &pp.req})
	r.appendPermissionEvent(contracts.SessionPermissionResolved,
		map[string]string{"request_id": id, "tool": pp.req.Tool, "status": status})

	// An allow un-parks the call: pending → in_progress (pkg/toolstatus).
	// Deny/expiry needs no transition here — the harness reports the
	// denial as an error tool_result (→ failed), and the turn-end sweep
	// backstops a harness that doesn't.
	if status == "allowed" && toolUseID != "" {
		if up, resumed := r.toolStatus.PermissionResolved(toolUseID); resumed {
			r.appendConversation(contracts.SessionToolStatusChanged, contracts.ToolStatusChange{
				ToolUseID:       up.ToolUseID,
				Status:          up.Status,
				ParentToolUseID: up.ParentToolUseID,
			})
		}
	}
	return true
}

// appendPermissionEvent persists one permission lifecycle event, best-effort
// (a failed append is logged, never blocks the decision path). Nil r.data —
// unit tests build Runners without a Data Service — is a no-op.
func (r *Runner) appendPermissionEvent(t contracts.EventType, payload map[string]string) {
	if r.data == nil {
		return
	}
	if err := r.data.Append(context.Background(), &contracts.Envelope{
		Type:      t,
		SessionID: r.sessionID,
		Payload:   mustJSON(payload),
	}); err != nil {
		log.Printf("runner: append %s: %v", t, err)
	}
}

// PermissionRequests returns every still-pending request for this session —
// the reconstruction source for a chat client that refetches its transcript
// and needs to redraw a still-open card (acceptance criterion 3). A resolved
// request isn't returned here; a client that already received its terminal
// event (allowed/denied/expired) off the live stream needs nothing further
// from this endpoint. Order is unspecified (map iteration) — there is at
// most a handful of these live at once, so callers sort if they care.
func (r *Runner) PermissionRequests() []provider.PermissionRequest {
	r.permMu.Lock()
	defer r.permMu.Unlock()
	out := make([]provider.PermissionRequest, 0, len(r.permPending))
	for _, pp := range r.permPending {
		out = append(out, pp.req)
	}
	return out
}
