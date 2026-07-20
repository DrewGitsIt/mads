package runner

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// TestRequestPermissionBlocksUntilResolved proves the core allow/deny round
// trip acceptance criterion: RequestPermission parks (doesn't return) until
// ResolvePermission answers, then returns exactly that decision.
func TestRequestPermissionBlocksUntilResolved(t *testing.T) {
	for _, decision := range []string{"allow", "deny"} {
		r := New("sess-1", nil, nil)
		ch, unsubscribe := r.hub.subscribe()
		defer unsubscribe()

		type result struct {
			decision string
		}
		done := make(chan result, 1)
		go func() {
			got := r.RequestPermission("Bash", json.RawMessage(`{"command":"ls"}`))
			done <- result{got}
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
		if req.Status != "pending" {
			t.Errorf("initial status = %q, want pending", req.Status)
		}
		if req.Tool != "Bash" {
			t.Errorf("tool = %q, want Bash", req.Tool)
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
			if res.decision != decision {
				t.Errorf("RequestPermission returned %q, want %q", res.decision, decision)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("RequestPermission never returned after resolve")
		}

		// The resolve also broadcasts a terminal-status event.
		wantStatus := map[string]string{"allow": "allowed", "deny": "denied"}[decision]
		select {
		case ev := <-ch:
			if ev.Permission == nil || ev.Permission.Status != wantStatus {
				t.Errorf("terminal event status = %+v, want %q", ev.Permission, wantStatus)
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

	done := make(chan string, 1)
	go func() {
		done <- r.RequestPermission("Write", nil)
	}()

	select {
	case decision := <-done:
		if decision != "deny" {
			t.Errorf("timed-out decision = %q, want deny", decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestPermission never returned after its timeout")
	}

	if len(r.PermissionRequests()) != 0 {
		t.Errorf("expired request should be removed from the pending set, got %v", r.PermissionRequests())
	}
}

// TestResolvePermissionAfterTimeoutIsNoOp proves the race arbitration: once
// finishPermission's delete-under-mutex has already run for one path
// (timeout), a late-arriving resolve for the same id must not panic, double
// -send, or report success.
func TestResolvePermissionAfterTimeoutIsNoOp(t *testing.T) {
	r := New("sess-1", nil, nil)
	r.PermissionTimeout = 10 * time.Millisecond

	done := make(chan string, 1)
	go func() { done <- r.RequestPermission("Bash", nil) }()

	select {
	case decision := <-done:
		if decision != "deny" {
			t.Fatalf("decision = %q, want deny", decision)
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
	go func() { _ = r.RequestPermission("Bash", nil) }()
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
		_ = r.RequestPermission("Bash", nil)
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
