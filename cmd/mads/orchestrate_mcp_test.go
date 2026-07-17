package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCmdInternalUnknownSubcommand(t *testing.T) {
	if err := cmdInternal([]string{"nope"}); err == nil {
		t.Fatal("unknown internal subcommand accepted")
	}
	if err := cmdInternal(nil); err == nil {
		t.Fatal("missing internal subcommand accepted")
	}
}

func TestCmdOrchestrateMCPRequiresEnv(t *testing.T) {
	t.Setenv("MADS_ROUTER_URL", "")
	t.Setenv("MADS_SESSION_ID", "")
	if err := cmdOrchestrateMCP(strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("missing MADS_ROUTER_URL/MADS_SESSION_ID accepted")
	}
}

// TestCmdOrchestrateMCPServesToolsList proves cmdInternal's dispatch reaches
// a working orchestratemcp.Server wired to the env-provided Router URL and
// session ID — the exact path pkg/capability.Registry's spawned process
// takes.
func TestCmdOrchestrateMCPServesToolsList(t *testing.T) {
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request to router: %s", r.URL.Path)
	}))
	defer router.Close()

	t.Setenv("MADS_ROUTER_URL", router.URL)
	t.Setenv("MADS_SESSION_ID", "ses_leader")

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := cmdOrchestrateMCP(in, &out); err != nil {
		t.Fatalf("cmdOrchestrateMCP: %v", err)
	}
	if !strings.Contains(out.String(), "list_links") {
		t.Fatalf("output = %q, want it to advertise list_links", out.String())
	}
}
