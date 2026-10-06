package claudecode

import (
	"encoding/json"
	"os"
	"strings"
)

// pathScopeHookTimeout bounds how long claude waits for the path-scope
// hook to answer before failing the tool call. The hook is a local POSIX
// sh check (no network), so 5s is generous; a stuck hook must not stall
// the agent.
const pathScopeHookTimeout = 5

// pathScopeMatcher is the PreToolUse matcher the path-scope hook rides:
// the file tools whose input names a target path.
const pathScopeMatcher = "Write|Edit|MultiEdit|NotebookEdit"

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type matcherEntry struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []hookEntry `json:"hooks"`
}

// hookConfig maps a hook event name ("PreToolUse", "SessionStart", ...) to
// its matcher entries.
type hookConfig map[string][]matcherEntry

// settingsFile is the --settings JSON shape: hook events nest under a
// top-level "hooks" object, exactly as in ~/.claude/settings.json. The
// earlier flat shape ({"PreToolUse": [...]} at the top level — the plugin
// hooks.json layout, not a settings file's) loads without complaint and is
// silently ignored: verified against claude-code 2.1.285, where no hook in
// it ever ran. TestSettingsShapeRealClaude pins the nested shape against
// the real binary.
type settingsFile struct {
	Hooks hookConfig `json:"hooks"`
}

// marshalSettings is the one place a hook config becomes --settings bytes —
// production (writeSettings) and the real-binary shape test both go through
// it, so the test proves the bytes claude actually gets.
func marshalSettings(hooks hookConfig) ([]byte, error) {
	return json.Marshal(settingsFile{Hooks: hooks})
}

// writeSettings materializes hooks as a temp --settings file and returns its
// path, for the caller to remove when the session closes.
func writeSettings(hooks hookConfig) (string, error) {
	raw, err := marshalSettings(hooks)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "mads-settings-*.json")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(raw); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// sessionHooks is the hook config a session launches with: today only the
// persona path-scope hook (a PreToolUse entry on the file tools), present
// whenever PersonaAllowedPaths is non-empty, *independent of trust*. A local
// POSIX sh containment check (no network) that denies a file-tool call whose
// target path is outside the allowlist, with a reason the model can act on.
// This is claude-code's native enforcement surface for the portable
// "path_scope" permission intent (agent-concept redesign §2, Option A) — the
// reason IntentMatrix() declares IntentPathScope: Native.
//
// Asking the user is not a hook's job any more: limited/read-only trust runs
// claude's own manual permission mode and answers its prompts over the
// stream-json host channel (hostprompt.go). Nil when there is nothing to
// wire.
func sessionHooks(allowedPaths []string) hookConfig {
	if len(allowedPaths) == 0 {
		return nil
	}
	return hookConfig{"PreToolUse": {{
		Matcher: pathScopeMatcher,
		Hooks:   []hookEntry{{Type: "command", Command: pathScopeScript(allowedPaths), Timeout: pathScopeHookTimeout}},
	}}}
}

// permissionArgs returns the CLI args wiring the session's --settings file
// into claude's launch, and the file's path (for the caller to remove on
// session Close). Both zero when the session needs no hooks — no persona
// path scope.
func permissionArgs(allowedPaths []string) (args []string, path string, err error) {
	hooks := sessionHooks(allowedPaths)
	if hooks == nil {
		return nil, "", nil
	}
	path, err = writeSettings(hooks)
	if err != nil {
		return nil, "", err
	}
	return []string{"--settings", path}, path, nil
}

// pathScopeHookBody is the POSIX sh body of the path-scope hook, with the
// $ALLOWLIST placeholder. The shell comments use `#`, not `//` — that
// matters because the Go string is pasted verbatim into the hook command
// and `//` is a Go comment, not a sh one.
//
// Contract (claude settings PreToolUse "command"):
//   - stdin carries the PreToolUse JSON (tool_name, tool_input).
//   - the hook must exit 0; *stdout* carries the decision.
//   - empty stdout means "no decision", so claude proceeds.
//   - a JSON object with hookSpecificOutput.permissionDecision "deny"
//     (and permissionDecisionReason) blocks the call, the reason shown to
//     the model.
//
// The check: take tool_input.file_path, absolutize against `pwd` (POSIX
// sh has no $CWD variable — the hook process's cwd is set to the
// session's Cwd by claude's hook runner), accept iff it *is* one of the
// allowlist roots or lies *under* one. A root may carry a trailing slash
// ("src/"); the hook strips it before matching.
//
// Fail-open posture: a missing tool_input, an unrecognized path key, or a
// tool that renamed its key produces empty stdout and the call proceeds.
// A false-positive deny would break a legitimate write the persona
// explicitly allowed; a false-allow on a rename only widens the file-tool
// palette to what the trust layer already permits for that tool. A Bash
// call that reads a file it shouldn't is out of scope — Bash is a separate
// intent with its own enforcement (trustArgs: asked under TrustLimited,
// removed under TrustReadOnly).
const pathScopeHookBody = `
inp=$(cat)
# Pull the target path out of tool_input.file_path (the key every file
# tool in this matcher uses). No match -> empty path -> exit 0, fail open.
path=$(printf %s "$inp" | sed -n 's/.*"file_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)
[ -z "$path" ] && exit 0
# Absolutize against the session's working directory. POSIX sh has no
# CWD variable; the hook process's cwd is the adapter's StartOptions.Cwd
# (claude launches the hook from the session's tree), so pwd is the
# correct base.
case $path in
  /*) ;;
  *) path="$(pwd)/$path" ;;
esac
ok=1
while IFS= read -r root; do
  [ -z "$root" ] && continue
  case $root in
    /*) ;;
    *) root="$(pwd)/$root" ;;
  esac
  # Strip a trailing slash so "/data/" and "/data" both contain "/data/x".
  case $root in */) root=${root%/} ;; esac
  case $path in
    "$root" ) ok=0; break ;;
    "$root"/*) ok=0; break ;;
  esac
done <<EOF
$ALLOWLIST
EOF
[ $ok -eq 0 ] && exit 0
# Build the deny response with the offending path inlined. printf %s
# prints the literal format arg, so build the string first and emit it.
reason="path_scope: target $path is outside the persona allowlist"
printf '%s' "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"$reason\"}}"
`

// pathScopeScript emits a self-contained POSIX sh command enforcing the
// persona's path-scope allowlist on Write, Edit, MultiEdit, and
// NotebookEdit. It inlines the allowlist into the body (replacing
// $ALLOWLIST) and wraps the result in `sh -c '...'` with single-quote
// backslash-escaping so the whole thing is a single argv element claude
// can exec directly — no shell word-splitting of the allowlist's newlines,
// no dependency on env vars mads doesn't control.
func pathScopeScript(paths []string) string {
	body := strings.ReplaceAll(pathScopeHookBody, "$ALLOWLIST", strings.Join(paths, "\n"))
	esc := strings.ReplaceAll(body, "'", `'\''`)
	return "sh -c '" + esc + "'"
}
