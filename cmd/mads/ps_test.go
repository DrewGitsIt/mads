package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/childsock"
	"github.com/drewkidwell/mads/pkg/localauth"
)

// shortHome is a $MADS_HOME (with a token) short enough for socket paths.
func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "mh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("MADS_HOME", home)
	if _, err := localauth.LoadOrCreate(home); err != nil {
		t.Fatal(err)
	}
	return home
}

// fakeChild serves a child API on id's socket under home.
func fakeChild(t *testing.T, home, id, service string) string {
	t.Helper()
	sock, err := childsock.Path(home, id)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := childsock.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"service":%q,"status":"ok","id":%q,"pid":"4242"}`, service, id)
	})
	mux.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = fmt.Fprintf(w, "%s auth=%q", b, r.Header.Get("Authorization"))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestAPISessionTalksToTheChildSocket(t *testing.T) {
	home := shortHome(t)
	fakeChild(t, home, "ses_a", "session")
	var out bytes.Buffer
	if err := cmdAPI([]string{"--session", "ses_a", "POST", "/echo", "-d", `{"x":1}`}, nil, &out); err != nil {
		t.Fatal(err)
	}
	// No token goes to a child: the socket's same-user check is the door.
	if out.String() != `{"x":1} auth=""` {
		t.Fatalf("child answered %q", out.String())
	}
	if err := cmdAPI([]string{"--session", "ses_nope", "GET", "/health"}, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "mads ps") {
		t.Fatalf("unknown child: %v", err)
	}
}

func TestPsListsTrackedOrphanedAndStaleChildren(t *testing.T) {
	home := shortHome(t)
	live := fakeChild(t, home, "ses_live", "session")
	fakeChild(t, home, "ses_orphan", "session")
	run := fakeChild(t, home, "run_1", "pipeline")
	stale, err := childsock.Path(home, "ses_dead")
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()

	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	old := psNow
	psNow = func() time.Time { return now }
	t.Cleanup(func() { psNow = old })
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/children" || r.Header.Get("Authorization") == "" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprintf(w, `[{"id":"ses_live","kind":"session","pid":11,"socket":%q,"started_at":%q},
			{"id":"run_1","kind":"pipeline","pid":12,"socket":%q,"started_at":%q},
			{"id":"ses_gone","kind":"session","pid":13,"socket":"/tmp/nope-gone.sock","started_at":%q}]`,
			live, now.Add(-90*time.Second).Format(time.RFC3339), run, now.Add(-5*time.Second).Format(time.RFC3339), now.Add(-time.Hour).Format(time.RFC3339))
	}))
	defer stub.Close()

	var out bytes.Buffer
	if err := cmdPs([]string{"-router", stub.URL}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "SOCKET") || !strings.Contains(lines[0], "UPTIME") {
		t.Fatalf("header: %q", lines[0])
	}
	for _, want := range [][]string{
		{"ses_live", "session", "11", live, "running", "1m30s"},
		{"run_1", "pipeline", "12", run, "running", "5s"},
		{"ses_gone", "session", "13", "unresponsive", "1h0m0s"},
		{"ses_orphan", "session", "4242", "orphan"},
		{"ses_dead", "stale"},
	} {
		found := false
		for _, l := range lines {
			f := strings.Fields(l)
			if len(f) > 0 && f[0] == want[0] {
				found = true
				for _, w := range want[1:] {
					if !strings.Contains(l, w) {
						t.Errorf("row %q lacks %q", l, w)
					}
				}
			}
		}
		if !found {
			t.Errorf("no row for %s:\n%s", want[0], got)
		}
	}
}
