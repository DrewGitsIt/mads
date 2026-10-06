package runner

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/toolstatus"
	"github.com/drewkidwell/mads/providers/provider"
)

// defaultPermissionTimeout is how long an unanswered permission request sits
// pending before RequestPermission resolves it to a deny on its own — the
// task's "~10 min" bound. r.PermissionTimeout overrides it (tests use a much
// shorter value to exercise the timeout path without an actual 10-minute
// wait).
const defaultPermissionTimeout = 10 * time.Minute

// Permission request statuses (provider.PermissionRequest.Status).
const (
	permPending   = "pending"
	permAllowed   = "allowed"
	permDenied    = "denied"
	permExpired   = "expired"
	permCancelled = "cancelled"
)

// pendingPermission is one harness permission ask awaiting a decision.
// resolved carries exactly one value (the final status) — RequestPermission
// blocks reading it.
type pendingPermission struct {
	req      provider.PermissionRequest
	resolved chan string
}

// RequestPermission is the session's provider.PermissionAsker (set as
// StartOptions.Permissions in Start): the adapter calls it whenever the
// harness itself asks permission for a tool call — claude-code's manual
// mode on limited/read-only trust, its auto mode's escalations on
// auto_review, an ACP agent's session/request_permission. It registers a
// new pending request, marks the call "pending" (pkg/toolstatus), broadcasts an
// EventPermissionRequest (status "pending") on the hub so the chat UI can
// render a card, and blocks until ResolvePermission answers it, the timeout
// elapses ("expired"), or ctx is done because the harness stopped waiting —
// an interrupted turn, the process gone ("cancelled"). Anything but an
// allow is a deny, and blocks the call.
func (r *Runner) RequestPermission(ctx context.Context, ask provider.PermissionAsk) provider.PermissionDecision {
	// Status synthesis first, so the card carries the call's parent: the
	// ask names its call exactly, and the tracker knows (or, if the ask
	// beat the call's tool_use to the pump, starts tracking) it.
	parent := ask.ParentToolUseID
	if up, ok := r.toolStatus.PermissionParked(ask.ToolUseID, ask.ParentToolUseID); ok {
		parent = up.ParentToolUseID
		r.appendToolStatus(up)
	}

	r.permMu.Lock()
	r.permSeq++
	id := fmt.Sprintf("%s-perm-%d", r.sessionID, r.permSeq)
	pp := &pendingPermission{
		req: provider.PermissionRequest{
			ID: id, Tool: ask.Tool, Input: ask.Input, Status: permPending,
			ToolUseID: ask.ToolUseID, ParentToolUseID: parent, AgentID: ask.AgentID, Reason: ask.Reason,
		},
		resolved: make(chan string, 1),
	}
	r.permPending[id] = pp
	req := pp.req // a copy: finishPermission mutates pp.req under permMu
	r.permMu.Unlock()

	r.hub.broadcast(provider.Event{Kind: provider.EventPermissionRequest, Permission: &req})
	// Persisted (unlike the hub frame, which only live viewers see) so a
	// session that sat parked awaiting a decision is diagnosable from the
	// event log after the fact.
	r.appendPermissionEvent(contracts.SessionPermissionRequested,
		map[string]string{"request_id": id, "tool": ask.Tool, "tool_use_id": ask.ToolUseID})

	timeout := r.PermissionTimeout
	if timeout <= 0 {
		timeout = defaultPermissionTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case status := <-pp.resolved:
		return permissionDecision(status)
	case <-timer.C:
		r.finishPermission(id, permExpired)
	case <-ctx.Done():
		r.finishPermission(id, permCancelled)
	}
	// Whichever finishPermission won the race — this one, or a resolve
	// that landed at the same moment — sent the final status exactly once.
	return permissionDecision(<-pp.resolved)
}

// permissionDecision is what the harness is told for a final status. Every
// deny message starts with toolstatus.DenialPrefix: the harness relays it
// to the model as the refused call's result, and the prefix is how a
// reloaded transcript still shows that call blocked.
func permissionDecision(status string) provider.PermissionDecision {
	switch status {
	case permAllowed:
		return provider.PermissionDecision{Allow: true}
	case permExpired:
		return provider.PermissionDecision{Message: toolstatus.DenialPrefix + "nobody answered the permission request in time."}
	case permCancelled:
		return provider.PermissionDecision{Message: toolstatus.DenialPrefix + "the permission request was withdrawn."}
	default:
		return provider.PermissionDecision{Message: toolstatus.DenialPrefix + "the user denied permission."}
	}
}

// ResolvePermission answers a pending request from the chat UI's Allow/Deny
// card (POST /permission-requests/{id}/resolve). decision must be "allow" or
// "deny". Returns false if id is unknown or already resolved (a stale or
// double click, or a request that already timed out) — not an error worth
// surfacing to the caller.
func (r *Runner) ResolvePermission(id, decision string) bool {
	switch decision {
	case "allow":
		return r.finishPermission(id, permAllowed)
	case "deny":
		return r.finishPermission(id, permDenied)
	default:
		return false
	}
}

// finishPermission resolves the pending request exactly once: deleting it
// from permPending under permMu is what arbitrates the resolve-vs-timeout-
// vs-cancel race (whichever call reaches the lock first deletes the entry
// and wins; the others find it already gone and are no-ops), so no separate
// once.Do/atomic guard is needed.
func (r *Runner) finishPermission(id, status string) bool {
	r.permMu.Lock()
	pp, ok := r.permPending[id]
	if ok {
		delete(r.permPending, id)
		pp.req.Status = status
	}
	r.permMu.Unlock()
	if !ok {
		return false
	}
	pp.resolved <- status
	req := pp.req
	r.hub.broadcast(provider.Event{Kind: provider.EventPermissionRequest, Permission: &req})
	r.appendPermissionEvent(contracts.SessionPermissionResolved,
		map[string]string{"request_id": id, "tool": req.Tool, "tool_use_id": req.ToolUseID, "status": status})

	// Status synthesis (pkg/toolstatus): an allow un-parks the call
	// (pending → in_progress); anything else means it will never run
	// (→ blocked, which the harness's error tool_result then keeps).
	if req.ToolUseID == "" {
		return true
	}
	var up toolstatus.Update
	var changed bool
	if status == permAllowed {
		up, changed = r.toolStatus.PermissionResolved(req.ToolUseID)
	} else {
		up, changed = r.toolStatus.PermissionDenied(req.ToolUseID)
	}
	if changed {
		r.appendToolStatus(up)
	}
	return true
}

// appendToolStatus persists one synthesized tool-status change.
func (r *Runner) appendToolStatus(up toolstatus.Update) {
	r.appendConversation(contracts.SessionToolStatusChanged, contracts.ToolStatusChange{
		ToolUseID:       up.ToolUseID,
		Status:          up.Status,
		ParentToolUseID: up.ParentToolUseID,
	})
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
// event (allowed/denied/expired/cancelled) off the live stream needs nothing further
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
