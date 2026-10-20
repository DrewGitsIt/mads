package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readSettings loads a --settings file the adapter wrote.
func readSettings(t *testing.T, path string) settingsFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg settingsFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("settings file isn't valid JSON: %v (%s)", err, raw)
	}
	// The shape claude reads: every hook event under "hooks", nothing at
	// the top level (the flat shape claude silently ignores).
	var top map[string]json.RawMessage
	_ = json.Unmarshal(raw, &top)
	for k := range top {
		if k != "hooks" {
			t.Errorf("settings has top-level key %q; hook events must nest under \"hooks\" (%s)", k, raw)
		}
	}
	return cfg
}

func TestPermissionArgsGating(t *testing.T) {
	cases := []struct {
		name         string
		allowedPaths []string
		wantArgs     bool
	}{
		{"no paths", nil, false},
		{"empty paths", []string{}, false},
		{"paths", []string{"src/"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args, path, err := permissionArgs(c.allowedPaths)
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
// file carries, under "hooks", one PreToolUse matcher scoped to the file
// tools (Write|Edit|MultiEdit|NotebookEdit, NOT "*"), whose hook command is
// the local path-scope script (a real, run-under-sh command — no network),
// with a short timeout. Nothing else: asking the user is the host
// permission channel's job, not a hook's.
func TestWritePermissionSettingsPathScope(t *testing.T) {
	_, path, err := permissionArgs([]string{"src/", "docs"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()
	cfg := readSettings(t, path)
	if len(cfg.Hooks) != 1 || len(cfg.Hooks["PreToolUse"]) != 1 {
		t.Fatalf("expected exactly one PreToolUse matcher (path-scope only), got %+v", cfg)
	}
	m := cfg.Hooks["PreToolUse"][0]
	if m.Matcher != pathScopeMatcher {
		t.Errorf("matcher = %q, want %s (scoped to file tools, not *)", m.Matcher, pathScopeMatcher)
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
}

// TestSettingsShapeRealClaude is the zero-token contract test for the
// --settings shape: it builds a settings file through the same code
// production uses (sessionHooks + writeSettings → marshalSettings), adds a
// SessionStart command hook that touches a marker file, and runs the real
// binary pointed at a dead API endpoint with a bogus key. claude fires
// SessionStart hooks before its first API call, which can only fail (no
// tokens, no auth) — so the marker appears iff claude actually read the
// hooks. The flat shape this package used to write fails this test: it
// loads without complaint and no hook ever runs. claude may exit at once
// or sit retrying the dead endpoint depending on its environment, so the
// test watches for the marker and kills the process either way. Opt-in
// with MADS_CLAUDE_SMOKE=1, like the other real-binary tests: a plain
// `go test ./...` must never start a real harness. Still zero-token.
// MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run SettingsShapeRealClaude
func TestSettingsShapeRealClaude(t *testing.T) {
	if os.Getenv("MADS_CLAUDE_SMOKE") != "1" {
		t.Skip("set MADS_CLAUDE_SMOKE=1 to run against the real claude binary (zero tokens)")
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not on PATH")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "session-start-fired")
	hooks := sessionHooks([]string{dir})
	hooks["SessionStart"] = []matcherEntry{{
		Hooks: []hookEntry{{Type: "command", Command: "touch " + shellQuote(marker), Timeout: 10}},
	}}
	path, err := writeSettings(hooks)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", "--settings", path, "hi")
	cmd.Dir = dir
	cmd.Stdin = nil // /dev/null
	cmd.Env = append(os.Environ(),
		"ANTHROPIC_API_KEY=sk-bogus",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:9", // discard port: nothing listens
		"CLAUDE_CODE_MAX_RETRIES=0",
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() { cancel(); <-exited }()

	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		select {
		case <-tick.C:
		case <-exited:
			if _, err := os.Stat(marker); err == nil {
				return
			}
			t.Fatalf("claude exited without firing the SessionStart hook — it ignored the --settings hooks (shape %s): %s", mustRead(t, path), out.String())
		case <-ctx.Done():
			t.Fatalf("SessionStart hook never fired within the bound (shape %s)", mustRead(t, path))
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// runPathScopeHook runs the *real* hook command the adapter would emit for
// a path-scope session against one PreToolUse stdin JSON, returning the
// hook's stdout (the decision) and whether it exit-0'd. This is the
// end-to-end proof that the allowlist semantics (allow in-scope, deny
// out-of-scope, fail-open on no path) hold under a real POSIX sh — the
// claude hook runner's shell.
func runPathScopeHook(t *testing.T, cworkDir string, stdinJSON string) (out string, exitOK bool) {
	t.Helper()
	_, path, err := permissionArgs([]string{"src/", "docs"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()
	cfg := readSettings(t, path)
	if len(cfg.Hooks["PreToolUse"]) != 1 {
		t.Fatalf("expected the path-scope-only matcher, got %+v", cfg)
	}
	cmd := cfg.Hooks["PreToolUse"][0].Hooks[0].Command

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
