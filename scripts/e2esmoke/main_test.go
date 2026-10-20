//go:build e2e_smoke

// Package e2esmoke drives the full mads service topology end to end (spec
// §15, W7.7): real Data Service, Router, and Session Service binaries; a
// session created through the Router (spawning a real session child); a
// scripted pipeline run over providers/fake via the built mads CLI; a retro
// generated over a scripted Fast Model fixture; and a final orphan-process
// check. Kept out of `go test ./...`/`make check` by this build tag — it
// spawns real OS processes and runs a real (if fast) pipeline, on the order
// of seconds to low tens of seconds, not the sub-second unit-test norm
// elsewhere in this repo. Run it with `make e2e-smoke`.
//
// The "pipeline" binary is deliberately never built or spawned here:
// services/pipeline/main.go is, as of this writing, a health-check-only
// skeleton — nothing calls services/pipeline/engine.Run from an HTTP
// trigger, so a pipeline started via the Router's POST /pipelines does
// nothing. The actual, working, already-documented way to run a pipeline in
// this codebase is `cmd/mads run` (see the root README's "-provider fake"
// paragraph), which calls engine.Run in-process against a real Data
// Service — a genuine, already-wired execution path, just not the Router
// endpoint of the same name. Wiring services/pipeline/main.go to engine.Run
// is a real missing feature (needs an HTTP trigger + a way to pass
// template/session into a spawned process); it's a Pipeline Engine/Router
// design change, out of scope for this test-infra work item. The Router is
// still spawned with -pipeline-cmd pointed at the harmless session binary
// (never actually invoked, since nothing here calls POST /pipelines) purely
// so a hypothetical future probe wouldn't hit a flag.Parse error.
package e2esmoke

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/binlocate"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/testleakcheck"
)

var (
	madsBin    string
	dataBin    string
	routerBin  string
	sessionBin string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mads-e2e-smoke")
	if err != nil {
		panic(err)
	}

	madsBin = filepath.Join(dir, "mads")
	dataBin = filepath.Join(dir, "data")
	routerBin = filepath.Join(dir, "router")
	sessionBin = filepath.Join(dir, "session")

	for _, b := range []struct{ out, pkg string }{
		{madsBin, "github.com/drewkidwell/mads/cmd/mads"},
		{dataBin, "github.com/drewkidwell/mads/services/data"},
		{routerBin, "github.com/drewkidwell/mads/services/router"},
		{sessionBin, "github.com/drewkidwell/mads/services/session"},
	} {
		build := exec.Command("go", "build", "-o", b.out, b.pkg)
		if out, err := build.CombinedOutput(); err != nil {
			panic(fmt.Sprintf("build %s: %v\n%s", b.pkg, err, out))
		}
	}

	code := m.Run()

	// The "zero orphan processes" proof: only the session binary is ever
	// spawned as a genuine child in this flow (by the Router), so it's the
	// only one worth checking here.
	if err := testleakcheck.Verify(sessionBin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

var listeningRe = regexp.MustCompile(`listening on (\S+)`)

// listeningProc is a spawned service process discovered via the codebase's
// "listening on <addr>" stderr log-line convention (see
// services/router/internal/child/child.go — this test can't import that
// internal package, so it re-implements the same small pattern).
type listeningProc struct {
	cmd    *exec.Cmd
	addr   string
	exited chan struct{}
}

// spawnListening starts argv[0] with the rest of argv, waits up to 15s for
// a "listening on <addr>" line on stderr, and fails the test (with captured
// stderr) if the process exits first or times out.
func spawnListening(t *testing.T, name string, argv []string, env []string) *listeningProc {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // argv is test-constructed, not user input
	cmd.Env = env
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("%s: stderr pipe: %v", name, err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: start: %v", name, err)
	}

	p := &listeningProc{cmd: cmd, exited: make(chan struct{})}
	var mu sync.Mutex
	var lines []string
	captured := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}

	addrCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
			if m := listeningRe.FindStringSubmatch(line); m != nil {
				select {
				case addrCh <- m[1]:
				default:
				}
			}
		}
	}()
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()

	select {
	case addr := <-addrCh:
		p.addr = addr
		return p
	case <-p.exited:
		t.Fatalf("%s: exited before reporting a listen address:\n%s", name, captured())
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-p.exited
		t.Fatalf("%s: did not report a listen address in time:\n%s", name, captured())
	}
	return nil // unreachable, satisfies the compiler
}

// terminate sends SIGTERM, waits up to grace, then SIGKILL. Safe to call
// more than once (e.g. an explicit teardown call plus a defer safety net) —
// a no-op once the process has already exited.
func (p *listeningProc) terminate(t *testing.T, grace time.Duration) {
	t.Helper()
	if p == nil {
		return
	}
	select {
	case <-p.exited:
		return
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.exited:
		return
	case <-time.After(grace):
	}
	_ = p.cmd.Process.Kill()
	<-p.exited
}

// cleanEnv builds a process environment rooted at an isolated home dir, with
// no leaked MADS_HOME/MADS_DATA_DIR from the outer test process's own
// environment — mirrors scripts/smoketest/install_test.go's cleanEnv, so
// this test never touches the real ~/.mads. $MADS_AGENT_BIN_DIR points at
// an empty stand-in dir (pkg/binlocate), so the router's boot model
// discovery and its /preflight probes find no harness and never start a
// real claude, goose or kiro-cli from PATH or a known install location.
func cleanEnv(home string) []string {
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "MADS_DATA_DIR=") || strings.HasPrefix(e, "MADS_HOME=") {
			continue
		}
		env = append(env, e)
	}
	return append(env, "HOME="+home, "MADS_HOME="+home,
		binlocate.EnvAgentBinDir+"="+filepath.Join(home, "agent-bin-standins"))
}

func waitHealth(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health") //nolint:gosec,noctx // test helper, base is a local 127.0.0.1 process this test spawned
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s/health did not become healthy in time", base)
}

// fastModelFixture stands in for an OpenAI-compatible Fast Model endpoint
// (providers/fastmodel's -fastmodel-base-url transport) so the retro job's
// structured-JSON generation succeeds without a live LLM. This is necessary,
// not optional: the bare providers/fake backend the Router falls back to
// without -fastmodel-base-url always replies plain "ok" (providers/fake's
// built-in dialect), which contains no JSON object at all — fastmodel.Do's
// decode step would fail every one of its retry attempts, the retro job
// would error out, and CreateRetro (its one and only write) would never
// run. A local, scripted HTTP fixture serving one valid DPA reply is the
// same spirit as cmd/mads/update_test.go's httptest-served release feed —
// not a live model, and the Fast Model output is data (a DPA suggestion),
// not code, so a fixed reply is enough to prove the whole path end to end.
func fastModelFixture(t *testing.T) *httptest.Server {
	t.Helper()
	content := `{"dpas":[{"problem":"e2e smoke problem","evidence":"e2e smoke evidence",` +
		`"root_cause":"e2e smoke root cause","remediation_layer":"pipeline_template",` +
		`"suggested_action":"e2e smoke action"}]}`
	body, err := json.Marshal(map[string]any{
		"model":   "e2esmoke-fixture",
		"choices": []map[string]any{{"message": map[string]string{"content": content}}},
		"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setupWorkspace mirrors services/pipeline/engine/engine_test.go's
// newHarness fixture: a Makefile whose build/check targets trivially pass,
// and a git repo configured so the default template's bare `git merge
// --no-ff` merge stage succeeds without an explicit remote.
func setupWorkspace(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("build:\n\t@true\ntest:\n\t@true\ncheck:\n\t@true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", "."},
		{"-c", "user.email=e2e@smoke.test", "-c", "user.name=e2esmoke", "add", "."},
		{"-c", "user.email=e2e@smoke.test", "-c", "user.name=e2esmoke", "commit", "-q", "-m", "init"},
		{"config", "merge.defaultToUpstream", "true"},
		{"config", "branch.main.remote", "."},
		{"config", "branch.main.merge", "refs/heads/main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

type createSessionResponse struct {
	Session contracts.Session `json:"session"`
	Addr    string            `json:"addr"`
}

// postJSON POSTs body as JSON and decodes the response into out (skipped if
// nil). The Router's own wire shapes are frequently a wrapper around the
// bare contracts type dataclient expects (see handleCreateSession/
// handleStartRetro) — this is the manual escape hatch for those, matching
// this package's own doc comment on why dataclient.CreateSession can't be
// used against the Router directly.
func postJSON(t *testing.T, url string, body, out any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw)) //nolint:gosec,noctx // test helper, url is a local process this test spawned
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode response from %s: %v", url, err)
		}
	}
	return resp
}

// TestFullTopologySmoke is the acceptance criterion itself: create session
// (router → session child) → seed tasks → pipeline run → tasks done → retro
// artifacts exist → metrics/cost non-empty → teardown leaves zero orphan
// processes (checked in TestMain after this returns). The steps are
// sequentially dependent — splitting into independent t.Run subtests would
// just add ceremony without letting anything run in parallel.
func TestFullTopologySmoke(t *testing.T) {
	start := time.Now()
	tmp := t.TempDir()
	homeDir := filepath.Join(tmp, "home")
	dataDir := filepath.Join(tmp, "data")
	workspace := filepath.Join(tmp, "workspace")
	worktreeRoot := filepath.Join(tmp, "worktrees")
	for _, d := range []string{homeDir, dataDir, workspace, worktreeRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := cleanEnv(homeDir)

	// 1. Data Service.
	data := spawnListening(t, "data", []string{dataBin, "-addr", "127.0.0.1:0", "-data-dir", dataDir}, env)
	defer data.terminate(t, 5*time.Second)
	dataURL := "http://" + data.addr
	waitHealth(t, dataURL)

	// 2. Fast Model fixture (see fastModelFixture's doc comment).
	fm := fastModelFixture(t)

	// 3. Router.
	router := spawnListening(t, "router", []string{
		routerBin, "-addr", "127.0.0.1:0",
		"-data-url", dataURL,
		"-session-cmd", sessionBin + " -data-url " + dataURL,
		"-pipeline-cmd", sessionBin,
		"-fastmodel-base-url", fm.URL,
		"-mads-home", homeDir,
	}, env)
	defer router.terminate(t, 5*time.Second)
	routerURL := "http://" + router.addr
	waitHealth(t, routerURL)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 4. Create a session through the Router — the "router → session
	// children" proof. A raw POST, not dataclient.CreateSession: the
	// Router's handler wraps the bare contracts.Session shape dataclient
	// expects in {"session":..., "addr":...} (see postJSON's doc comment).
	var created createSessionResponse
	resp := postJSON(t, routerURL+"/sessions", map[string]string{"name": "e2e smoke", "provider": "fake"}, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session: status %d", resp.StatusCode)
	}
	if created.Session.ID == "" || created.Addr == "" {
		t.Fatalf("create session: incomplete response %+v", created)
	}
	sessionID := created.Session.ID

	// 5. Seed two independent tasks. dataclient's other methods are
	// bare-shaped and work fine against the Router base URL.
	client := dataclient.New(routerURL, "e2esmoke")
	seeded, err := client.ImportTasks(ctx, []contracts.Task{
		{SessionID: sessionID, Title: "e2e smoke task one", Source: "user"},
		{SessionID: sessionID, Title: "e2e smoke task two", Source: "user"},
	})
	if err != nil {
		t.Fatalf("seed tasks: %v", err)
	}

	// 6. Workspace fixture the pipeline run's build/test/merge stages need.
	setupWorkspace(t, workspace)

	// 7. Run the pipeline via the built mads CLI (the substituted leg — see
	// this package's doc comment on services/pipeline/main.go).
	runCmd := exec.Command(madsBin, "run", //nolint:gosec // madsBin is built by TestMain, not user input
		"-data", dataURL,
		"-session", sessionID,
		"-workspace", workspace,
		"-provider", "fake",
		"-stage-timeout", "30s",
		"-worktree-root", worktreeRoot,
	)
	runCmd.Env = env
	runOut, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mads run: %v\n%s", err, runOut)
	}

	doneTasks, err := client.Tasks(ctx, sessionID, contracts.StatusDone)
	if err != nil {
		t.Fatalf("list done tasks: %v", err)
	}
	if len(doneTasks) != len(seeded) {
		t.Fatalf("done tasks = %d, want %d (all seeded tasks)\nmads run output:\n%s", len(doneTasks), len(seeded), runOut)
	}

	// 8. Retro. POST /retros has no dataclient wrapper (only CreateRetro, a
	// direct write, is exposed there), so this is a raw POST too.
	var started struct {
		RetroID string `json:"retro_id"`
	}
	resp = postJSON(t, routerURL+"/retros", map[string]any{"session_ids": []string{sessionID}}, &started)
	if resp.StatusCode != http.StatusAccepted || started.RetroID == "" {
		t.Fatalf("start retro: status %d, body %+v", resp.StatusCode, started)
	}

	var retro contracts.Retro
	retroDeadline := time.Now().Add(30 * time.Second)
	for {
		retro, err = client.Retro(ctx, started.RetroID)
		if err == nil {
			break
		}
		if !errors.Is(err, dataclient.ErrNotFound) {
			t.Fatalf("poll retro: %v", err)
		}
		if time.Now().After(retroDeadline) {
			t.Fatalf("retro %s did not complete in time", started.RetroID)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(retro.SessionIDs) != 1 || retro.SessionIDs[0] != sessionID {
		t.Fatalf("retro session_ids = %v, want [%s]", retro.SessionIDs, sessionID)
	}
	if len(retro.DPAs) == 0 {
		t.Fatalf("retro %s has no DPAs", started.RetroID)
	}

	// 9. Cost metrics non-empty (spec §8's usage.recorded events, one per
	// agent stage one-shot — providers/fake's usage() is always non-zero
	// tokens, even though its CostUSD is always 0 with no scripted
	// override, so the assertion is on tokens, not dollars).
	cost, err := client.MetricsCost(ctx, sessionID)
	if err != nil {
		t.Fatalf("metrics cost: %v", err)
	}
	if len(cost.ByDay) == 0 || len(cost.BySession) == 0 {
		t.Fatalf("metrics/cost is empty for session %s: %+v", sessionID, cost)
	}
	var sawUsage bool
	for _, p := range cost.ByDay {
		if p.InputTokens > 0 && p.OutputTokens > 0 {
			sawUsage = true
		}
	}
	if !sawUsage {
		t.Fatalf("metrics/cost has no non-zero token usage: %+v", cost.ByDay)
	}

	// 10. Teardown, in order: graceful session terminate (blocks until the
	// Supervisor's exit handling, including lease release, completes
	// server-side), then Router, then Data Service. The deferred
	// router.terminate/data.terminate above are the safety net if an
	// earlier assertion already failed the test — sup.Shutdown terminates
	// every tracked child (including this session) when the Router itself
	// is SIGTERMed, so orphan-freedom holds even on a failure exit.
	resp = postJSON(t, routerURL+"/sessions/"+sessionID+"/terminate", nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("terminate session: status %d", resp.StatusCode)
	}
	router.terminate(t, 5*time.Second)
	data.terminate(t, 5*time.Second)

	t.Logf("full-topology smoke completed in %s", time.Since(start))
}
