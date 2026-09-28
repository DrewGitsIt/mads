package claudecode

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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
// --settings loads for the *ask-first-only* case: a single PreToolUse hook
// matching every tool ("*"), whose command pipes stdin straight to the URL
// via curl and falls back to a pre-formatted deny response if curl itself
// fails, and whose declared "timeout" exceeds curl's own --max-time (see
// the const doc comments in permission.go for why that ordering matters).
// The path-scope-only and both-hooks shapes are covered by
// TestWritePermissionSettingsPathScope below.
func TestWritePermissionSettingsShape(t *testing.T) {
	const url = "http://127.0.0.1:9999/permission-hook"
	path, err := writePermissionSettings(provider.TrustLimited, url, nil)
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
	// Ask-first-only call (no paths): exactly one PreToolUse matcher.
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
		name         string
		trust        provider.Trust
		url          string
		allowedPaths []string
		wantArgs     bool
	}{
		{"limited with url", provider.TrustLimited, "http://x/hook", nil, true},
		{"readonly with url", provider.TrustReadOnly, "http://x/hook", nil, true},
		{"full with url", provider.TrustFull, "http://x/hook", nil, false},
		{"limited without url", provider.TrustLimited, "", nil, false},
		{"zero value without url", "", "", nil, false},
		// Path-scope-only: no trust hook, no URL — the allowlist alone
		// must still produce a --settings file (the point of Option A).
		{"full with paths (no url)", provider.TrustFull, "", []string{"src/"}, true},
		{"limited with url and paths", provider.TrustLimited, "http://x/hook", []string{"src/"}, true},
		{"full with empty paths", provider.TrustFull, "", []string{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args, path, err := permissionArgs(c.trust, c.url, c.allowedPaths)
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

// TestWritePermissionSettingsPathScope locks down the settings.json shape
// when a persona declares an allowlist (path_scope intent, Option A): the
// file carries a PreToolUse matcher scoped to the file tools
// (Write|Edit|MultiEdit|NotebookEdit, NOT "*"), whose hook command is the
// local path-scope script (a real, run-under-sh command — no curl), with a
// short timeout far below the ask-first hook's bound. The trust-ask-first
// entry is independent: with trust leaving a tool asking and a URL, BOTH
// entries must be present (the hooks coexist in one file).
func TestWritePermissionSettingsPathScope(t *testing.T) {
	t.Run("paths only (TrustFull, no url)", func(t *testing.T) {
		path, err := writePermissionSettings(provider.TrustFull, "", []string{"src/", "docs"})
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
		if len(cfg.PreToolUse) != 1 {
			t.Fatalf("expected exactly one PreToolUse matcher (path-scope only), got %d: %+v", len(cfg.PreToolUse), cfg)
		}
		m := cfg.PreToolUse[0]
		if m.Matcher != "Write|Edit|MultiEdit|NotebookEdit" {
			t.Errorf("matcher = %q, want Write|Edit|MultiEdit|NotebookEdit (scoped to file tools, not *)", m.Matcher)
		}
		if len(m.Hooks) != 1 {
			t.Fatalf("expected exactly one hook, got %d", len(m.Hooks))
		}
		h := m.Hooks[0]
		if h.Type != "command" {
			t.Errorf("hook type = %q, want command", h.Type)
		}
		if h.Timeout != pathScopeHookTimeout {
			t.Errorf("hook timeout = %d, want %d", h.Timeout, pathScopeHookTimeout)
		}
		if h.Timeout >= permissionHookTimeoutSeconds {
			t.Errorf("path-scope hook timeout %d must be far below the ask-first hook's %d", h.Timeout, permissionHookTimeoutSeconds)
		}
		if strings.Contains(h.Command, "curl") {
			t.Errorf("path-scope hook must be local (no network), but its command curls: %q", h.Command)
		}
		if !strings.Contains(h.Command, "src/") || !strings.Contains(h.Command, "docs") {
			t.Errorf("hook command must embed the allowlist (src/, docs): %q", h.Command)
		}
		if !strings.Contains(h.Command, "permissionDecisionReason") || !strings.Contains(h.Command, "outside the persona allowlist") {
			t.Errorf("hook command has no deny-reason construction for an out-of-scope path: %q", h.Command)
		}
		if !strings.Contains(h.Command, "hookEventName") || !strings.Contains(h.Command, "PreToolUse") {
			t.Errorf("hook command has no PreToolUse hook-event envelope in its deny output: %q", h.Command)
		}
	})

	t.Run("paths AND ask-first (TrustLimited + url + paths)", func(t *testing.T) {
		path, err := writePermissionSettings(provider.TrustLimited, "http://x/hook", []string{"src/"})
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
		if len(cfg.PreToolUse) != 2 {
			t.Fatalf("expected two PreToolUse matchers (ask-first + path-scope), got %d: %+v", len(cfg.PreToolUse), cfg)
		}
		matchers := map[string]bool{}
		for _, m := range cfg.PreToolUse {
			matchers[m.Matcher] = true
		}
		if !matchers["*"] {
			t.Errorf("missing the ask-first matcher (\"*\")")
		}
		if !matchers["Write|Edit|MultiEdit|NotebookEdit"] {
			t.Errorf("missing the path-scope matcher")
		}
	})

	t.Run("neither hook -> no file", func(t *testing.T) {
		path, err := writePermissionSettings(provider.TrustFull, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if path != "" {
			t.Errorf("want no settings file when neither hook is required, got %q", path)
		}
	})
}

// runPathScopeHook runs the *real* hook command the adapter would emit for
// the given trust/url/path-set against one PreToolUse stdin JSON, returning
// the hook's stdout (the decision) and whether it exit-0'd. This is the
// end-to-end proof that the allowlist semantics (allow in-scope, deny
// out-of-scope, fail-open on no path) hold under a real POSIX sh — the
// claude hook runner's shell.
func runPathScopeHook(t *testing.T, cworkDir string, stdinJSON string) (out string, exitOK bool) {
	t.Helper()
	// Rebuild the exact command the adapter would wire for a
	// path-scope-only session. TrustFull + no url isolates the path-scope
	// matcher from the ask-first one.
	path, err := writePermissionSettings(provider.TrustFull, "", []string{"src/", "docs"})
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
		t.Fatal(err)
	}
	if len(cfg.PreToolUse) != 1 {
		t.Fatalf("expected the path-scope-only matcher, got %+v", cfg)
	}
	cmd := cfg.PreToolUse[0].Hooks[0].Command

	sh := exec.Command("sh", "-c", cmd)
	sh.Dir = cworkDir
	sh.Stdin = strings.NewReader(stdinJSON)
	var buf bytes.Buffer
	sh.Stdout = &buf
	err = sh.Run()
	return buf.String(), err == nil
}

// TestPathScopeHookSemantics is the behavior test that proves Option A:
// in-scope file_path -> empty stdout (allow/out-of-scope -> deny JSON; no
// file_path -> fail-open (empty stdout).
func TestPathScopeHookSemantics(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name     string
		stdin    string
		wantDeny bool
	}{
		{"in-scope absolute", `{"tool_name":"Write","tool_input":{"file_path":"` + dir + `/src/a.go"}}"`, false},
		{"in-scope root exact", `{"tool_name":"Write","tool_input":{"file_path":"` + dir + `/src"}}"`, false},
		{"in-scope relative", `{"tool_name":"Write","tool_input":{"file_path":"src/a.go"}}"`, false},
		{"in-scope second root", `{"tool_name":"Edit","tool_input":{"file_path":"docs/README.md"}}"`, false},
		{"out-of-scope sibling", `{"tool_name":"Write","tool_input":{"file_path":"src2/a.go"}}"`, true},
		{"out-of-scope parent", `{"tool_name":"Write","tool_input":{"file_path":"` + dir + `/.env"}}"`, true},
		{"out-of-scope absolute other", `{"tool_name":"Bash","tool_input":{"file_path":"/etc/passwd"}}"`, true},
		{"no file_path fails open", `{"tool_name":"Write","tool_input":{"nope":1}}`, false},
		{"empty tool_input fails open", `{"tool_name":"Write","tool_input":{}}`, false},
		{"no tool_input fails open", `{"tool_name":"Write"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, exitOK := runPathScopeHook(t, dir, c.stdin)
			if !exitOK {
				t.Fatalf("hook did not exit 0 (claude would fail the tool call on a non-zero exit): out=%q", out)
			}
			detected := strings.Contains(out, `"permissionDecision":"deny"`)
			if detected != c.wantDeny {
				t.Errorf("deny detected = %v, want %v; hook stdout=%q", detected, c.wantDeny, out)
			}
			if c.wantDeny {
				if !strings.Contains(out, "hookEventName") || !strings.Contains(out, "PreToolUse") {
					t.Errorf("deny response missing hookEventName/PreToolUse envelope: %q", out)
				}
			}
		})
	}
}
