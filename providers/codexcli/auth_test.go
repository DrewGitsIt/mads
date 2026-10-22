package codexcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

func errorEvents(evs []provider.Event) []provider.Event {
	return slices.DeleteFunc(slices.Clone(evs), func(ev provider.Event) bool { return ev.Kind != provider.EventError })
}

func TestTurnErrorAuthClassification(t *testing.T) {
	cases := []struct {
		name string
		err  turnError
		auth bool
	}{
		{"expired login", turnError{Message: "Your access token could not be refreshed. Please log out and sign in again.", CodexErrorInfo: json.RawMessage(`"unauthorized"`)}, true},
		{"401 on a stream", turnError{Message: "Reconnecting... 2/5", CodexErrorInfo: json.RawMessage(`{"responseStreamDisconnected":{"httpStatusCode":401}}`)}, true},
		{"401 on connect", turnError{Message: "x", CodexErrorInfo: json.RawMessage(`{"httpConnectionFailed":{"httpStatusCode":401}}`)}, true},
		{"no login: other + codex's 401 text", turnError{Message: "unexpected status 401 Unauthorized: Missing bearer or basic authentication in header", CodexErrorInfo: json.RawMessage(`"other"`)}, true},
		{"403 is not auth", turnError{Message: "unexpected status 403 Forbidden", CodexErrorInfo: json.RawMessage(`{"httpConnectionFailed":{"httpStatusCode":403}}`)}, false},
		{"403 text is not auth", turnError{Message: "unexpected status 403 Forbidden", CodexErrorInfo: json.RawMessage(`"other"`)}, false},
		{"no status", turnError{Message: "stream closed", CodexErrorInfo: json.RawMessage(`{"responseStreamDisconnected":{"httpStatusCode":null}}`)}, false},
		{"server error", turnError{Message: "boom", CodexErrorInfo: json.RawMessage(`"internalServerError"`)}, false},
		{"401 text under another kind", turnError{Message: "unexpected status 401 Unauthorized", CodexErrorInfo: json.RawMessage(`"badRequest"`)}, false},
		{"rate limit", turnError{Message: "slow down", CodexErrorInfo: json.RawMessage(`"rateLimitExceeded"`)}, false},
		{"no info", turnError{Message: "turn failed"}, false},
	}
	for _, c := range cases {
		if got := c.err.authFailed(); got != c.auth {
			t.Errorf("%s: authFailed = %v, want %v", c.name, got, c.auth)
		}
	}
	limit := turnError{Message: "slow down", CodexErrorInfo: json.RawMessage(`"rateLimitExceeded"`)}
	if !limit.rateLimited() || limit.errorKind() != "" {
		t.Error("a rate limit must stay a rate limit, unclassified for auth")
	}
	var none *turnError
	if none.authFailed() || none.errorKind() != "" {
		t.Error("a nil turn error classified")
	}
}

// The two real auth failures — an expired login and none at all — each end
// their turn with one error, tagged auth; the retries before the signed-out
// failure (willRetry) are not errors of their own.
func TestReplayAuthFailuresTagTheTurnError(t *testing.T) {
	for _, capture := range []string{"auth-expired.jsonl", "auth-signed-out.jsonl"} {
		t.Run(capture, func(t *testing.T) {
			evs, _ := replay(t, capture, provider.TrustFull, nil)
			ts := turns(evs)
			if len(ts) != 1 {
				t.Fatalf("turns = %d, want 1: %+v", len(ts), evs)
			}
			errs := errorEvents(ts[0])
			if len(errs) != 1 || errs[0].ErrorKind != provider.ErrorKindAuth {
				t.Fatalf("turn errors = %+v, want one auth error", errs)
			}
		})
	}
}

// Through the scripted stand-in: an unauthorized turn/completed yields one
// EventError, tagged auth and naming the session's login; a 403 and a usage
// limit stay unclassified.
func TestSessionAuthFailureCarriesTheLogin(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "codex-work")
	p := fakeProvider(t, "CODEX_HOME="+codexHome)
	s := startSession(t, p, provider.StartOptions{})
	want := "codex:" + codexHome
	if s.account != want {
		t.Fatalf("session account = %q, want %q", s.account, want)
	}
	for _, prompt := range []string{"fail-expired", "fail-signed-out"} {
		errs := errorEvents(sendTurn(t, s, prompt))
		if len(errs) != 1 {
			t.Fatalf("%s: errors = %+v, want exactly one", prompt, errs)
		}
		e := errs[0]
		if e.ErrorKind != provider.ErrorKindAuth || e.Account == nil || e.Account.Account != want {
			t.Errorf("%s: error = %+v (account %+v), want auth on %s", prompt, e, e.Account, want)
		}
	}
	for _, prompt := range []string{"fail-forbidden", "fail-limit"} {
		errs := errorEvents(sendTurn(t, s, prompt))
		if len(errs) != 1 || errs[0].ErrorKind != "" || errs[0].Account != nil {
			t.Errorf("%s: errors = %+v, want one unclassified error with no account", prompt, errs)
		}
	}

	// The stored transcript: each failed turn reads as its user message and
	// an IsError agent row after it, classified as live.
	msgs, err := p.KeyedTranscript(context.Background(), s.Ref())
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, m := range msgs {
		switch {
		case m.Role == "user":
			kinds = append(kinds, "user")
		case m.Role == "agent" && m.IsError:
			kinds = append(kinds, "error:"+m.ErrorKind)
		default:
			kinds = append(kinds, m.Role)
		}
	}
	wantKinds := []string{"user", "error:auth", "user", "error:auth", "user", "error:", "user", "error:"}
	if !slices.Equal(kinds, wantKinds) {
		t.Fatalf("stored rows = %v, want %v\n%+v", kinds, wantKinds, msgs)
	}
	if msgs[1].Text != "Your access token could not be refreshed. Please log out and sign in again." || msgs[1].Key == "" {
		t.Errorf("expired-login row = %+v", msgs[1])
	}
}

// The real stored form: a fresh app-server's thread/items/list and
// thread/turns/list for each captured failure read as the user message and
// one auth error row.
func TestStoredAuthFailures(t *testing.T) {
	for _, name := range []string{"auth-expired", "auth-signed-out"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/captures/" + name + "-turns-list.json")
			if err != nil {
				t.Fatal(err)
			}
			var res struct {
				Data []struct {
					ID     string     `json:"id"`
					Status string     `json:"status"`
					Error  *turnError `json:"error"`
				} `json:"data"`
			}
			if err := json.Unmarshal(raw, &res); err != nil {
				t.Fatal(err)
			}
			failed := map[string]*turnError{}
			for _, tr := range res.Data {
				if tr.Status == "failed" {
					failed[tr.ID] = tr.Error
				}
			}
			msgs := normalizeItems(loadItems(t, name+"-items-list.json"), failed, true)
			if len(msgs) != 2 || msgs[0].Role != "user" {
				t.Fatalf("rows = %+v, want the user message and its error", msgs)
			}
			if e := msgs[1]; e.Role != "agent" || !e.IsError || e.ErrorKind != provider.ErrorKindAuth {
				t.Errorf("error row = %+v, want an IsError agent row, ErrorKind auth", e)
			}
		})
	}
}

// A failed turn's error row lands after the turn's last item, not after
// its first; turns that didn't fail get none.
func TestFailedTurnRowFollowsTheTurn(t *testing.T) {
	entries := []itemEntry{
		{TurnID: "t1", Item: threadItem{Type: "userMessage", ID: "u1", Content: []userInput{{Type: "text", Text: "one"}}}},
		{TurnID: "t1", Item: threadItem{Type: "agentMessage", ID: "a1", Text: "ok"}},
		{TurnID: "t2", Item: threadItem{Type: "userMessage", ID: "u2", Content: []userInput{{Type: "text", Text: "two"}}}},
		{TurnID: "t2", Item: threadItem{Type: "agentMessage", ID: "a2", Text: "partial"}},
		{TurnID: "t3", Item: threadItem{Type: "userMessage", ID: "u3", Content: []userInput{{Type: "text", Text: "three"}}}},
	}
	failed := map[string]*turnError{"t2": {Message: "boom", CodexErrorInfo: json.RawMessage(`"internalServerError"`)}}
	msgs := normalizeItems(entries, failed, false)
	var keys []string
	for _, m := range msgs {
		keys = append(keys, m.Key)
	}
	want := []string{"user:u1", "a1", "user:u2", "a2", "turn-error:t2", "user:u3"}
	if !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if e := msgs[4]; !e.IsError || e.ErrorKind != "" || e.Text != "boom" {
		t.Errorf("error row = %+v", e)
	}
}

func TestAccountKey(t *testing.T) {
	cases := []struct {
		env  []string
		want string
	}{
		{[]string{"HOME=/Users/me"}, "codex:/Users/me/.codex"},
		{[]string{"HOME=/Users/me", "CODEX_HOME=/Users/me/.codex-work/"}, "codex:/Users/me/.codex-work"},
		{[]string{"CODEX_HOME=/a", "CODEX_HOME=/b", "HOME=/Users/me"}, "codex:/b"},
	}
	for _, c := range cases {
		if got := accountKey(c.env); got != c.want {
			t.Errorf("accountKey(%q) = %q, want %q", c.env, got, c.want)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	if got := New(Config{}).DefaultAccount(); got != "codex:"+filepath.Join(home, ".codex") {
		t.Errorf("DefaultAccount = %q", got)
	}
	t.Setenv("CODEX_HOME", "/srv/codex")
	if got := New(Config{}).DefaultAccount(); got != "codex:/srv/codex" {
		t.Errorf("DefaultAccount with CODEX_HOME = %q", got)
	}
}

func TestLoginCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	p := New(Config{Bin: "/opt/codex/bin/codex"})

	cmd, ok := p.LoginCommand("codex:" + filepath.Join(home, ".codex"))
	if !ok || !slices.Equal(cmd.Argv, []string{"/opt/codex/bin/codex", "login"}) || len(cmd.Env) != 0 {
		t.Errorf("default login = %+v, %v; want `codex login` with no env", cmd, ok)
	}
	cmd, ok = p.LoginCommand("codex:/Users/me/.codex-work")
	if !ok || !slices.Equal(cmd.Env, []string{"CODEX_HOME=/Users/me/.codex-work"}) {
		t.Errorf("other login = %+v, %v; want CODEX_HOME set", cmd, ok)
	}
	if _, ok := p.LoginCommand("claude-code:/Users/me/.claude"); ok {
		t.Error("another provider's account got a codex login command")
	}
	if _, ok := p.LoginCommand("codex:"); ok {
		t.Error("an account with no home got a login command")
	}
	if _, ok := New(Config{Bin: "codex"}).LoginCommand("codex:/x"); ok {
		t.Error("a bare binary name got a login command; argv[0] must be absolute")
	}

	// With no Bin, the binary sessions would spawn: binlocate, confined to
	// $MADS_AGENT_BIN_DIR when set — never a real codex elsewhere.
	binDir := t.TempDir()
	t.Setenv("MADS_AGENT_BIN_DIR", binDir)
	if _, ok := New(Config{}).LoginCommand("codex:/x"); ok {
		t.Error("login command offered with no codex in MADS_AGENT_BIN_DIR")
	}
	standIn := filepath.Join(binDir, "codex")
	if err := os.WriteFile(standIn, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if cmd, ok := New(Config{}).LoginCommand("codex:/x"); !ok || cmd.Argv[0] != standIn {
		t.Errorf("login command = %+v, %v; want argv[0] %s", cmd, ok, standIn)
	}
}
