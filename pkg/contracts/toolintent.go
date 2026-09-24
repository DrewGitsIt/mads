package contracts

// ToolIntent is one portable, provider-independent "what a tool does" label
// for the persona's allow/deny policy (spec §9, redesign §2). It is the
// *vocabulary* mads uses to express a persona's tool allowance:
//
//   - the persona declares `Allowlist`/`Denylist` as ToolIntents
//   - each harness adapter maps ToolIntents -> its own concrete tool names
//     (the `ToolIntentResolver` in providers/provider)
//   - the runner never mentions a concrete tool name: it passes ToolIntents
//     through to the harness and trusts the resolver.
//
// This is deliberately distinct from PermissionIntent (permission.go):
// PermissionIntent is a *restriction* (deny_commands, path_scope,
// read_only, require_approval) — the things Trust and AgentPermissions
// compile down to. ToolIntent is a *capability* — the things the persona
// is allowed to do at all. The two combine: a persona that Allowlists
// "shell" but whose session Trust is TrustReadOnly gets blocked by the
// Trust's read_only intent even though the persona permits shell.
type ToolIntent string

const (
	// IntentRead — inspect file contents.
	IntentRead ToolIntent = "read"
	// IntentWrite — create/overwrite file contents.
	IntentWrite ToolIntent = "write"
	// IntentEdit — surgically modify existing file contents.
	IntentEdit ToolIntent = "edit"
	// IntentShell — execute a command in a shell.
	IntentShell ToolIntent = "shell"
	// IntentSearch — find code / files / patterns across a tree.
	IntentSearch ToolIntent = "search"
	// IntentFetch — make an outbound network request (web fetch / http
	// tool in a harness's vocabulary).
	IntentFetch ToolIntent = "fetch"
	// IntentSkill — invoke a mads capability-registry skill.
	IntentSkill ToolIntent = "skill"
	// IntentMCP — invoke a mads capability-registry MCP server tool.
	IntentMCP ToolIntent = "mcp"
)

// ToolIntents is the full closed set (for validation and UI enumeration).
var ToolIntents = []ToolIntent{
	IntentRead, IntentWrite, IntentEdit, IntentShell,
	IntentSearch, IntentFetch, IntentSkill, IntentMCP,
}

// Valid reports whether i is a registered ToolIntent.
func (i ToolIntent) Valid() bool {
	for _, v := range ToolIntents {
		if v == i {
			return true
		}
	}
	return false
}

// LegacyToolToIntent maps the old concrete tool NAMES (the shape
// `capabilities.tools` held before the persona split) to the ToolIntent
// they most closely approximate. Used by the registry's read-time migration
// so a persona saved with `"capabilities": {"tools": ["bash"]}` still
// enforces its shell intent, and by the `mads personas` CLI's legacy
// import path. Unknown names map to "" (no intent) — callers ignore "".
var LegacyToolToIntent = map[string]ToolIntent{
	// claude-code
	"Read":     IntentRead,
	"Write":    IntentWrite,
	"Edit":     IntentEdit,
	"Bash":     IntentShell,
	"Grep":     IntentSearch,
	"Glob":     IntentSearch,
	"WebFetch": IntentFetch,
	// goose acp
	"read":   IntentRead,
	"write":  IntentWrite,
	"edit":   IntentEdit,
	"shell":  IntentShell,
	"search": IntentSearch,
	"fetch":  IntentFetch,
	// kiro (best effort — Kiro's own tool vocabulary is not fixed)
	"read_file":   IntentRead,
	"write_file":  IntentWrite,
	"edit_file":   IntentEdit,
	"run_command": IntentShell,
	"fetch_url":   IntentFetch,
}

// LegacyCommandToIntent compiles an old `denied_commands` entry to the
// single ToolIntent it most restrictively implies. The policy was
// command-string-based (harness-leaky); the persona split expresses the
// same intent-level restriction via a `Denylist` entry. The mapping is
// intentionally coarse: any non-empty shell command denies `IntentShell`,
// which is the strongest statement that entry could have made. Callers
// should treat "" as "no restriction implied" and skip.
func LegacyCommandToIntent(cmd string) ToolIntent {
	trimmed := trimSpace(cmd)
	if trimmed == "" {
		return ""
	}
	// Every denied_commands entry is a shell-string blocklist, by
	// construction. The persona split replaces the string list with the
	// single "shell" ToolIntent deny — the user can still scope *paths*
	// via AllowedPaths, and the harness itself can still restrict specific
	// executables if it chooses to (the resolver is per-harness).
	return IntentShell
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[:end-start]
}
