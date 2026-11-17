package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/pkg/localguard"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// spyChild is a stand-in session child (python3, no product code): it
// listens on the -socket the Supervisor passes, logs "listening on", and
// for every request appends {headers, env} as one JSON line to $FD_SPY_LOG.
const spyChild = `
import json, os, socket, sys, http.server, socketserver
sock = sys.argv[sys.argv.index("-socket") + 1]
log = os.environ["FD_SPY_LOG"]
class H(http.server.BaseHTTPRequestHandler):
    def handle_one(self):
        pass
    def do_any(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n: self.rfile.read(n)
        with open(log, "a") as f:
            f.write(json.dumps({"path": self.path, "headers": dict(self.headers.items()),
                                "env": {k: v for k, v in os.environ.items() if k.startswith("MADS_")}}) + "\n")
        body = b"data: hi\n\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    do_GET = do_POST = do_any
    def log_message(self, *a): pass
    def address_string(self): return "unix"
class S(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True
if os.path.exists(sock): os.unlink(sock)
srv = S(sock, H)
os.chmod(sock, 0o600)
sys.stderr.write("spy: listening on %s\n" % sock); sys.stderr.flush()
srv.serve_forever()
`

type spyStack struct {
	*routerStack
	sup *supervisor.Supervisor
	log string
}

// newSpyStack is newRouterStack with a Supervisor wired exactly as main.go
// wires it (install token in, scoped tokens minted, sockets under
// $MADS_HOME/run), spawning spyChild as the session binary.
func newSpyStack(t *testing.T) *spyStack {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available for the spy child")
	}
	home, err := os.MkdirTemp("", "fdadv") // short: the socket path must fit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	script := filepath.Join(home, "spy.py")
	if err := os.WriteFile(script, []byte(spyChild), 0o600); err != nil {
		t.Fatal(err)
	}
	spyLog := filepath.Join(home, "spy.log")
	t.Setenv("FD_SPY_LOG", spyLog)

	tokens, err := localauth.LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	auth := newTestFrontDoor(tokens)
	data := dataclient.New(datatest.Start(t), "router")
	sup := supervisor.New(data, []string{"python3", script}, nil, nil)
	sup.SetTokenFile(localauth.Path(home))
	sup.SetMadsHome(home)
	sup.SetScopedTokens(auth.scoped)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	reg := registryclient.New("")
	jobs := job.New(data, fastmodel.New(fake.New(), fastmodel.Config{}), reg)
	providers := provider.NewRegistry()
	if err := providers.Register(fake.New()); err != nil {
		t.Fatal(err)
	}
	creds, err := credstore.Open(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := newMux(data, sup, jobs, providers, creds, nil, nil, nil, nil, reg, t.TempDir(), nil, "", nil, "",
		identity{root: "/src/checkout"}, localguard.New("router", "17373", nil), nil, auth)
	return &spyStack{routerStack: &routerStack{h: h, home: home, auth: auth, data: data, tokens: tokens}, sup: sup, log: spyLog}
}

type spyHit struct {
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Env     map[string]string `json:"env"`
}

func (s *spyStack) hits(t *testing.T) []spyHit {
	t.Helper()
	f, err := os.Open(s.log)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []spyHit
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		var h spyHit
		if err := json.Unmarshal(sc.Bytes(), &h); err == nil {
			out = append(out, h)
		}
	}
	return out
}

func (s *spyStack) spawn(t *testing.T, ctx context.Context) contracts.Session {
	t.Helper()
	sess := contracts.Session{Name: "spy", Provider: "fake"}
	if err := s.data.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sup.SpawnSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	return sess
}

// scopedTokenOf asks the spy child for its env through the Router (the
// spawn's MADS_ROUTER_TOKEN), as any request it serves records it.
func (s *spyStack) scopedTokenOf(t *testing.T, id string) string {
	t.Helper()
	before := len(s.hits(t))
	if w := s.do(t, "GET", "/sessions/"+id+"/output", withBearer(s.tokens.Token())); w.Code != http.StatusOK {
		t.Fatalf("GET output through the Router: %d %s", w.Code, w.Body)
	}
	h := s.hits(t)
	if len(h) <= before {
		t.Fatal("the spy child saw no request")
	}
	return h[len(h)-1].Env[localauth.EnvRouterToken]
}

// TestProxyStripsCredentialsBeforeChildren (adversarial, front door D3 /
// D9; fixed in round 1): the door stripped Authorization before a request
// went on, but not the cookie, so every browser request the Router proxied
// to a session child (output streams, messages, queue, transport, ...)
// handed the child the page's credential in a Cookie header. Now neither
// the cookie nor a bearer reaches a child.
func TestProxyStripsCredentialsBeforeChildren(t *testing.T) {
	s := newSpyStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess := s.spawn(t, ctx)
	tok := s.tokens.Token()
	if w := s.do(t, "GET", "/sessions/"+sess.ID+"/output", func(r *http.Request) {
		withBearer(tok)(r)
		withCookie(tok)(r)
		r.AddCookie(&http.Cookie{Name: "other", Value: "x"})
	}); w.Code != http.StatusOK {
		t.Fatalf("GET output with the cookie: %d %s", w.Code, w.Body)
	}
	hits := s.hits(t)
	if len(hits) == 0 {
		t.Fatal("the spy child saw no request")
	}
	last := hits[len(hits)-1]
	for k, v := range last.Headers {
		if strings.EqualFold(k, "Authorization") {
			t.Errorf("Authorization reached the child: %s", v)
		}
		if strings.EqualFold(k, "Cookie") {
			t.Errorf("a Cookie header reached the child: %s: %.20s…", k, v)
		}
	}
}

// TestAdversarialChildSocketsAndScopedTokenLifecycle: the child's socket
// and dir are private; no token is in its argv (the install token's path
// is, H4; the scoped token arrives by env); the scoped token works on its own session only while
// that spawn lives, dies with a bounce (the respawn gets a new one), with
// a delete, and with a rotation.
func TestAdversarialChildSocketsAndScopedTokenLifecycle(t *testing.T) {
	s := newSpyStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess := s.spawn(t, ctx)

	addr, ok := s.sup.SessionAddr(sess.ID)
	if !ok {
		t.Fatal("no addr")
	}
	if fi, err := os.Lstat(addr); err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket %s: %v %v", addr, fi.Mode(), err)
	}
	if fi, err := os.Stat(filepath.Dir(addr)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("socket dir: %v %v", fi.Mode(), err)
	}
	pid, _ := s.sup.SessionPID(sess.ID)
	if out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output(); err == nil {
		if strings.Contains(string(out), s.tokens.Token()) || strings.Contains(string(out), "mads_s_") {
			t.Errorf("a token is in the child's argv: %s", out)
		}
	}

	tok1 := s.scopedTokenOf(t, sess.ID)
	if !strings.HasPrefix(tok1, "mads_s_") {
		t.Fatalf("child env has no scoped token: %q", tok1)
	}
	summary := "/sessions/" + sess.ID + "/orchestrate/summary"
	if w := s.do(t, "GET", summary, withBearer(tok1)); w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Fatalf("own scoped token on own route: %d", w.Code)
	}
	if w := s.do(t, "GET", "/sessions/"+sess.ID+"/output", withBearer(tok1)); w.Code != http.StatusForbidden {
		t.Errorf("scoped token on the output stream: %d, want 403", w.Code)
	}

	// Bounce: the old spawn's token dies, the new spawn's works.
	if _, err := s.sup.BounceSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if w := s.do(t, "GET", summary, withBearer(tok1)); w.Code != http.StatusUnauthorized {
		t.Errorf("pre-bounce scoped token after a bounce: %d, want 401", w.Code)
	}
	tok2 := s.scopedTokenOf(t, sess.ID)
	if tok2 == "" || tok2 == tok1 {
		t.Fatalf("respawn token %q (old %q)", tok2, tok1)
	}
	if w := s.do(t, "GET", summary, withBearer(tok2)); w.Code == http.StatusUnauthorized {
		t.Errorf("respawn's scoped token refused: %d", w.Code)
	}

	// Rotation: every scoped token dies.
	if _, err := localauth.Rotate(s.home); err != nil {
		t.Fatal(err)
	}
	if w := s.do(t, "GET", summary, withBearer(tok2)); w.Code != http.StatusUnauthorized {
		t.Errorf("scoped token after rotation: %d, want 401", w.Code)
	}

	// Delete via the API: no token for the session survives.
	sess2 := s.spawn(t, ctx)
	tok3 := s.scopedTokenOf(t, sess2.ID)
	if w := s.do(t, "DELETE", "/sessions/"+sess2.ID, withBearer(s.tokens.Token())); w.Code/100 != 2 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := s.do(t, "GET", "/sessions/"+sess2.ID+"/orchestrate/summary", withBearer(tok3)); w.Code != http.StatusUnauthorized {
		t.Errorf("scoped token after its session was deleted: %d, want 401", w.Code)
	}
	// The rotation killed the bounced session's token and the delete killed
	// sess2's: nothing is left.
	if n := s.auth.scoped.Len(); n != 0 {
		t.Errorf("%d scoped tokens still live, want 0", n)
	}
}

// TestRealChildEnvironHoldsOnlyTheScopedToken (adversarial, front door D4;
// H4): os.Unsetenv only edits Go's copy, and the environment block the
// kernel exposes — `ps eww <pid>` (KERN_PROCARGS2) on macOS,
// /proc/<pid>/environ on Linux — keeps whatever a process was started
// with. So the install token is never handed over that way: the child reads
// $MADS_HOME/local.token itself and its environ never holds it. The
// session-scoped token stays in env by decision (the orchestrate MCP server
// needs it for the provider's config) — the accepted risk H4 records:
// visible to the same user, scoped to one session's orchestrate routes.
func TestRealChildEnvironHoldsOnlyTheScopedToken(t *testing.T) {
	binDir := t.TempDir()
	sessionBin := filepath.Join(binDir, "session")
	if b, err := exec.Command("go", "build", "-o", sessionBin, "../session").CombinedOutput(); err != nil {
		t.Fatalf("build session: %v\n%s", err, b)
	}
	home, err := os.MkdirTemp("", "fdenv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	tokens, err := localauth.LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	scoped := localauth.NewRegistry()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sess := contracts.Session{Name: "env", Provider: "fake", Isolation: contracts.IsolationCurrentDir}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	sup := supervisor.New(data, []string{sessionBin, "-data-url", base, "-mads-home", home, "-cwd", t.TempDir()}, nil, nil)
	sup.SetTokenFile(localauth.Path(home))
	sup.SetMadsHome(home)
	sup.SetScopedTokens(scoped)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	if _, err := sup.SpawnSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	pid, _ := sup.SessionPID(sess.ID)
	var environ string
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ"); err == nil { //nolint:gosec // test reads its own child
		environ = strings.ReplaceAll(string(b), "\x00", " ")
	} else if out, err := exec.Command("ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output(); err == nil {
		environ = string(out)
	} else {
		t.Skip("no way to read a process's environ here")
	}
	if strings.Contains(environ, tokens.Token()) || strings.Contains(environ, localauth.EnvLocalToken+"=") {
		t.Errorf("the running session child's environ block holds the install token")
	}
	if !strings.Contains(environ, localauth.EnvRouterToken+"=mads_s_") {
		t.Errorf("the scoped token isn't in the child's env, where the orchestrate MCP server's config needs it (H4)")
	}
}
