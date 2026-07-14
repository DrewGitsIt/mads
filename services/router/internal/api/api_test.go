package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

var testChildBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mads-router-api-test")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	testChildBin = filepath.Join(dir, "testchild")
	build := exec.Command("go", "build", "-o", testChildBin,
		"github.com/drewkidwell/mads/services/router/internal/testchild")
	if out, err := build.CombinedOutput(); err != nil {
		panic("build testchild: " + err.Error() + "\n" + string(out))
	}

	os.Exit(m.Run())
}

// testJobs builds a job.Runner over a fake-backed Fast Model — no scripted
// reply needed for tests that don't exercise agent generation.
func testJobs(t *testing.T, data *dataclient.Client) *job.Runner {
	t.Helper()
	fm := fastmodel.New(fake.New(), fastmodel.Config{})
	return job.New(data, fm)
}

// newTestServer wires a real datatest Data Service, a Supervisor that spawns
// the fast testchild binary, and the Router's own api.Handler in front of it.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin})
	srv := httptest.NewServer(api.New(data, sup, testJobs(t, data)))
	t.Cleanup(srv.Close)
	return srv
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw)) //nolint:gosec,noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeJSON[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return v
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestCreateTerminateSession(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "test session"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"session"`
		Addr string `json:"addr"`
	}](t, resp)
	if created.Session.ID == "" {
		t.Fatal("create session: no ID assigned")
	}
	if created.Addr == "" {
		t.Fatal("create session: no addr discovered from spawned child")
	}

	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/terminate", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("terminate status = %d, want 204", resp.StatusCode)
	}

	// The session record is gone too — DeleteSession ran after Terminate.
	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/terminate", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second terminate status = %d, want 404 (session record already deleted)", resp.StatusCode)
	}
}

func TestCreateSessionValidation(t *testing.T) {
	srv := newTestServer(t)

	// Missing required "name" — the Data Service rejects it, and the Router
	// maps that ValidationError to 400 (not the Data Service's own 422).
	resp := postJSON(t, srv.URL+"/sessions", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestBounceSession(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "bounce me"})
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Addr string `json:"addr"`
	}](t, resp)

	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/bounce", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bounce status = %d, want 200", resp.StatusCode)
	}
	bounced := decodeJSON[struct {
		Addr string `json:"addr"`
	}](t, resp)
	if bounced.Addr == "" {
		t.Fatal("bounce session: no addr discovered from respawned child")
	}
}

func TestStartCancelPipeline(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/pipelines", map[string]string{"session_id": "ses_x", "template_id": "tpl_x"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d, want 201", resp.StatusCode)
	}
	started := decodeJSON[struct {
		RunID string `json:"run_id"`
		Addr  string `json:"addr"`
	}](t, resp)
	if started.RunID == "" || started.Addr == "" {
		t.Fatalf("start pipeline: incomplete response: %+v", started)
	}

	resp = postJSON(t, srv.URL+"/pipelines/"+started.RunID+"/cancel", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel status = %d, want 204", resp.StatusCode)
	}

	// Cancel is only valid once — the run is forgotten after Terminate's
	// exit handling completes.
	resp = postJSON(t, srv.URL+"/pipelines/"+started.RunID+"/cancel", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second cancel status = %d, want 404", resp.StatusCode)
	}
}

func TestStartPipelineValidation(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/pipelines", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	verr := decodeJSON[struct {
		Errors []struct {
			Field string `json:"field"`
		} `json:"errors"`
	}](t, resp)
	if len(verr.Errors) != 2 {
		t.Fatalf("errors = %+v, want 2 (session_id, template_id)", verr.Errors)
	}
}
