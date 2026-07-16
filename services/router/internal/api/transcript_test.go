package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// newTestServerWithFake wires a Router over a *held* fake.Fake instance,
// rather than a fresh one from testProviders, so a test can drive a real
// conversation through it and then assert the Router reads that same
// history back via GET /sessions/{id}/transcript.
func newTestServerWithFake(t *testing.T, fp *fake.Fake) (*httptest.Server, string) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	reg := provider.NewRegistry()
	if err := reg.Register(fp); err != nil {
		t.Fatal(err)
	}
	opts := testOptions(t, data, sup)
	opts.Providers = reg
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	return srv, base
}

// driveConversation starts a fake session, sends "echo hi", and drains
// events until the turn ends, so both the user and agent messages land in
// fp's history (fake.go's Send records the user message synchronously but
// the agent reply only at turn end).
func driveConversation(t *testing.T, fp *fake.Fake) string {
	t.Helper()
	ctx := context.Background()
	sess, err := fp.Start(ctx, provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(ctx, "echo hi"); err != nil {
		t.Fatal(err)
	}
	for ev := range sess.Events() {
		if ev.Kind == provider.EventTurnEnd {
			break
		}
	}
	return sess.Ref()
}

// createSessionDirect writes sess straight to the Data Service (bypassing
// POST /sessions, which would try to spawn a real child through the
// supervisor — this endpoint doesn't need one) and returns the assigned ID.
func createSessionDirect(t *testing.T, base string, sess contracts.Session) string {
	t.Helper()
	data := dataclient.New(base, "router")
	if err := data.CreateSession(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	return sess.ID
}

func TestSessionTranscriptActiveWithMessages(t *testing.T) {
	fp := fake.New()
	srv, base := newTestServerWithFake(t, fp)
	ref := driveConversation(t, fp)
	id := createSessionDirect(t, base, contracts.Session{
		Name: "active-with-messages", Provider: "fake", ProviderRef: ref, Activity: contracts.SessionActive,
	})

	resp, err := http.Get(srv.URL + "/sessions/" + id + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	msgs := decodeJSON[[]contracts.TranscriptMessage](t, resp)
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Text != "echo hi" {
		t.Errorf("msgs[0] = %+v, want {role: user, text: echo hi}", msgs[0])
	}
	if msgs[1].Role != "agent" || msgs[1].Text != "you said hi" {
		t.Errorf("msgs[1] = %+v, want {role: agent, text: you said hi}", msgs[1])
	}
}

func TestSessionTranscriptInactive(t *testing.T) {
	fp := fake.New()
	srv, base := newTestServerWithFake(t, fp)
	ref := driveConversation(t, fp)
	id := createSessionDirect(t, base, contracts.Session{
		Name: "inactive-with-messages", Provider: "fake", ProviderRef: ref, Activity: contracts.SessionInactive,
	})

	resp, err := http.Get(srv.URL + "/sessions/" + id + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	msgs := decodeJSON[[]contracts.TranscriptMessage](t, resp)
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2 (Activity must not be consulted): %+v", len(msgs), msgs)
	}
}

func TestSessionTranscriptNoConversation(t *testing.T) {
	srv := newTestServer(t)
	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "no-conversation", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)

	tresp, err := http.Get(srv.URL + "/sessions/" + created.Session.ID + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	if tresp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", tresp.StatusCode)
	}
	defer func() { _ = tresp.Body.Close() }()
	body, err := io.ReadAll(tresp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "[]\n" {
		t.Errorf("body = %q, want literal empty array, not null", body)
	}
	var msgs []contracts.TranscriptMessage
	if err := json.Unmarshal(body, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("len(msgs) = %d, want 0", len(msgs))
	}
}

func TestSessionTranscriptUnknownID(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/sessions/nonexistent/transcript")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, resp)
	if body["error"] == "" {
		t.Errorf("expected non-empty error field, got %+v", body)
	}
}
