package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/localauth"
)

// streamingSpyChild is spyChild that, for GET .../output, streams an SSE
// tick every 50ms until the Router hangs up, then appends "closed <path>"
// to $FD_SPY_LOG — so a test can see the Router's side of a proxied stream
// actually close, not just the client's.
const streamingSpyChild = `
import json, os, sys, time, http.server, socketserver
sock = sys.argv[sys.argv.index("-socket") + 1]
log = os.environ["FD_SPY_LOG"]
def note(s):
    with open(log, "a") as f: f.write(s + "\n")
class H(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        note("open " + self.path)
        try:
            while True:
                chunk = b"data: tick\n\n"
                self.wfile.write(b"%x\r\n%s\r\n" % (len(chunk), chunk)); self.wfile.flush()
                time.sleep(0.05)
        except Exception:
            note("closed " + self.path)
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

// TestRound2ProxiedChildStreamDiesWithItsCredential (adversarial round 2,
// H-fixes "streams opened with a credential are closed when that
// credential is revoked"): round 1 pinned the Router's own /events. This
// is the other kind of stream — a child's output, proxied over its Unix
// socket — opened by the install token that then rotates (the browser
// session that also used to expire mid-stream is gone with H7). It must end
// for the client *and* the Router's connection to the child must close (no half-open proxy left
// pumping a dead credential's stream).
func TestRound2ProxiedChildStreamDiesWithItsCredential(t *testing.T) {
	s := newSpyStack(t)
	// Swap the spy for the streaming one (same path the Supervisor runs).
	if err := os.WriteFile(s.home+"/spy.py", []byte(streamingSpyChild), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess := s.spawn(t, ctx)
	srv := httptest.NewServer(s.h)
	t.Cleanup(srv.Close)

	open := func(set func(*http.Request)) (*http.Response, chan struct{}) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/sessions/"+sess.ID+"/output", nil)
		set(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("open output stream: %d", resp.StatusCode)
		}
		ended := make(chan struct{})
		go func() {
			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
			}
			close(ended)
		}()
		return resp, ended
	}
	closedCount := func() int {
		b, _ := os.ReadFile(s.log) //nolint:gosec // test-written
		return strings.Count(string(b), "closed ")
	}
	waitEnd := func(what string, ended chan struct{}, wantClosed int) {
		t.Helper()
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the proxied stream outlived its credential", what)
		}
		deadline := time.Now().Add(3 * time.Second)
		for closedCount() < wantClosed && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if closedCount() < wantClosed {
			t.Fatalf("%s: the client's stream ended but the Router's connection to the child is still open", what)
		}
	}

	// The install token, rotated mid-stream.
	resp2, ended2 := open(withBearer(s.tokens.Token()))
	defer func() { _ = resp2.Body.Close() }()
	time.Sleep(200 * time.Millisecond)
	if _, err := localauth.Rotate(s.home); err != nil {
		t.Fatal(err)
	}
	waitEnd("install token rotated", ended2, 1)
}
