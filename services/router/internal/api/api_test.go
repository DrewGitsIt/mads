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
	"time"

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
	gokeyring "github.com/zalando/go-keyring"
)

var testChildBin string
var sessionBin string

func TestMain(m *testing.M) {
	// credstore.Set needs a data key from the OS keyring; run every test in
	// this binary against an in-memory mock so the suite passes without a
	// real Secret Service/D-Bus session (CI, containers).
	gokeyring.MockInit()

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

	// A real mads binary beside the session binary: the session child's
	// orchestrate capability resolves it with madsbin.Path (sibling of its
	// own executable first), the same layout bin/ has in a real install.
	// It's also put on PATH for anything else in these tests that runs it.
	build = exec.Command("go", "build", "-o", filepath.Join(dir, "mads"),
		"github.com/drewkidwell/mads/cmd/mads")
	if out, err := build.CombinedOutput(); err != nil {
		panic("build mads: " + err.Error() + "\n" + string(out))
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		panic("prepend PATH: " + err.Error())
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

func httpDelete(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil) //nolint:noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
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
	body := decodeJSON[map[string]string](t, resp)
	if body["service"] != "router" || body["status"] != "ok" {
		t.Errorf("unexpected health body: %v", body)
	}
	// No identity configured: the fields are absent, which is what an older
	// build looks like to scripts/app.sh.
	for _, k := range []string{"root", "ui_dist", "started_at"} {
		if _, ok := body[k]; ok {
			t.Errorf("health body has %q without an identity: %v", k, body)
		}
	}
}

// TestHealthReportsIdentity pins the fields scripts/app.sh reads to refuse
// reusing a Router launched from a different checkout.
func TestHealthReportsIdentity(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	opts := testOptions(t, data, sup)
	opts.Root = "/src/checkout"
	opts.UIDist = "/src/checkout/ui/dist"
	opts.StartedAt = time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := decodeJSON[map[string]string](t, resp)
	want := map[string]string{
		"service": "router", "status": "ok", "root": "/src/checkout",
		"ui_dist": "/src/checkout/ui/dist", "started_at": "2026-09-28T10:00:00Z",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("health %s = %q, want %q (body %v)", k, body[k], v, body)
		}
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

// TestDeleteSession proves delete terminates rather than refuses: a session
// with a live tracked child (spawned via testchild, see TestResumeSession)
// is deletable in a single call, with the child stopped before the record
// goes. Delete used to 409 on both counts.
func TestDeleteSession(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	srv := httptest.NewServer(api.New(testOptions(t, data, sup)))
	t.Cleanup(srv.Close)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "delete test", "provider": "fake"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	if _, tracked := sup.SessionAddr(created.Session.ID); !tracked {
		t.Fatal("create session: no child tracked; this test needs a live one to terminate")
	}

	resp = httpDelete(t, srv.URL+"/sessions/"+created.Session.ID)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete-while-active status = %d, want 204 (terminate, don't refuse)", resp.StatusCode)
	}
	if addr, tracked := sup.SessionAddr(created.Session.ID); tracked {
		t.Fatalf("child still tracked at %s after delete: the child was not terminated first", addr)
	}

	resp, err := http.Get(srv.URL + "/sessions/" + created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get session after delete status = %d, want 404", resp.StatusCode)
	}

	resp = httpDelete(t, srv.URL+"/sessions/unknown-id")
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

func TestSetSessionTrust(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "trust me", "provider": "fake"})
	created := decodeJSON[struct {
		Session struct {
			ID    string `json:"id"`
			Trust string `json:"trust"`
		} `json:"session"`
	}](t, resp)

	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/trust", map[string]string{"trust": "full"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set trust status = %d, want 200", resp.StatusCode)
	}
	updated := decodeJSON[struct {
		Trust string `json:"trust"`
	}](t, resp)
	if updated.Trust != "full" {
		t.Fatalf("trust = %q, want %q", updated.Trust, "full")
	}

	// GET reflects the persisted change (the respawned child reads this
	// record at start — trust is spawn-time-only in the runner).
	getResp, err := http.Get(srv.URL + "/sessions/" + created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	fetched := decodeJSON[struct {
		Trust string `json:"trust"`
	}](t, getResp)
	if fetched.Trust != "full" {
		t.Fatalf("fetched trust = %q, want %q", fetched.Trust, "full")
	}

	// Fail closed on anything outside the registry — a typo must never fall
	// through to the provider's default (full-access) spawn branch.
	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/trust", map[string]string{"trust": "Full Access"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid trust status = %d, want 400", resp.StatusCode)
	}
}

// TestSetSessionModel covers the endpoint that was missing entirely: the UI
// had no way to change a session's model after creation, so the header pill
// and "/model" only ever updated local React state. Asserts both halves —
// the record is persisted AND the live child is reconfigured — since either
// one alone reproduces the original "switching models does nothing" bug.
func TestSetSessionModel(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "switch me", "provider": "fake"})
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Addr string `json:"addr"`
	}](t, resp)

	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/model", map[string]string{"model": "claude-opus-5"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set model status = %d, want 200", resp.StatusCode)
	}
	out := decodeJSON[contracts.SetSessionModelResponse](t, resp)
	if out.Session.Model != "claude-opus-5" {
		t.Fatalf("response model = %q, want %q", out.Session.Model, "claude-opus-5")
	}
	if out.Queued {
		t.Fatal("queued = true, want false — the child reported an immediate switch")
	}

	// Persisted: this is what the next spawn reads.
	getResp, err := http.Get(srv.URL + "/sessions/" + created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	fetched := decodeJSON[contracts.Session](t, getResp)
	if fetched.Model != "claude-opus-5" {
		t.Fatalf("fetched model = %q, want %q", fetched.Model, "claude-opus-5")
	}

	// Applied to the running child too — a persisted-only change would leave
	// the agent answering on the old model for the rest of the session.
	childResp, err := http.Get("http://" + created.Addr + "/model")
	if err != nil {
		t.Fatal(err)
	}
	child := decodeJSON[struct {
		Model string `json:"model"`
	}](t, childResp)
	if child.Model != "claude-opus-5" {
		t.Fatalf("child model = %q, want %q — the switch never reached the agent", child.Model, "claude-opus-5")
	}
}

// TestSetSessionModelQueuedMidTurn: the child defers a switch requested while
// a turn is in flight rather than interrupting the agent, and the Router
// passes that through so the UI can say the change lands at the turn's end
// instead of claiming it already took effect. ("queued" is testchild's cue to
// take that branch — see services/router/internal/testchild.)
func TestSetSessionModelQueuedMidTurn(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "busy", "provider": "fake"})
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/model", map[string]string{"model": "queued"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set model status = %d, want 200", resp.StatusCode)
	}
	out := decodeJSON[contracts.SetSessionModelResponse](t, resp)
	if !out.Queued {
		t.Fatal("queued = false, want true — a mid-turn switch must be reported as deferred")
	}
	// Still persisted: deferral is about the running process, not the record.
	if out.Session.Model != "queued" {
		t.Fatalf("response model = %q, want %q", out.Session.Model, "queued")
	}
}

// TestSetSessionModelRejectsEmpty and the child-failure case: an empty model
// would otherwise persist a blank binding and spawn the provider with no
// --model at all, silently falling back to its default.
func TestSetSessionModelRejectsEmpty(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "empty", "provider": "fake", "model": "claude-sonnet-5"})
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)

	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/model", map[string]string{"model": "   "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty model status = %d, want 400", resp.StatusCode)
	}

	// A child that refuses the switch is a 502 — the record is already
	// updated by then, which the message says rather than hiding.
	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/model", map[string]string{"model": "not_ready"})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("child-refused status = %d, want 502", resp.StatusCode)
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
