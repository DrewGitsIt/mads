package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// permissionHookTimeoutSeconds is the hook's own declared timeout (settings
// "timeout" field) — the outermost of the three layered bounds that keep an
// unanswered prompt from hanging the agent forever. It must exceed both
// curlMaxTimeSeconds below and the Runner's own resolve-by bound
// (services/session/internal/runner/permissions.go), so the server-side
// "deny after ~10 minutes" response always wins the race.
const permissionHookTimeoutSeconds = 660

// curlMaxTimeSeconds bounds the hook subprocess's own blocking HTTP call —
// the second layer, in case the session service itself never answers (e.g.
// it crashed mid-request).
const curlMaxTimeSeconds = 640

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
// claude's default ask-first prompt. TrustLimited/TrustReadOnly disallow some
// tools outright (trustArgs) but leave the rest asking; TrustFull runs
// --permission-mode bypassPermissions and never asks at all.
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

// writePermissionSettings materializes a --settings file wiring a PreToolUse
// hook for every tool ("*") to url. The hook command pipes its stdin (the
// PreToolUse JSON claude sends) straight through as the POST body and prints
// the response straight back to stdout: the session service's handler is
// what pre-formats the exact {"hookSpecificOutput": ...} JSON claude expects,
// so the shell command itself never parses or builds JSON. If curl fails
// outright (connection refused, its own --max-time expiry), the fallback
// denies rather than leaving claude to interpret empty/malformed stdout.
func writePermissionSettings(url string) (path string, err error) {
	command := fmt.Sprintf(
		`curl -s -X POST --max-time %d -d @- %s || echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"},"systemMessage":"permission service unreachable"}'`,
		curlMaxTimeSeconds, shellSingleQuote(url),
	)
	cfg := settingsFile{PreToolUse: []matcherEntry{{
		Matcher: "*",
		Hooks:   []hookEntry{{Type: "command", Command: command, Timeout: permissionHookTimeoutSeconds}},
	}}}
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

// permissionArgs returns the CLI args wiring the hook into claude's launch,
// and the settings file path (for the caller to remove on session Close) —
// both zero/nil when trust doesn't need it or no callback URL was supplied
// (e.g. terminal transport, which never sets opts.PermissionPromptURL).
func permissionArgs(trust provider.Trust, url string) (args []string, path string, err error) {
	if !needsPermissionHook(trust) || url == "" {
		return nil, "", nil
	}
	path, err = writePermissionSettings(url)
	if err != nil {
		return nil, "", err
	}
	return []string{"--settings", path}, path, nil
}
