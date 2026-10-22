package hermescli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

func errorEvents(evs []provider.Event) []provider.Event {
	return slices.DeleteFunc(slices.Clone(evs), func(ev provider.Event) bool { return ev.Kind != provider.EventError })
}

func TestFailedTurnAuthClassification(t *testing.T) {
	cases := []struct {
		name string
		meta string
		auth bool
	}{
		{"oauth 401", fhFailures["fail-oauth"].meta, true},
		{"api key 401", fhFailures["fail-key"].meta, true},
		{"403 is not auth (Drew's real copilot failure)", fhFailures["fail-forbidden"].meta, false},
		{"server error", fhFailures["fail-server"].meta, false},
		{"401 text off the auth layer", `{"error": "HTTP 401: x", "error_surface": {"layer": "provider", "code": "unknown"}}`, false},
		{"auth layer with no status", `{"error": "token refresh failed", "error_surface": {"layer": "auth", "code": "auth"}}`, false},
		{"401 inside the text", `{"error": "HTTP 400: not HTTP 401: ", "error_surface": {"layer": "auth"}}`, false},
		{"no surface", `{"error": "HTTP 401: x"}`, false},
		{"no metadata", ``, false},
		{"not json", `HTTP 401: x`, false},
	}
	for _, c := range cases {
		got := failedTurnErrorKind(c.meta) == provider.ErrorKindAuth
		if got != c.auth {
			t.Errorf("%s: auth = %v, want %v", c.name, got, c.auth)
		}
	}
}

// Through the stand-in: hermes ends a refused turn like any other (its
// sentence as the reply, end_turn), and the wrapper adds one EventError,
// tagged auth and naming the session's login, ahead of the turn end — for a
// 401 only. The stored transcript's failed-turn rows are classified the same.
func TestSessionAuthFailureCarriesTheLogin(t *testing.T) {
	p := testProvider(t)
	s := startSession(t, p, provider.StartOptions{})
	want := "hermes:" + p.cfg.Home
	for _, prompt := range []string{"fail-oauth", "fail-key"} {
		evs := sendTurn(t, s, prompt)
		errs := errorEvents(evs)
		if len(errs) != 1 {
			t.Fatalf("%s: errors = %+v, want exactly one", prompt, errs)
		}
		e := errs[0]
		if e.ErrorKind != provider.ErrorKindAuth || e.Account == nil || e.Account.Account != want || !strings.HasPrefix(e.Text, "HTTP 401: ") {
			t.Errorf("%s: error = %+v (account %+v), want auth on %s", prompt, e, e.Account, want)
		}
		if evs[len(evs)-2].Kind != provider.EventError {
			t.Errorf("%s: the error should come right before the turn end: %+v", prompt, evs)
		}
	}
	for _, prompt := range []string{"fail-forbidden", "fail-server", "echo hi"} {
		if errs := errorEvents(sendTurn(t, s, prompt)); len(errs) != 0 {
			t.Errorf("%s: errors = %+v, want none", prompt, errs)
		}
	}

	msgs, err := p.KeyedTranscript(context.Background(), s.Ref())
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, m := range msgs {
		switch {
		case m.Role == "agent" && m.IsError:
			kinds = append(kinds, "error:"+m.ErrorKind)
		default:
			kinds = append(kinds, m.Role)
		}
	}
	wantKinds := []string{"user", "error:auth", "user", "error:auth", "user", "error:", "user", "error:", "user", "agent"}
	if !slices.Equal(kinds, wantKinds) {
		t.Fatalf("stored rows = %v, want %v\n%+v", kinds, wantKinds, msgs)
	}
}

// A failed-turn row from before the turn was sent is an earlier turn's.
func TestTurnAuthFailureIgnoresAnEarlierTurn(t *testing.T) {
	p := testProvider(t)
	s := startSession(t, p, provider.StartOptions{})
	sendTurn(t, s, "fail-oauth")
	if _, err := p.turnAuthFailure(context.Background(), s.Ref(), 0, ""); err != nil {
		t.Fatalf("the failure itself: %v", err)
	}
	if _, err := p.turnAuthFailure(context.Background(), s.Ref(), nowFloat()+1, ""); !errors.Is(err, errNoAuthFailure) {
		t.Fatalf("a row older than the send = %v, want errNoAuthFailure", err)
	}
}

func TestOneShotAuthFailureIsAnError(t *testing.T) {
	_, err := testProvider(t).OneShot(context.Background(), provider.OneShotRequest{Prompt: "fail-oauth", Cwd: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("OneShot = %v, want the 401", err)
	}
}

func TestAccountKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	cases := []struct {
		env  []string
		want string
	}{
		{nil, "hermes:" + filepath.Join(home, ".hermes")},
		{[]string{"HERMES_HOME=/srv/hermes-work/"}, "hermes:/srv/hermes-work"},
		{[]string{"HERMES_HOME=/a", "HERMES_HOME=/b"}, "hermes:/b"},
		{[]string{"HERMES_HOME=relative"}, ""},
	}
	for _, c := range cases {
		if got := accountKey(c.env); got != c.want {
			t.Errorf("accountKey(%q) = %q, want %q", c.env, got, c.want)
		}
	}
	if got := New(Config{}).DefaultAccount(); got != "hermes:"+filepath.Join(home, ".hermes") {
		t.Errorf("DefaultAccount = %q", got)
	}
	if got := New(Config{Home: "/srv/h"}).DefaultAccount(); got != "hermes:/srv/h" {
		t.Errorf("DefaultAccount with Config.Home = %q", got)
	}
	t.Setenv("HERMES_HOME", "/srv/env")
	if got := New(Config{}).DefaultAccount(); got != "hermes:/srv/env" {
		t.Errorf("DefaultAccount with HERMES_HOME = %q", got)
	}
}

// storeFailures writes failed-turn rows, oldest first, into home's state.db.
func storeFailures(t *testing.T, home string, prompts ...string) {
	t.Helper()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := openTestStore(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	f := &fakeHermes{db: db, id: "s1"}
	for _, prompt := range prompts {
		f.addFailedTurn(fhFailures[prompt].meta)
	}
}

// standInBins lays out a hermes-acp and the hermes CLI beside it.
func standInBins(t *testing.T, dir string) (acp, cli string) {
	t.Helper()
	acp, cli = filepath.Join(dir, "hermes-acp"), filepath.Join(dir, "hermes")
	for _, p := range []string{acp, cli} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return acp, cli
}

func TestLoginCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HERMES_HOME", "")
	t.Setenv("HERMES_DATA_DIR_SUFFIX", "")
	acp, cli := standInBins(t, t.TempDir())
	p := New(Config{Bin: acp})
	oauth := []string{cli, "auth", "add", "nous", "--type", "oauth"}

	work := filepath.Join(t.TempDir(), "hermes-work")
	if _, ok := p.LoginCommand("hermes:" + work); ok {
		t.Error("a home with no state.db got a login command")
	}
	// The newest auth failure decides; a 403 and a server error after it
	// are not auth failures and don't hide it.
	storeFailures(t, work, "fail-oauth", "fail-forbidden", "fail-server")
	cmd, ok := p.LoginCommand("hermes:" + work)
	if !ok || !slices.Equal(cmd.Argv, oauth) || !slices.Equal(cmd.Env, []string{"HERMES_HOME=" + work}) {
		t.Errorf("other home = %+v, %v; want `hermes auth add nous --type oauth` with HERMES_HOME", cmd, ok)
	}
	// An API key refused after it: no sign-in fixes that.
	storeFailures(t, work, "fail-key")
	if cmd, ok := p.LoginCommand("hermes:" + work); ok {
		t.Errorf("a refused API key got a login command: %+v", cmd)
	}

	def := filepath.Join(home, ".hermes")
	storeFailures(t, def, "fail-oauth")
	cmd, ok = p.LoginCommand("hermes:" + def)
	if !ok || !slices.Equal(cmd.Argv, oauth) || len(cmd.Env) != 0 {
		t.Errorf("default home = %+v, %v; want no env", cmd, ok)
	}

	for _, account := range []string{"codex:" + def, "hermes:", "hermes:relative"} {
		if _, ok := p.LoginCommand(account); ok {
			t.Errorf("%q got a hermes login command", account)
		}
	}
	if _, ok := New(Config{Bin: "hermes-acp"}).LoginCommand("hermes:" + def); ok {
		t.Error("a bare binary name got a login command; argv[0] must be absolute")
	}
	lone := filepath.Join(t.TempDir(), "hermes-acp")
	if err := os.WriteFile(lone, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := New(Config{Bin: lone}).LoginCommand("hermes:" + def); ok {
		t.Error("login command offered with no hermes CLI beside hermes-acp")
	}

	// With no Bin, beside the hermes-acp sessions would spawn: binlocate,
	// confined to $MADS_AGENT_BIN_DIR when set — never a real hermes.
	binDir := t.TempDir()
	t.Setenv("MADS_AGENT_BIN_DIR", binDir)
	if _, ok := New(Config{}).LoginCommand("hermes:" + def); ok {
		t.Error("login command offered with no hermes-acp in MADS_AGENT_BIN_DIR")
	}
	_, standIn := standInBins(t, binDir)
	if cmd, ok := New(Config{}).LoginCommand("hermes:" + def); !ok || cmd.Argv[0] != standIn {
		t.Errorf("login command = %+v, %v; want argv[0] %s", cmd, ok, standIn)
	}
}

// A home whose hermes would bootstrap its dependencies on launch gets no
// command: running it would start the ~600 MB install (pkg/hermesinstall).
func TestLoginCommandRefusesAHomeWithoutAnInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	checkout := t.TempDir()
	for _, d := range []string{".git", ".hermes/bin"} {
		if err := os.MkdirAll(filepath.Join(checkout, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(checkout, "pyproject.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	acp, _ := standInBins(t, filepath.Join(checkout, ".hermes/bin"))
	work := filepath.Join(t.TempDir(), "hermes-work")
	storeFailures(t, work, "fail-oauth")
	if cmd, ok := New(Config{Bin: acp}).LoginCommand("hermes:" + work); ok {
		t.Errorf("login command for a home with no install: %+v", cmd)
	}
}
