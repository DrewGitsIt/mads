package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// transcriptStub is the fake provider with a fixed durable transcript for
// every ref, so the Router's dormant-session path (transcriptFor) reads a
// realistic multi-turn conversation without driving one.
type transcriptStub struct {
	*fake.Fake
	msgs []provider.Message
}

func (s transcriptStub) Transcript(context.Context, string) ([]provider.Message, error) {
	return s.msgs, nil
}

// newTestServerWithProviderData is newTestServerWithProvider (reads_test.go)
// that also returns the Data Service base URL, for createSessionDirect.
func newTestServerWithProviderData(t *testing.T, p provider.Provider) (*httptest.Server, string) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	reg := provider.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	opts := testOptions(t, data, sup)
	opts.Providers = reg
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	return srv, base
}

// TestOrchestrateGetTurnsReadsTranscript proves get_session_turns renders
// the subject's transcript turns (not its turn_ended deltas), slices them by
// count/from_turn, refuses a from_turn past the end with the turn count, and
// that list_linked_sessions' ?turns=1 and get_session_summary report the
// same count (2026-10-19).
func TestOrchestrateGetTurnsReadsTranscript(t *testing.T) {
	stub := transcriptStub{Fake: fake.New(), msgs: []provider.Message{
		{Role: "user", Text: "count the rows"},
		{Role: "tool", Tool: &provider.ToolUse{ID: "t1", Name: "Bash", Input: json.RawMessage(`{"command":"wc -l rows.jsonl"}`)}},
		{Role: "tool_result", Tool: &provider.ToolUse{ID: "t1"}, Text: "800 rows.jsonl"},
		{Role: "agent", Text: "800 rows."},
		{Role: "user", Text: "now label them"},
		{Role: "agent", Text: "Labeling."},
	}}
	srv, base := newTestServerWithProviderData(t, stub)
	setLinkedSessionsFlag(t, srv.URL, true)

	leaderID := createSessionDirect(t, base, contracts.Session{Name: "leader", Provider: "fake"})
	subjectID := createSessionDirect(t, base, contracts.Session{Name: "Labeler", Provider: "fake", ProviderRef: "ref-labeler"})
	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leaderID,
		SubjectSessionID:  subjectID,
		Preset:            contracts.PresetLeader,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create link: status %d", createResp.StatusCode)
	}

	get := func(query string) (int, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + "/sessions/" + leaderID + "/orchestrate/turns?for=" + subjectID + query)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	status, body := get("")
	if status != http.StatusOK || !strings.HasPrefix(body, "Labeler — turn 2 of 2\n") ||
		!strings.Contains(body, "## Turn 2 · user\nnow label them\nagent: Labeling.\n") || strings.Contains(body, "count the rows") {
		t.Fatalf("default slice: status %d\n%s", status, body)
	}

	status, body = get("&from_turn=1&count=1")
	want := "Labeler — turn 1 of 2\n\n## Turn 1 · user\ncount the rows\n" +
		"tool Bash {\"command\":\"wc -l rows.jsonl\"}\n  → 800 rows.jsonl\nagent: 800 rows.\n"
	if status != http.StatusOK || body != want {
		t.Fatalf("from_turn 1: status %d\n%s\nwant:\n%s", status, body, want)
	}

	status, body = get("&from_turn=3")
	if status != http.StatusBadRequest || !strings.Contains(body, "Labeler has 2 turns") {
		t.Fatalf("from_turn past the end: status %d, %q", status, body)
	}

	type subjectRow struct {
		SubjectSessionID string `json:"subject_session_id"`
		Turns            *int   `json:"turns"`
	}
	for _, tc := range []struct {
		query     string
		wantTurns bool
	}{{"", false}, {"?turns=1", true}} {
		resp, err := http.Get(srv.URL + "/sessions/" + leaderID + "/orchestrate/subjects" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		rows := decodeJSON[[]subjectRow](t, resp)
		if len(rows) != 1 {
			t.Fatalf("subjects%s = %+v", tc.query, rows)
		}
		if tc.wantTurns && (rows[0].Turns == nil || *rows[0].Turns != 2) {
			t.Errorf("subjects%s turns = %v, want 2", tc.query, rows[0].Turns)
		}
		if !tc.wantTurns && rows[0].Turns != nil {
			t.Errorf("subjects%s turns = %d, want omitted (it reads every transcript)", tc.query, *rows[0].Turns)
		}
	}

	resp, err := http.Get(srv.URL + "/sessions/" + leaderID + "/orchestrate/summary?for=" + subjectID)
	if err != nil {
		t.Fatal(err)
	}
	summary := decodeJSON[map[string]any](t, resp)
	if summary["turns"] != float64(2) {
		t.Errorf("summary = %+v, want turns 2", summary)
	}
}
