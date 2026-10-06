package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/toolstatus"
	"github.com/drewkidwell/mads/providers/provider"
)

// bashAsk is the ask a harness raises for one Bash call.
func bashAsk() provider.PermissionAsk {
	return provider.PermissionAsk{ToolUseID: "toolu_1", Tool: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}
}

// TestRequestPermissionBlocksUntilResolved proves the core allow/deny round
// trip acceptance criterion: RequestPermission parks (doesn't return) until
// ResolvePermission answers, then returns exactly that decision.
func TestRequestPermissionBlocksUntilResolved(t *testing.T) {
	for _, decision := range []string{"allow", "deny"} {
		r := New("sess-1", nil, nil)
		ch, unsubscribe := r.hub.subscribe()
		defer unsubscribe()

		done := make(chan provider.PermissionDecision, 1)
		go func() {
			ask := bashAsk()
			ask.AgentID, ask.Reason = "agent-1", "Bash asks"
			done <- r.RequestPermission(context.Background(), ask)
		}()

		var req provider.PermissionRequest
		select {
		case ev := <-ch:
			if ev.Kind != provider.EventPermissionRequest || ev.Permission == nil {
				t.Fatalf("expected a permission_request event, got %+v", ev)
			}
			req = *ev.Permission
		case <-time.After(2 * time.Second):
			t.Fatal("no permission_request event broadcast")
		}
		if req.Status != "pending" || req.Tool != "Bash" || req.ToolUseID != "toolu_1" ||
			req.AgentID != "agent-1" || req.Reason != "Bash asks" {
			t.Errorf("request = %+v, want a pending Bash card for toolu_1 by agent-1", req)
		}

		select {
		case <-done:
			t.Fatal("RequestPermission returned before being resolved")
		case <-time.After(50 * time.Millisecond):
		}

		if !r.ResolvePermission(req.ID, decision) {
			t.Fatalf("ResolvePermission(%q, %q) = false, want true", req.ID, decision)
		}

		select {
		case res := <-done:
			if res.Allow != (decision == "allow") {
				t.Errorf("RequestPermission returned %+v for %q", res, decision)
			}
			if !res.Allow && !strings.HasPrefix(res.Message, toolstatus.DenialPrefix) {
				t.Errorf("deny message %q lacks the denial prefix", res.Message)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("RequestPermission never returned after resolve")
		}

		// The resolve also broadcasts a terminal-status event.
		wantStatus := map[string]string{"allow": "allowed", "deny": "denied"}[decision]
		select {
		case ev := <-ch:
			if ev.Permission == nil || ev.Permission.Status != wantStatus || ev.Permission.ToolUseID != "toolu_1" {
				t.Errorf("terminal event = %+v, want %q for toolu_1", ev.Permission, wantStatus)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no terminal permission_request event broadcast after resolve")
		}
	}
}

// TestRequestPermissionTimesOutToDeny is the "unanswered prompts deny after
// the bound" acceptance criterion — proven with a short overridden
// PermissionTimeout rather than waiting out the real 10-minute default.
func TestRequestPermissionTimesOutToDeny(t *testing.T) {
	r := New("sess-1", nil, nil)
	r.PermissionTimeout = 20 * time.Millisecond

	done := make(chan provider.PermissionDecision, 1)
	go func() { done <- r.RequestPermission(context.Background(), provider.PermissionAsk{Tool: "Write"}) }()

	select {
	case decision := <-done:
		if decision.Allow {
			t.Errorf("timed-out decision = %+v, want deny", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestPermission never returned after its timeout")
	}

	if len(r.PermissionRequests()) != 0 {
		t.Errorf("expired request should be removed from the pending set, got %v", r.PermissionRequests())
	}
}

// TestRequestPermissionCancelled: the harness stopped waiting (an
// interrupted turn, the process gone) — the card goes "cancelled", the
// answer is a deny nobody will read, and the call is blocked.
func TestRequestPermissionCancelled(t *testing.T) {
	r := New("sess-1", nil, nil)
	r.toolStatus.ToolUse("toolu_1", "Bash", "", "")
	ch, unsubscribe := r.hub.subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan provider.PermissionDecision, 1)
	go func() { done <- r.RequestPermission(ctx, bashAsk()) }()
	<-ch // pending
	cancel()

	select {
	case decision := <-done:
		if decision.Allow {
			t.Errorf("cancelled decision = %+v, want deny", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestPermission ignored its cancelled ctx")
	}
	select {
	case ev := <-ch:
		if ev.Permission == nil || ev.Permission.Status != "cancelled" {
			t.Errorf("terminal event = %+v, want cancelled", ev.Permission)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no cancelled event broadcast")
	}
	if got := r.toolStatus.ToolResult("toolu_1", true, "The user doesn't want to proceed", ""); got != toolstatus.Blocked {
		t.Errorf("cancelled call's result status = %q, want blocked", got)
	}
}

// TestRequestPermissionParentFromTracker: an ask that doesn't name its
// parent gets it from the tracker's knowledge of the call.
func TestRequestPermissionParentFromTracker(t *testing.T) {
	r := New("sess-1", nil, nil)
	r.toolStatus.ToolUse("toolu_1", "Bash", "toolu_agent", "")
	ch, unsubscribe := r.hub.subscribe()
	defer unsubscribe()
	go func() { _ = r.RequestPermission(context.Background(), bashAsk()) }()
	ev := <-ch
	if ev.Permission == nil || ev.Permission.ParentToolUseID != "toolu_agent" {
		t.Fatalf("request = %+v, want parent toolu_agent", ev.Permission)
	}
	r.ResolvePermission(ev.Permission.ID, "deny")
}

// TestResolvePermissionAfterTimeoutIsNoOp proves the race arbitration: once
// finishPermission's delete-under-mutex has already run for one path
// (timeout), a late-arriving resolve for the same id must not panic, double
// -send, or report success.
func TestResolvePermissionAfterTimeoutIsNoOp(t *testing.T) {
	r := New("sess-1", nil, nil)
	r.PermissionTimeout = 10 * time.Millisecond

	done := make(chan provider.PermissionDecision, 1)
	go func() { done <- r.RequestPermission(context.Background(), bashAsk()) }()

	select {
	case decision := <-done:
		if decision.Allow {
			t.Fatalf("decision = %+v, want deny", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no timeout")
	}

	reqs := r.PermissionRequests()
	if len(reqs) != 0 {
		t.Fatalf("expected pending set empty after timeout, got %v", reqs)
	}
	if r.ResolvePermission("sess-1-perm-1", "allow") {
		t.Error("resolving an already-timed-out request should return false")
	}
}

func TestResolvePermissionRejectsUnknownDecision(t *testing.T) {
	r := New("sess-1", nil, nil)
	go func() { _ = r.RequestPermission(context.Background(), bashAsk()) }()
	time.Sleep(20 * time.Millisecond)
	reqs := r.PermissionRequests()
	if len(reqs) != 1 {
		t.Fatalf("expected one pending request, got %v", reqs)
	}
	if r.ResolvePermission(reqs[0].ID, "maybe") {
		t.Error("ResolvePermission with an invalid decision should return false")
	}
	// Still pending — an invalid decision must not have consumed it.
	if got := r.PermissionRequests(); len(got) != 1 || got[0].Status != "pending" {
		t.Errorf("request should still be pending after rejected decision, got %v", got)
	}
}

func TestPermissionRequestsListsOnlyPending(t *testing.T) {
	r := New("sess-1", nil, nil)
	done := make(chan struct{})
	go func() {
		_ = r.RequestPermission(context.Background(), bashAsk())
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)

	reqs := r.PermissionRequests()
	if len(reqs) != 1 || reqs[0].Tool != "Bash" || reqs[0].Status != "pending" {
		t.Fatalf("expected one pending Bash request, got %v", reqs)
	}

	if !r.ResolvePermission(reqs[0].ID, "allow") {
		t.Fatal("resolve failed")
	}
	<-done

	if got := r.PermissionRequests(); len(got) != 0 {
		t.Errorf("resolved requests should not be listed, got %v", got)
	}
}
