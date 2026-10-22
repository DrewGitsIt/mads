package claudecode

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// replayAuth feeds a capture's lines to a streamSession on account and
// returns what it emitted.
func replayAuth(t *testing.T, account string, lines []string) []provider.Event {
	t.Helper()
	s := &streamSession{account: account, events: make(chan provider.Event, 64), announced: make(chan string, 1)}
	for _, ln := range lines {
		if strings.TrimSpace(ln) != "" {
			s.handleLine([]byte(ln))
		}
	}
	var evs []provider.Event
	for {
		select {
		case ev := <-s.events:
			evs = append(evs, ev)
		default:
			return evs
		}
	}
}

func errorEvents(evs []provider.Event) []provider.Event {
	return slices.DeleteFunc(slices.Clone(evs), func(ev provider.Event) bool { return ev.Kind != provider.EventError })
}

// The real signed-out capture: an assistant line saying
// authentication_failed, then a result with is_error. The turn yields one
// error, tagged auth and carrying the login, and still ends.
func TestStreamAuthFailureTagsTheTurnError(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "stream-auth-failed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	evs := replayAuth(t, "claude-code:/Users/me/.claude", strings.Split(string(raw), "\n"))
	errs := errorEvents(evs)
	if len(errs) != 1 {
		t.Fatalf("errors = %+v, want exactly one", errs)
	}
	e := errs[0]
	if e.ErrorKind != provider.ErrorKindAuth || e.Text != "Not logged in · Please run /login" {
		t.Errorf("error = %+v, want an auth error with the CLI's text", e)
	}
	if e.Account == nil || e.Account.Account != "claude-code:/Users/me/.claude" {
		t.Errorf("error account = %+v, want the session's login", e.Account)
	}
	if !slices.ContainsFunc(evs, func(ev provider.Event) bool { return ev.Kind == provider.EventTurnEnd }) {
		t.Error("no turn end after the auth failure")
	}
}

// Any other failed turn stays unclassified, and an auth failure doesn't
// leak into the next turn's error.
func TestStreamOtherErrorsStayUnclassified(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "stream-auth-failed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	other := []string{
		`{"type":"assistant","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"API Error: Connection refused"}]},"parent_tool_use_id":null,"session_id":"s","error":"unknown","is_api_error_message":true}`,
		`{"type":"result","subtype":"success","is_error":true,"result":"API Error: Connection refused","session_id":"s"}`,
	}
	lines := append(strings.Split(strings.TrimSpace(string(raw)), "\n"), other...)
	errs := errorEvents(replayAuth(t, "claude-code:/x", lines))
	if len(errs) != 2 {
		t.Fatalf("errors = %+v, want two", errs)
	}
	if errs[0].ErrorKind != provider.ErrorKindAuth {
		t.Errorf("first error = %+v, want auth", errs[0])
	}
	if errs[1].ErrorKind != "" || errs[1].Account != nil {
		t.Errorf("second error = %+v, want unclassified with no account", errs[1])
	}
}

// A refused login written to the transcript is an agent entry marked
// IsError and ErrorKind auth; another API failure is IsError alone.
func TestTranscriptClassifiesAuthFailures(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"hello"},"timestamp":"2026-10-22T19:25:51Z"}`,
		`{"type":"assistant","isApiErrorMessage":true,"error":"authentication_failed","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"Failed to authenticate: OAuth session expired and could not be refreshed"}]},"timestamp":"2026-10-22T19:25:52Z"}`,
		`{"type":"user","message":{"role":"user","content":"again"},"timestamp":"2026-10-22T19:26:00Z"}`,
		`{"type":"assistant","isApiErrorMessage":true,"error":"unknown","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"API Error: Connection refused"}]},"timestamp":"2026-10-22T19:26:01Z"}`,
	}, "\n")
	msgs, err := normalizeTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4: %+v", len(msgs), msgs)
	}
	if m := msgs[1]; !m.IsError || m.ErrorKind != provider.ErrorKindAuth {
		t.Errorf("expired login = %+v, want IsError with ErrorKind auth", m)
	}
	if m := msgs[3]; !m.IsError || m.ErrorKind != "" {
		t.Errorf("connection error = %+v, want IsError, unclassified", m)
	}
}

func TestLoginCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	p := New(Config{Bin: "/opt/claude/bin/claude"})

	cmd, ok := p.LoginCommand("claude-code:" + filepath.Join(home, ".claude"))
	if !ok || !slices.Equal(cmd.Argv, []string{"/opt/claude/bin/claude", "auth", "login"}) || len(cmd.Env) != 0 {
		t.Errorf("default login = %+v, %v; want `claude auth login` with no env (CLAUDE_CONFIG_DIR picks another keychain entry)", cmd, ok)
	}
	cmd, ok = p.LoginCommand("claude-code:/Users/me/.claude-work")
	if !ok || !slices.Equal(cmd.Env, []string{"CLAUDE_CONFIG_DIR=/Users/me/.claude-work"}) {
		t.Errorf("other login = %+v, %v; want CLAUDE_CONFIG_DIR set", cmd, ok)
	}
	if _, ok := p.LoginCommand("codex:/Users/me/.codex"); ok {
		t.Error("another provider's account got a claude login command")
	}
	if _, ok := New(Config{Bin: "claude"}).LoginCommand("claude-code:/x"); ok {
		t.Error("a bare binary name got a login command; argv[0] must be absolute")
	}
}
