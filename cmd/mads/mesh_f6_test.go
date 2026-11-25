package main

import (
	"strings"
	"testing"
)

// Half-paired devices (docs/MESH-NETWORK-DESIGN.md F6), the CLI row.

const popFP = "BBBB7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG"

// mads mesh remove says how forgetting went on the device itself (R1).
func TestMeshRemoveSaysThere(t *testing.T) {
	m, url := newMeshRouter(t)
	m.json("GET /devices", raw(`[{"name":"pop-os","fingerprint":"`+popFP+`"}]`))
	for there, want := range map[string]string{
		"removed":    "removed on pop-os too",
		"not_paired": "pop-os didn't have this device paired any more",
		"offline":    "pop-os didn't answer: it may still list this device until it hears from it (connection refused)",
		"failed":     "pop-os couldn't forget this device: boom",
	} {
		m.json("DELETE /devices/"+popFP, raw(`{"introduced_by_this":[],"also_removed":[],"there":"`+there+`","there_error":"`+map[string]string{"offline": "connection refused", "failed": "boom"}[there]+`"}`))
		out, err := runMesh(t, url, "", "remove", "pop-os")
		if err != nil {
			t.Fatal(err)
		}
		wantLines(t, out, "removed pop-os here", want)
	}
	m.json("POST /devices/"+popFP+"/remove-everywhere", raw(`{"here":"removed","there":"removed","hosts":[],"cannot_manage":[]}`))
	out, err := runMesh(t, url, "", "remove", "pop-os", "--everywhere")
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, out, "pop-os: removed here", "removed on pop-os too")
}

// mads mesh status shows a device that no longer has this one paired.
func TestMeshStatusNoLongerPairedThere(t *testing.T) {
	m, url := newMeshRouter(t)
	m.json("GET /devices/self", raw(sparkSelf))
	m.json("GET /devices", raw(`[{"name":"pop-os","fingerprint":"`+popFP+`","reachability":"denied","pairing_there":"not_paired","pairing_checked_at":"2026-11-25T00:00:00Z"}]`))
	out, err := runMesh(t, url, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, out, "No longer paired there")
}

// mads mesh approve says a request from a key paired here is a re-pair.
func TestMeshApproveRePair(t *testing.T) {
	m, url := newMeshRouter(t)
	m.json("GET /devices/self", raw(sparkSelf))
	m.json("POST /devices/discoverable", raw(`{"on":true}`))
	m.json("GET /devices/pair-requests", raw(`[{"id":"pr_4","name":"mac","platform":"darwin","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","state":"pending","mode":"code","repair":{"peer_id":"per_1","level":"control"}}]`))
	m.json("POST /devices/pair-requests/pr_4/approve", raw(`{"id":"pr_4","name":"mac","state":"approved","level":"chat","peer_id":"per_1"}`))
	out, err := runMesh(t, url, "", "approve", "--level", "chat", "--yes", "--timeout", "2s")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out, "mac (macOS) wants to pair again. It's paired here now (control); approving replaces that.", "✓ Re-paired mac.")
}

// mads mesh join: a code from a half-paired host offers [r]e-pair /
// [c]ancel (R6); a transport failure says the code can be tried again
// (R7).
func TestMeshJoinRepairOffered(t *testing.T) {
	m, url := newMeshRouter(t)
	m.on("POST /devices/pair", func(body string) (int, any) {
		if strings.Contains(body, `"repair":true`) {
			return 202, raw(`{"id":"JOYG:pr_9","state":"pending","self":"ABCD-EFGH","host":{"name":"pop-os"}}`)
		}
		return 409, raw(`{"code":"repair_offered","error":"pop-os is paired here, but it doesn't have this device paired now","name":"pop-os","peer_id":"per_1"}`)
	})
	m.json("GET /devices/pair/JOYG:pr_9", raw(`{"id":"JOYG:pr_9","state":"approved","level":"view","peer_id":"per_1","host":{"name":"pop-os"}}`))
	if _, err := runMesh(t, url, "c\n", "join", "mads-pair-2:CODE"); err == nil || !strings.Contains(err.Error(), "nothing changed") || m.count("POST /devices/pair") != 1 {
		t.Fatalf("cancel: %v", err)
	}
	out, err := runMesh(t, url, "r\n", "join", "mads-pair-2:CODE")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out, "[r]e-pair / [c]ancel", "Asked pop-os to pair")
	if m.body("POST /devices/pair", m.count("POST /devices/pair")-1)["repair"] != true {
		t.Fatal("the re-pair wasn't asked for")
	}
	if out, err := runMesh(t, url, "", "join", "mads-pair-2:CODE", "--repair"); err != nil {
		t.Fatalf("--repair: %v\n%s", err, out)
	}

	m.on("POST /devices/pair", func(string) (int, any) {
		return 502, raw(`{"code":"host_unreachable","error":"can't reach pop-os","retryable":true}`)
	})
	if _, err := runMesh(t, url, "", "join", "mads-pair-2:CODE"); err == nil || !strings.Contains(err.Error(), "try the same code again") {
		t.Fatalf("retryable: %v", err)
	}
	m.on("POST /devices/pair", func(string) (int, any) {
		return 429, raw(`{"code":"rate_limited","error":"too many pairing attempts","retryable":true,"retry_after":12}`)
	})
	if _, err := runMesh(t, url, "", "join", "mads-pair-2:CODE"); err == nil || !strings.Contains(err.Error(), "again in 12s") {
		t.Fatalf("rate limited: %v", err)
	}
}
