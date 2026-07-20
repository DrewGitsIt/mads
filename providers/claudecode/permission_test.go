package claudecode

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

func TestNeedsPermissionHook(t *testing.T) {
	cases := []struct {
		trust provider.Trust
		want  bool
	}{
		{provider.TrustFull, false},
		{"", false},
		{provider.TrustLimited, true},
		{provider.TrustReadOnly, true},
	}
	for _, c := range cases {
		if got := needsPermissionHook(c.trust); got != c.want {
			t.Errorf("needsPermissionHook(%q) = %v, want %v", c.trust, got, c.want)
		}
	}
}

func TestShellSingleQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://127.0.0.1:8080/hook", `'http://127.0.0.1:8080/hook'`},
		{"it's", `'it'\''s'`},
	}
	for _, c := range cases {
		if got := shellSingleQuote(c.in); got != c.want {
			t.Errorf("shellSingleQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWritePermissionSettingsShape locks down the exact settings.json shape
// --settings loads: a PreToolUse hook matching every tool ("*"), whose
// command pipes stdin straight to the URL via curl and falls back to a
// pre-formatted deny response if curl itself fails, and whose declared
// "timeout" exceeds curl's own --max-time (see the const doc comments in
// permission.go for why that ordering matters).
func TestWritePermissionSettingsShape(t *testing.T) {
	const url = "http://127.0.0.1:9999/permission-hook"
	path, err := writePermissionSettings(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg settingsFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("settings file isn't valid JSON: %v (%s)", err, raw)
	}
	if len(cfg.PreToolUse) != 1 || len(cfg.PreToolUse[0].Hooks) != 1 {
		t.Fatalf("expected exactly one PreToolUse matcher with one hook, got %+v", cfg)
	}
	m := cfg.PreToolUse[0]
	if m.Matcher != "*" {
		t.Errorf("matcher = %q, want \"*\"", m.Matcher)
	}
	h := m.Hooks[0]
	if h.Type != "command" {
		t.Errorf("hook type = %q, want \"command\"", h.Type)
	}
	if h.Timeout != permissionHookTimeoutSeconds {
		t.Errorf("hook timeout = %d, want %d", h.Timeout, permissionHookTimeoutSeconds)
	}
	if h.Timeout <= curlMaxTimeSeconds {
		t.Errorf("hook timeout %d must exceed curl --max-time %d, or the hook's own bound wins the race first", h.Timeout, curlMaxTimeSeconds)
	}
	if !strings.Contains(h.Command, "curl") || !strings.Contains(h.Command, shellSingleQuote(url)) {
		t.Errorf("command %q doesn't curl the given url", h.Command)
	}
	if !strings.Contains(h.Command, `"permissionDecision":"deny"`) {
		t.Errorf("command %q has no deny fallback for an unreachable service", h.Command)
	}
}

func TestPermissionArgsGating(t *testing.T) {
	cases := []struct {
		name     string
		trust    provider.Trust
		url      string
		wantArgs bool
	}{
		{"limited with url", provider.TrustLimited, "http://x/hook", true},
		{"readonly with url", provider.TrustReadOnly, "http://x/hook", true},
		{"full with url", provider.TrustFull, "http://x/hook", false},
		{"limited without url", provider.TrustLimited, "", false},
		{"zero value without url", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args, path, err := permissionArgs(c.trust, c.url)
			if err != nil {
				t.Fatal(err)
			}
			if path != "" {
				defer func() { _ = os.Remove(path) }()
			}
			if c.wantArgs {
				if len(args) != 2 || args[0] != "--settings" || args[1] != path || path == "" {
					t.Errorf("args=%v path=%q, want [--settings <path>] with a real path", args, path)
				}
			} else if args != nil || path != "" {
				t.Errorf("args=%v path=%q, want nil/empty", args, path)
			}
		})
	}
}
