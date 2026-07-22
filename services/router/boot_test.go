// This file's real-process boot test is deliberately part of `go test
// ./...`/`make check`, not gated behind a build tag like scripts/e2esmoke:
// it exists specifically to catch main.go's actual wiring silently
// diverging from what digestwatch's hand-wired unit tests exercise (the
// class of bug behind "digestwatch: subscribe: stream: status 422" showing
// up only at real boot). Slower than the rest of the router suite (two `go
// build`s, two real OS process spawns) but still sub-tens-of-seconds.
package main

import (
	"bufio"
	"context"
	"encoding/json"
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

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/testleakcheck"
	"github.com/drewkidwell/mads/services/data/datatest"
)

var (
	bootRouterBin    string
	bootTestchildBin string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mads-router-boot-test")
	if err != nil {
		panic(err)
	}

	bootRouterBin = filepath.Join(dir, "router")
	bootTestchildBin = filepath.Join(dir, "testchild")
	for _, b := range []struct{ out, pkg string }{
		{bootRouterBin, "github.com/drewkidwell/mads/services/router"},
		{bootTestchildBin, "github.com/drewkidwell/mads/services/router/internal/testchild"},
	} {
		build := exec.Command("go", "build", "-o", b.out, b.pkg)
		if out, err := build.CombinedOutput(); err != nil {
			panic(fmt.Sprintf("build %s: %v\n%s", b.pkg, err, out))
		}
	}

	code := m.Run()

	// The router spawns testchild as its session/pipeline child in the
	// tests below — this is the "zero orphan processes" proof.
	if err := testleakcheck.Verify(bootTestchildBin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

var bootListeningRe = regexp.MustCompile(`listening on (\S+)`)

// stderrCapture accumulates a spawned process's stderr lines for both
// readiness discovery and later substring assertions (log-line contract
// tests can't observe any other way against a real subprocess).
type stderrCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *stderrCapture) add(line string) {
	c.mu.Lock()
	c.lines = append(c.lines, line)
	c.mu.Unlock()
}

func (c *stderrCapture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

func (c *stderrCapture) count(substr string) int {
	n := 0
	for _, l := range c.snapshot() {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

// bootProc is a spawned service process discovered via the codebase's
// "listening on <addr>" stderr log-line convention (see
// services/router/internal/child/child.go — this test can't import that
// internal package, so it re-implements the same small pattern, mirroring
// scripts/e2esmoke/main_test.go's spawnListening).
type bootProc struct {
	cmd    *exec.Cmd
	addr   string
	exited chan struct{}
	stderr *stderrCapture
}

func spawnListening(t *testing.T, name string, argv []string, env []string) *bootProc {
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

	p := &bootProc{cmd: cmd, exited: make(chan struct{}), stderr: &stderrCapture{}}
	addrCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			p.stderr.add(line)
			if m := bootListeningRe.FindStringSubmatch(line); m != nil {
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
		t.Fatalf("%s: exited before reporting a listen address:\n%s", name, strings.Join(p.stderr.snapshot(), "\n"))
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-p.exited
		t.Fatalf("%s: did not report a listen address in time:\n%s", name, strings.Join(p.stderr.snapshot(), "\n"))
	}
	return nil // unreachable, satisfies the compiler
}

// terminate sends SIGTERM, waits up to grace, then SIGKILL.
func (p *bootProc) terminate(t *testing.T, grace time.Duration) {
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

// bootCleanEnv mirrors scripts/e2esmoke/main_test.go's cleanEnv: an isolated
// home dir, no leaked MADS_HOME/MADS_DATA_DIR from the outer test process.
func bootCleanEnv(home string) []string {
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "MADS_DATA_DIR=") || strings.HasPrefix(e, "MADS_HOME=") {
			continue
		}
		env = append(env, e)
	}
	return append(env, "HOME="+home, "MADS_HOME="+home)
}

func bootWaitHealth(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health") //nolint:gosec,noctx // test helper, base is a local process this test spawned
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

func bootEventually(t *testing.T, budget time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !fn() {
		t.Fatalf("condition not met within %s", budget)
	}
}

type bootCreateSessionResponse struct {
	Session contracts.Session `json:"session"`
	Addr    string            `json:"addr"`
}

func bootPostJSON(t *testing.T, url string, body, out any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", strings.NewReader(string(raw))) //nolint:gosec,noctx // test helper, url is a local process this test spawned
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

// TestDigestwatchSubscribesAtBootAndDeliversDigest is the regression this
// class of bug needs: boot the REAL router binary (not a hand-wired
// newMux/digestwatch.NewWatcher construction) against a real Data Service,
// and assert digestwatch reaches subscribed for both watchers with no
// "status 422" anywhere in its log — then drive a live turn-ended digest
// end to end through the real boot path (real router → real supervisor →
// spawned session children), proving the whole seam actually works when
// wired the way main.go wires it, not just the way the unit tests wire it.
func TestDigestwatchSubscribesAtBootAndDeliversDigest(t *testing.T) {
	tmp := t.TempDir()
	homeDir := filepath.Join(tmp, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := bootCleanEnv(homeDir)

	dataURL := datatest.Start(t)
	data := dataclient.New(dataURL, "boottest")

	cfg, err := data.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.FeatureFlags.LinkedSessions = true
	if err := data.UpdateConfig(context.Background(), &cfg); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	// A Fast Model backend must actually be configured for this test:
	// GenerateDigest now skips delivery entirely (fastmodel.
	// ErrBackendUnconfigured) when nothing selects an endpoint, so without
	// this fixture the /injections assertion below would never see a
	// delivery.
	fastModel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"model":"boot-fast","choices":[{"message":{"content":"did stuff"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer fastModel.Close()

	router := spawnListening(t, "router", []string{
		bootRouterBin, "-addr", "127.0.0.1:0",
		"-data-url", dataURL,
		"-session-cmd", bootTestchildBin,
		"-pipeline-cmd", bootTestchildBin,
		"-mads-home", homeDir,
		"-fastmodel-base-url", fastModel.URL,
	}, env)
	defer router.terminate(t, 5*time.Second)
	routerURL := "http://" + router.addr
	bootWaitHealth(t, routerURL)

	// Both watchers (turn-ended digests, run digests) must reach subscribed
	// — this is the "no 422 at boot" acceptance criterion.
	bootEventually(t, 5*time.Second, func() bool {
		return router.stderr.count("digestwatch: subscribed to ") >= 2
	})
	if n := router.stderr.count("status 422"); n != 0 {
		t.Fatalf("router logged %d occurrence(s) of \"status 422\":\n%s", n, strings.Join(router.stderr.snapshot(), "\n"))
	}

	// Watched and monitor sessions must be created through the real
	// router's own API (not directly against the Data Service): job.Deps's
	// Resolver is the real Supervisor in production, and it can only
	// resolve a session's live address for one it actually spawned.
	var watched, monitor bootCreateSessionResponse
	resp := bootPostJSON(t, routerURL+"/sessions", map[string]string{"name": "watched", "provider": "fake"}, &watched)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create watched session: status %d", resp.StatusCode)
	}
	resp = bootPostJSON(t, routerURL+"/sessions", map[string]string{"name": "monitor", "provider": "fake"}, &monitor)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create monitor session: status %d", resp.StatusCode)
	}
	if watched.Session.ID == "" || watched.Addr == "" || monitor.Session.ID == "" || monitor.Addr == "" {
		t.Fatalf("incomplete create-session responses: watched=%+v monitor=%+v", watched, monitor)
	}

	if _, err := data.CreateLink(context.Background(), contracts.LinkRequest{
		GrantingSessionID: monitor.Session.ID,
		SubjectSessionID:  watched.Session.ID,
		Grants:            []contracts.Grant{contracts.GrantRead},
	}); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	// testchild has no real session runner, so this appends the
	// session.turn_ended envelope directly — mirroring
	// digestwatch/watcher_test.go's TestWatcherSpawnsDigestJobForTurnEnded,
	// just driven through a real boot path instead of a hand-wired one.
	if err := data.Append(context.Background(), &contracts.Envelope{
		Type:      contracts.TurnEnded,
		SessionID: watched.Session.ID,
		Payload:   json.RawMessage(`{"delta":"user: hi\nassistant: did stuff\n"}`),
	}); err != nil {
		t.Fatalf("Append turn_ended: %v", err)
	}

	bootEventually(t, 10*time.Second, func() bool {
		resp, err := http.Get("http://" + monitor.Addr + "/injections") //nolint:gosec,noctx // test helper, monitor.Addr is a local process this test spawned
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		var got []contracts.InjectRequest
		if json.NewDecoder(resp.Body).Decode(&got) != nil {
			return false
		}
		return len(got) == 1
	})

	if n := router.stderr.count("status 422"); n != 0 {
		t.Fatalf("router logged %d occurrence(s) of \"status 422\" during the run:\n%s", n, strings.Join(router.stderr.snapshot(), "\n"))
	}
}

// TestDefaultAddrIsProductionPort locks in that a bare "./bin/router" launch
// (the shepherd's post-drain restart, scripts/app.sh, any human running the
// binary directly) binds :8080, the port dataclient's default and every
// other service in the stack expects — not the ephemeral "localhost:0" the
// runtime-isolated-gates change briefly made the default. Every gate/e2e
// path (this file included) passes -addr explicitly, so ephemeral-by-
// default belongs there, not in the flag's own default. Asserted via -h,
// which flag.Parse prints and exits(0) on before main ever reaches
// net.Listen — so this can't itself bind the real port and collide with a
// concurrently running gate.
func TestDefaultAddrIsProductionPort(t *testing.T) {
	out, err := exec.Command(bootRouterBin, "-h").CombinedOutput()
	if err != nil {
		t.Fatalf("-h: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `listen address (default "localhost:8080")`) {
		t.Fatalf("router's default -addr changed, want localhost:8080:\n%s", out)
	}
}
