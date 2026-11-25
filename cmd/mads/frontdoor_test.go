package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/localauth"
)

// withHome points $MADS_HOME at a fresh dir holding a new install token.
func withHome(t *testing.T) (home, tok string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("MADS_HOME", home)
	s, err := localauth.LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	return home, s.Token()
}

// doorServer is a stand-in service behind a real localauth door: it echoes
// what it saw, and refuses what the real services refuse.
func doorServer(t *testing.T, home string) (*httptest.Server, *[]string) {
	t.Helper()
	store, err := localauth.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Content-Type")+" "+string(body))
		if r.URL.Path == "/boom" {
			http.Error(w, `{"error":"nope"}`, http.StatusConflict)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	door := localauth.NewDoor(localauth.DoorOptions{Name: "stub", Store: store, Classify: func(r *http.Request) string { _, p := mux.Handler(r); return p }})
	srv := httptest.NewServer(door.Wrap(mux))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestAPISendsTheTokenToRouterAndData(t *testing.T) {
	home, _ := withHome(t)
	router, rseen := doorServer(t, home)
	data, dseen := doorServer(t, home)
	var out bytes.Buffer
	if err := cmdAPI([]string{"-router", router.URL, "get", "/sessions?x=1"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != `{"ok":true}` {
		t.Fatalf("stdout %q", out.String())
	}
	if err := cmdAPI([]string{"-router", router.URL, "POST", "sessions/s1/messages", "-d", `{"text":"hi"}`}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := cmdAPI([]string{"--data", "-data-url", data.URL, "PUT", "/config", "-d", "@-"}, strings.NewReader(`{"a":1}`), io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*rseen, "|"); got != "GET /sessions?x=1  |POST /sessions/s1/messages application/json {\"text\":\"hi\"}" {
		t.Fatalf("router saw %q", got)
	}
	if got := strings.Join(*dseen, "|"); got != `PUT /config application/json {"a":1}` {
		t.Fatalf("data saw %q", got)
	}
	// A non-2xx status is an error, with the body still on stdout.
	out.Reset()
	if err := cmdAPI([]string{"-router", router.URL, "GET", "/boom"}, nil, &out); err == nil || !strings.Contains(err.Error(), "status 409") || !strings.Contains(out.String(), "nope") {
		t.Fatalf("GET /boom: err %v, stdout %q", err, out.String())
	}
}

func TestAPIWithoutTheTokenIsRefused(t *testing.T) {
	home, _ := withHome(t)
	router, _ := doorServer(t, home)
	t.Setenv("MADS_HOME", t.TempDir()) // a home with no token
	var out bytes.Buffer
	err := cmdAPI([]string{"-router", router.URL, "GET", "/sessions"}, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(out.String(), "mads api") {
		t.Fatalf("err %v, stdout %q", err, out.String())
	}
}

func TestAPIRefusesAReadableTokenFile(t *testing.T) {
	home, _ := withHome(t)
	router, seen := doorServer(t, home)
	if err := os.Chmod(localauth.Path(home), 0o644); err != nil {
		t.Fatal(err)
	}
	err := cmdAPI([]string{"-router", router.URL, "GET", "/sessions"}, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err %v, want the mode refusal", err)
	}
	if len(*seen) != 0 {
		t.Fatal("the request went out anyway")
	}
}

func TestTokenEnsureAndRotate(t *testing.T) {
	home := t.TempDir() + "/mads"
	t.Setenv("MADS_HOME", home)
	var out bytes.Buffer
	if err := cmdToken([]string{"ensure"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != localauth.Path(home) {
		t.Fatalf("ensure printed %q", out.String())
	}
	first, err := localauth.ReadToken(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdToken([]string{"ensure"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if again, _ := localauth.ReadToken(home); again != first {
		t.Fatal("ensure replaced an existing token")
	}
	out.Reset()
	if err := cmdToken([]string{"rotate"}, &out); err != nil {
		t.Fatal(err)
	}
	if rotated, _ := localauth.ReadToken(home); rotated == first || rotated == "" {
		t.Fatal("rotate didn't write a new token")
	}
	if !strings.Contains(out.String(), "orchestrate tools stop") {
		t.Fatalf("rotate output doesn't say what to do: %s", out.String())
	}
	if err := cmdToken([]string{"show"}, io.Discard); err == nil {
		t.Fatal("an unknown token subcommand was accepted")
	}
}
