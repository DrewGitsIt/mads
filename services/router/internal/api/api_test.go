package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/testleakcheck"
	"github.com/drewkidwell/mads/pkg/vault"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
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

	testChildBin = filepath.Join(dir, "testchild")
	build := exec.Command("go", "build", "-o", testChildBin,
		"github.com/drewkidwell/mads/services/router/internal/testchild")
	if out, err := build.CombinedOutput(); err != nil {
		panic("build testchild: " + err.Error() + "\n" + string(out))
	}

	code := m.Run()

	if err := testleakcheck.Verify(testChildBin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
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
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	srv := httptest.NewServer(api.New(data, sup, testJobs(t, data), testProviders(t), testCredStore(t), nil, testVaultRefs(t), nil, nil, "test"))
	t.Cleanup(srv.Close)
	return srv
}

// testProviders returns a registry with just the fake provider registered,
// matching the test fixtures' existing philosophy of never touching a real
// provider backend.
func testProviders(t *testing.T) *provider.Registry {
	t.Helper()
	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatal(err)
	}
	return reg
}

// testCredStore opens a credential store under a fresh temp dir — isolated
// per test, never touching a real ~/.mads.
func testCredStore(t *testing.T) *credstore.Store {
	t.Helper()
	cs, err := credstore.Open(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// testVaultRefs opens a vault ref store under a fresh temp dir — isolated
// per test, never touching a real ~/.mads.
func testVaultRefs(t *testing.T) *vault.RefStore {
	t.Helper()
	rs, err := vault.OpenRefStore(filepath.Join(t.TempDir(), "vault-refs.json"))
	if err != nil {
		t.Fatal(err)
	}
	return rs
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

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "test session", "provider": "fake"})
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

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "bounce me", "provider": "fake"})
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

func TestRetryTask(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	task := &contracts.Task{SessionID: sess.ID, Title: "t1", Status: contracts.StatusBlocked, Source: "user"}
	if err := data.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, srv.URL+"/tasks/"+task.ID+"/retry", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", resp.StatusCode)
	}
	retried := decodeJSON[contracts.Task](t, resp)
	if retried.Status != contracts.StatusReady {
		t.Fatalf("retried task status = %s, want ready", retried.Status)
	}
	if retried.RetryCount != 1 {
		t.Fatalf("retried task retry_count = %d, want 1", retried.RetryCount)
	}
}

func TestRetryTaskNotFound(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/tasks/nonexistent/retry", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
