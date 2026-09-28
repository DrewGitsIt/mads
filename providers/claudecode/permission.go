package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// permissionHookTimeoutSeconds is the ask-first hook's own declared timeout
// (settings "timeout" field) — the outermost of the three layered bounds
// that keep an unanswered prompt from hanging the agent forever.
const permissionHookTimeoutSeconds = 660

// curlMaxTimeSeconds bounds the ask-first hook subprocess's own blocking
// HTTP call — the second layer, in case the session service itself never
// answers.
const curlMaxTimeSeconds = 640

// pathScopeHookTimeout bounds how long claude waits for the path-scope
// hook to answer before failing the tool call. The hook is a local POSIX
// sh check (no network), so 5s is generous; a stuck hook must not stall
// the agent the way an unanswered approval might.
const pathScopeHookTimeout = 5

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type matcherEntry struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// settingsFile is the "direct" (non-plugin) settings.json shape --settings
// loads: hook events as top-level keys, no wrapping "hooks" object.
type settingsFile struct {
	PreToolUse []matcherEntry `json:"PreToolUse"`
}

// needsPermissionHook reports whether trust leaves any tool call subject to
// claude's default ask-first prompt. TrustLimited/TrustReadOnly disallow
// some tools outright (trustArgs) but leave the rest asking; TrustFull
// runs --permission-mode bypassPermissions and never asks at all. This
// gate applies to the *ask-first* hook only — the path-scope hook (a
// separate matcher entry in the same settings file) fires on a persona's
// allowlist regardless of trust.
func needsPermissionHook(trust provider.Trust) bool {
	switch trust {
	case provider.TrustLimited, provider.TrustReadOnly:
		return true
	default:
		return false
	}
}

// shellSingleQuote wraps s for safe interpolation into a POSIX sh command
// line. The URL is ours (built from the session service's own listener
// address), but quoting defensively costs nothing.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writePermissionSettings materializes a --settings file carrying up to
// two PreToolUse hook entries:
//
//  1. the ask-first hook (matcher "*" — every tool) — fired on every
//     tool call *while trust leaves the tool asking*
//     (TrustLimited/ReadOnly) and a callback URL is supplied. It pipes
//     each call's JSON to the session service's permission endpoint and
//     prints the response verbatim — the handler is what pre-formats the
//     exact {"hookSpecificOutput": ...} JSON claude expects, so the
//     shell command itself never parses or builds JSON. If curl fails
//     outright (connection refused, its own --max-time expiry), the
//     fallback denies rather than leaving claude to interpret empty or
//     malformed stdout.
//
//  2. the path-scope hook (matcher "Write|Edit|MultiEdit|NotebookEdit")
//     — fired whenever PersonaAllowedPaths is non-empty, *independent of
//     trust*. A local POSIX sh containment check (no network) that
//     denies a file-tool call whose target path is outside the
//     allowlist, with a reason the model can act on. This is
//     claude-code's native enforcement surface for the portable
//     "path_scope" permission intent (agent-concept redesign §2, Option
//     A) — the reason IntentMatrix() now declares IntentPathScope:
//     Native.
//
// Either entry may be absent; the file is written only when at least one
// is present (see permissionArgs, which gates the call).
func writePermissionSettings(trust provider.Trust, url string, allowedPaths []string) (path string, err error) {
	entries := make([]matcherEntry, 0, 2)
	if needsPermissionHook(trust) && url != "" {
		command := fmt.Sprintf(
			`curl -s -X POST --max-time %d -d @- %s || echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"},"systemMessage":"permission service unreachable"}'`,
			curlMaxTimeSeconds, shellSingleQuote(url),
		)
		entries = append(entries, matcherEntry{
			Matcher: "*",
			Hooks:   []hookEntry{{Type: "command", Command: command, Timeout: permissionHookTimeoutSeconds}},
		})
	}
	if len(allowedPaths) > 0 {
		entries = append(entries, matcherEntry{
			Matcher: "Write|Edit|MultiEdit|NotebookEdit",
			Hooks:   []hookEntry{{Type: "command", Command: pathScopeScript(allowedPaths), Timeout: pathScopeHookTimeout}},
		})
	}
	if len(entries) == 0 {
		return "", nil
	}
	cfg := settingsFile{PreToolUse: entries}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "mads-permission-*.json")
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

// permissionArgs returns the CLI args wiring the --settings file into
// claude's launch, and the settings file path (for the caller to remove
// on session Close). Both zero/nil when neither hook is required — a
// terminal transport that never sets opts.PermissionPromptURL on a persona
// with no path scope, or a TrustFull session with no persona.
func permissionArgs(trust provider.Trust, url string, allowedPaths []string) (args []string, path string, err error) {
	if !needsPermissionHook(trust) && url == "" && len(allowedPaths) == 0 {
		return nil, "", nil
	}
	path, err = writePermissionSettings(trust, url, allowedPaths)
	if err != nil {
		return nil, "", err
	}
	if path == "" {
		return nil, "", nil
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
// intent with its own TrustLimited/ReadOnly enforcement (trustArgs).
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
