package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// newTrustTestServer is newTestServer with the session argv supplied by the
// caller and the data client handed back, so a test can read the event log
// the handler appended to.
func newTrustTestServer(t *testing.T, sessionArgv []string) (*httptest.Server, *dataclient.Client) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, sessionArgv, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	srv := httptest.NewServer(api.New(testOptions(t, data, sup)))
	t.Cleanup(srv.Close)
	return srv, data
}

func createTrustSession(t *testing.T, srvURL string) string {
	t.Helper()
	resp := postJSON(t, srvURL+"/sessions", map[string]string{"name": "trust log", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID    string `json:"id"`
			Trust string `json:"trust"`
		} `json:"session"`
	}](t, resp)
	if created.Session.Trust != string(contracts.TrustLimited) {
		t.Fatalf("new session trust = %q, want the builtin default %q", created.Session.Trust, contracts.TrustLimited)
	}
	return created.Session.ID
}

func trustChanges(t *testing.T, data *dataclient.Client, sessionID string) []contracts.TrustChange {
	t.Helper()
	envs, err := data.Events(context.Background(), 0, sessionID, []string{string(contracts.SessionTrustChanged)})
	if err != nil {
		t.Fatal(err)
	}
	var out []contracts.TrustChange
	for _, env := range envs {
		var c contracts.TrustChange
		if err := json.Unmarshal(env.Payload, &c); err != nil {
			t.Fatalf("decode %s payload: %v", env.Type, err)
		}
		if env.Actor != "user" {
			t.Errorf("actor = %q, want %q", env.Actor, "user")
		}
		out = append(out, c)
	}
	return out
}

// A trust change is logged as session.trust_changed — the conversation's
// inline, reload-proof record of it — carrying from/to and that the live
// child was restarted onto it. Re-setting the same level logs nothing.
func TestSetSessionTrustLogsChange(t *testing.T) {
	srv, data := newTrustTestServer(t, []string{testChildBin})
	id := createTrustSession(t, srv.URL)

	resp := postJSON(t, srv.URL+"/sessions/"+id+"/trust", map[string]string{"trust": "full"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set trust status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	want := contracts.TrustChange{From: contracts.TrustLimited, To: contracts.TrustFull, Restarted: true}
	if got := trustChanges(t, data, id); len(got) != 1 || got[0] != want {
		t.Fatalf("trust changes = %+v, want [%+v]", got, want)
	}

	resp = postJSON(t, srv.URL+"/sessions/"+id+"/trust", map[string]string{"trust": "full"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("same-value set status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	if got := trustChanges(t, data, id); len(got) != 1 {
		t.Fatalf("after re-setting the same level: %d trust changes, want still 1: %+v", len(got), got)
	}

	// No live child: the change waits for the next spawn, and says so.
	resp = postJSON(t, srv.URL+"/sessions/"+id+"/terminate", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("terminate status = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
	resp = postJSON(t, srv.URL+"/sessions/"+id+"/trust", map[string]string{"trust": "read_only"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set trust (no child) status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	got := trustChanges(t, data, id)
	want = contracts.TrustChange{From: contracts.TrustFull, To: contracts.TrustReadOnly}
	if len(got) != 2 || got[1] != want {
		t.Fatalf("trust changes = %+v, want a second %+v", got, want)
	}
}

// A bounce that fails still logs the change — the record is updated — but
// not as restarted, and with the reason, so the notice can say it applies
// when the agent next starts.
func TestSetSessionTrustLogsFailedRestart(t *testing.T) {
	// The session argv is a wrapper that refuses to start once a marker
	// exists: the first spawn (create) succeeds, the bounce's respawn fails.
	dir := t.TempDir()
	marker := filepath.Join(dir, "refuse")
	script := filepath.Join(dir, "child.sh")
	body := "#!/bin/sh\n[ -e '" + marker + "' ] && exit 1\nexec '" + testChildBin + "' \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, data := newTrustTestServer(t, []string{"/bin/sh", script})
	id := createTrustSession(t, srv.URL)

	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, srv.URL+"/sessions/"+id+"/trust", map[string]string{"trust": "full"})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("set trust status = %d, want 502 (bounce failed)", resp.StatusCode)
	}
	resp.Body.Close()
	got := trustChanges(t, data, id)
	if len(got) != 1 {
		t.Fatalf("trust changes = %+v, want exactly 1", got)
	}
	c := got[0]
	if c.From != contracts.TrustLimited || c.To != contracts.TrustFull || c.Restarted || c.RestartError == "" {
		t.Fatalf("trust change = %+v, want limited→full, not restarted, with a restart error", c)
	}
}
