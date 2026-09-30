package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// stubOrchestrateRouter serves the three Router orchestrate routes the CLI
// wraps, for leader ses_lead holding one link (lnk_1) over ses_sub.
func stubOrchestrateRouter(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var sent []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions/ses_lead/orchestrate/subjects", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Mads-Actor"); got != "session-ses_lead" {
			t.Errorf("actor header = %q", got)
		}
		_, _ = io.WriteString(w, `[{"link_id":"lnk_1","subject_session_id":"ses_sub","subject_name":"payments","grants":["read","inject","orchestrate"],"activity":"active"}]`)
	})
	mux.HandleFunc("GET /sessions/ses_lead/orchestrate/summary", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("for") != "ses_sub" {
			http.Error(w, `{"error":"no link"}`, http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"summary":"refactoring the ledger"}`)
	})
	mux.HandleFunc("POST /links/lnk_1/orchestrate/message", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, body.Text)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"delivered"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &sent
}

func TestOrchestrateSubjectsAndSummary(t *testing.T) {
	srv, _ := stubOrchestrateRouter(t)
	var out bytes.Buffer
	if err := cmdOrchestrate([]string{"subjects", "-session", "ses_lead", "-router", srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"subject_name": "payments"`) {
		t.Errorf("subjects output = %s", out.String())
	}

	out.Reset()
	if err := cmdOrchestrate([]string{"summary", "-session", "ses_lead", "-router", srv.URL, "-subject", "ses_sub"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "refactoring the ledger") {
		t.Errorf("summary output = %s", out.String())
	}
}

// send takes a subject id and resolves the link itself.
func TestOrchestrateSendResolvesSubjectToLink(t *testing.T) {
	srv, sent := stubOrchestrateRouter(t)
	t.Setenv("MADS_SESSION_ID", "ses_lead")
	t.Setenv("MADS_ROUTER_URL", srv.URL)
	var out bytes.Buffer
	if err := cmdOrchestrate([]string{"send", "-subject", "ses_sub", "-text", "ship it"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 || (*sent)[0] != "ship it" {
		t.Errorf("sent = %v, want [ship it]", *sent)
	}
	if !strings.Contains(out.String(), "delivered") {
		t.Errorf("send output = %s", out.String())
	}
}

func TestOrchestrateErrors(t *testing.T) {
	srv, _ := stubOrchestrateRouter(t)
	t.Setenv("MADS_SESSION_ID", "")
	var out bytes.Buffer
	for name, args := range map[string][]string{
		"no session":        {"subjects", "-router", srv.URL},
		"unlinked subject":  {"send", "-session", "ses_lead", "-router", srv.URL, "-subject", "ses_other", "-text", "hi"},
		"router 404":        {"summary", "-session", "ses_lead", "-router", srv.URL, "-subject", "ses_other"},
		"unknown command":   {"reboot", "-session", "ses_lead"},
		"send without text": {"send", "-session", "ses_lead", "-router", srv.URL, "-subject", "ses_sub"},
	} {
		if err := cmdOrchestrate(args, &out); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
