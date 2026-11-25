package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// fakeDevicesRouter answers the Router's /devices routes `mads pair` uses,
// and records what it was sent.
type fakeDevicesRouter struct {
	mu   sync.Mutex
	seen []string
	body map[string]string
}

func (f *fakeDevicesRouter) start(t *testing.T) string {
	t.Helper()
	f.body = map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw bytes.Buffer
		_, _ = raw.ReadFrom(r.Body)
		f.mu.Lock()
		key := r.Method + " " + r.URL.Path
		f.seen = append(f.seen, key)
		f.body[key] = raw.String()
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch key {
		case "POST /devices/pairing-code":
			_, _ = w.Write([]byte(`{"id":"pc_1","code":"mads-pair-1:SECRETCODE","expires_at":"2026-11-19T12:05:00Z","host":{"name":"pop-os","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","mesh_url":"http://pop-os:17380"}}`))
		case "POST /devices/pair":
			_, _ = w.Write([]byte(`{"id":"pr_1","state":"pending","host":{"name":"pop-os"}}`))
		case "GET /devices/pair/pr_1":
			_, _ = w.Write([]byte(`{"id":"pr_1","state":"approved","level":"control","peer_id":"per_9","expires_at":"2026-11-19T12:05:00Z","host":{"name":"pop-os","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG"}}`))
		case "POST /devices/pair-requests/pr_1/approve":
			_, _ = w.Write([]byte(`{"id":"pr_1","name":"mac","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","state":"approved","level":"chat"}`))
		case "GET /devices":
			_, _ = w.Write([]byte(`[{"name":"phone","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG"}]`))
		case "POST /devices/JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG/remove-everywhere":
			_, _ = w.Write([]byte(`{"here":"removed","hosts":[{"name":"pop-os","result":"removed"},{"name":"spark","result":"offline","error":"connection refused"}],"cannot_manage":[{"name":"work"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","error":"no such route"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestPairSubcommands(t *testing.T) {
	f := &fakeDevicesRouter{}
	url := f.start(t)
	run := func(stdin string, args ...string) (string, error) {
		var out bytes.Buffer
		err := cmdPairSub(append(args, "-router", url), strings.NewReader(stdin), &out)
		return out.String(), err
	}
	out, err := run("", "code")
	if err != nil || !strings.Contains(out, "mads-pair-1:SECRETCODE") || !strings.Contains(out, "mads pair approve") {
		t.Fatalf("code: %v\n%s", err, out)
	}
	// The code from stdin, so it never sits in the process list.
	out, err = run("mads-pair-1:PASTED\n", "join", "--wait")
	if err != nil || !strings.Contains(out, "lets this device control mads there") {
		t.Fatalf("join: %v\n%s", err, out)
	}
	var sent map[string]string
	_ = json.Unmarshal([]byte(f.body["POST /devices/pair"]), &sent)
	if sent["code"] != "mads-pair-1:PASTED" {
		t.Fatalf("join sent %v", sent)
	}
	if _, err := run("", "approve", "pr_1", "--grant", "admin"); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("approve admin: %v", err)
	}
	if _, err := run("", "approve", "pr_1"); err == nil {
		t.Fatal("approve without --grant")
	}
	out, err = run("", "approve", "pr_1", "--grant", "chat")
	if err != nil || !strings.Contains(out, "may chat with mads here") {
		t.Fatalf("approve: %v\n%s", err, out)
	}
	var decision contracts.PairDecision
	_ = json.Unmarshal([]byte(f.body["POST /devices/pair-requests/pr_1/approve"]), &decision)
	if decision.Level != contracts.GrantChat {
		t.Fatalf("approve sent %+v", decision)
	}
	out, err = run("", "remove", "phone", "--everywhere")
	if err == nil || !strings.Contains(out, "spark: offline") || !strings.Contains(out, "work: not yours to manage") {
		t.Fatalf("remove everywhere: %v\n%s", err, out)
	}
	// --json is the Router's whole answer; the code's host fields keep
	// their JSON names.
	out, err = run("", "code", "--json")
	if err != nil || !strings.Contains(out, `"mesh_url"`) || !strings.Contains(out, `"fingerprint"`) || strings.Contains(out, `"Name"`) {
		t.Fatalf("code --json: %v\n%s", err, out)
	}
	out, _ = run("", "remove", "phone", "--everywhere", "--json")
	if !strings.Contains(out, `"cannot_manage"`) || !strings.Contains(out, `"here"`) {
		t.Fatalf("remove --json: %s", out)
	}
	// join --wait --json carries what the Router answered.
	out, err = run("mads-pair-1:PASTED\n", "join", "--wait", "--json")
	if err != nil || !strings.Contains(out, `"peer_id": "per_9"`) || !strings.Contains(out, `"expires_at"`) {
		t.Fatalf("join --json: %v\n%s", err, out)
	}
	if _, err := run("", "bogus"); err == nil {
		t.Fatal("unknown subcommand")
	}
}
