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
	"github.com/drewkidwell/mads/pkg/registryclient"
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
var sessionBin string

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

	// The real session binary, needed by TestSessionTranscriptProxiesLiveChild
	// to exercise the Router->child transcript proxy against a genuine
	// Session Service process rather than testchild's stub.
	sessionBin = filepath.Join(dir, "mads-session")
	build = exec.Command("go", "build", "-o", sessionBin,
		"github.com/drewkidwell/mads/services/session")
	if out, err := build.CombinedOutput(); err != nil {
		panic("build session: " + err.Error() + "\n" + string(out))
	}

	code := m.Run()

	if err := testleakcheck.Verify(testChildBin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	if err := testleakcheck.Verify(sessionBin); err != nil {
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
	return job.New(data, fm, testRegistry(t))
}

// testRegistry returns an unconfigured registry client — most tests don't
// exercise /registry/* and just need something inert to plug into job.New /
// api.New's required argument.
func testRegistry(t *testing.T) *registryclient.Client {
	t.Helper()
	return registryclient.New("")
}

// testOptions returns a baseline api.Options over the given data client and
// supervisor, with inert fakes/stubs for every other dependency. Individual
// tests override whichever fields their scenario needs — a new Options
// field only needs a default added here, not an edit in every test file.
func testOptions(t *testing.T, data *dataclient.Client, sup *supervisor.Supervisor) api.Options {
	t.Helper()
	return api.Options{
		Data:      data,
		Sup:       sup,
		Jobs:      testJobs(t, data),
		Providers: testProviders(t),
		Creds:     testCredStore(t),
		VaultRefs: testVaultRefs(t),
		Version:   "test",
		Registry:  testRegistry(t),
	}
}

// newTestServer wires a real datatest Data Service, a Supervisor that spawns
// the fast testchild binary, and the Router's own api.Handler in front of it.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	srv := httptest.NewServer(api.New(testOptions(t, data, sup)))
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

// TestCreateTerminateSession spawns a real Session Service child (not
// testchild's stub) because asserting the post-terminate Activity flip
// exercises the real runner.Shutdown path — testchild has no such logic.
func TestCreateTerminateSession(t *testing.T) {
	srv, _ := newTestServerWithRealSession(t)

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

	// The session record survives terminate, left inactive — a second
	// terminate is an idempotent no-op (Supervisor.TerminateSession's own
	// doc comment: successful call if no child is tracked), not a 404.
	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/terminate", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("second terminate status = %d, want 204 (idempotent no-op)", resp.StatusCode)
	}

	resp, err := http.Get(srv.URL + "/sessions/" + created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get session after terminate status = %d, want 200 (record survives)", resp.StatusCode)
	}
	sess := decodeJSON[struct {
		Activity string `json:"activity"`
	}](t, resp)
	if sess.Activity != "inactive" {
		t.Fatalf("session activity after terminate = %q, want %q", sess.Activity, "inactive")
	}
}

// TestResumeSession spawns via testchild (fast, no real provider) since the
// fake provider's session refs don't survive a real process boundary — a
// resumed child would fail to start with "unknown session ref". testchild
// doesn't flip Activity on terminate the way the real Session Service does
// (see TestCreateTerminateSession), so this test flips it directly through
// the Data Service to simulate that already having happened.
func TestResumeSession(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "router")

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "resume test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/terminate", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("terminate status = %d, want 204", resp.StatusCode)
	}

	sess, err := data.Session(context.Background(), created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess.Activity = contracts.SessionInactive
	if err := data.UpdateSession(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}

	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/resume", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume status = %d, want 200", resp.StatusCode)
	}
	resumed := decodeJSON[struct {
		Addr string `json:"addr"`
	}](t, resp)
	if resumed.Addr == "" {
		t.Fatal("resume session: no addr discovered from spawned child")
	}

	// Resuming an already-active session is a conflict.
	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/resume", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("resume-while-active status = %d, want 409", resp.StatusCode)
	}

	resp = postJSON(t, srv.URL+"/sessions/unknown-id/resume", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("resume unknown id status = %d, want 404", resp.StatusCode)
	}
}

// TestDeleteSession spawns via testchild (see TestResumeSession) and flips
// Activity directly through the Data Service after terminate to simulate
// what the real Session Service's Shutdown does (proven separately in
// TestCreateTerminateSession).
func TestDeleteSession(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "router")

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "delete test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	// Deleting a still-active session is a conflict.
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/sessions/"+created.Session.ID, nil) //nolint:noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete-while-active status = %d, want 409", resp.StatusCode)
	}

	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/terminate", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("terminate status = %d, want 204", resp.StatusCode)
	}

	sess, err := data.Session(context.Background(), created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess.Activity = contracts.SessionInactive
	if err := data.UpdateSession(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}

	req, err = http.NewRequest(http.MethodDelete, srv.URL+"/sessions/"+created.Session.ID, nil) //nolint:noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/sessions/" + created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get session after delete status = %d, want 404", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodDelete, srv.URL+"/sessions/unknown-id", nil) //nolint:noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown id status = %d, want 404", resp.StatusCode)
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
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	ws := &contracts.Workspace{Name: "ws1", Repos: []contracts.WorkspaceRepo{{Path: t.TempDir(), Primary: true}}}
	if err := data.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	tpl := &contracts.Template{Name: "nightly", Source: "name: nightly\nstages:\n  - name: verify\n    command: make test\n"}
	if err := data.CreateTemplate(context.Background(), tpl); err != nil {
		t.Fatal(err)
	}
	sess := &contracts.Session{Name: "s1", Workspace: ws.ID, Provider: "fake"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	task := &contracts.Task{SessionID: sess.ID, Title: "t1", Status: contracts.StatusReady, Source: "user"}
	if err := data.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, srv.URL+"/pipelines", map[string]string{"session_id": sess.ID, "template_id": tpl.ID})
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

// TestStartPipelineNoWorkspace covers "201 + silence must be impossible": a
// session with no workspace gets a clear 4xx and no child is ever spawned
// (the suite-wide testleakcheck in TestMain fails if one were left running).
func TestStartPipelineNoWorkspace(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, srv.URL+"/pipelines", map[string]string{"session_id": sess.ID, "template_id": "tpl_x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, resp)
	if body["error"] == "" {
		t.Fatalf("body = %+v, want a human-readable error", body)
	}
}

// TestStartPipelineNoReadyTasks covers the same "clear 4xx, no orphan child"
// requirement when the session has a workspace but nothing to run.
func TestStartPipelineNoReadyTasks(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	ws := &contracts.Workspace{Name: "ws1", Repos: []contracts.WorkspaceRepo{{Path: t.TempDir(), Primary: true}}}
	if err := data.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	tpl := &contracts.Template{Name: "nightly", Source: "name: nightly\nstages:\n  - name: verify\n    command: make test\n"}
	if err := data.CreateTemplate(context.Background(), tpl); err != nil {
		t.Fatal(err)
	}
	sess := &contracts.Session{Name: "s1", Workspace: ws.ID}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, srv.URL+"/pipelines", map[string]string{"session_id": sess.ID, "template_id": tpl.ID})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, resp)
	if body["error"] == "" {
		t.Fatalf("body = %+v, want a human-readable error", body)
	}
}

// TestStartPipelineInvalidTemplate covers the same requirement for an
// unresolvable template_id.
func TestStartPipelineInvalidTemplate(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	ws := &contracts.Workspace{Name: "ws1", Repos: []contracts.WorkspaceRepo{{Path: t.TempDir(), Primary: true}}}
	if err := data.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	sess := &contracts.Session{Name: "s1", Workspace: ws.ID}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	task := &contracts.Task{SessionID: sess.ID, Title: "t1", Status: contracts.StatusReady, Source: "user"}
	if err := data.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, srv.URL+"/pipelines", map[string]string{"session_id": sess.ID, "template_id": "nonexistent"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeJSON[map[string]string](t, resp)
	if body["error"] == "" {
		t.Fatalf("body = %+v, want a human-readable error", body)
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
