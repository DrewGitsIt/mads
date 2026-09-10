package claudecode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// Every claude-code tool name maps onto the normalized kind vocabulary;
// mcp__<server>__<tool> names carry their server out separately. Unknown
// names classify as "other" — and, matching toolMutates's fail-closed rule,
// must never be reported as mutating.
func TestToolKindMapping(t *testing.T) {
	cases := []struct {
		name   string
		kind   provider.ToolKind
		server string
	}{
		{"Bash", provider.ToolKindExecute, ""},
		{"Grep", provider.ToolKindSearch, ""},
		{"WebSearch", provider.ToolKindSearch, ""},
		{"Read", provider.ToolKindRead, ""},
		{"Glob", provider.ToolKindRead, ""},
		{"Write", provider.ToolKindEdit, ""},
		{"Edit", provider.ToolKindEdit, ""},
		{"MultiEdit", provider.ToolKindEdit, ""},
		{"NotebookEdit", provider.ToolKindEdit, ""},
		{"WebFetch", provider.ToolKindFetch, ""},
		{"Agent", provider.ToolKindAgent, ""},
		{"Task", provider.ToolKindAgent, ""},
		{"TodoWrite", provider.ToolKindPlan, ""},
		{"Skill", provider.ToolKindSkill, ""},
		{"mcp__github__create_issue", provider.ToolKindMCP, "github"},
		{"mcp__plugin_name__tool__extra", provider.ToolKindMCP, "plugin_name"},
		{"SomeFutureTool", provider.ToolKindOther, ""},
		{"mcp__", provider.ToolKindOther, ""},
	}
	for _, c := range cases {
		kind, server := toolKind(c.name)
		if kind != c.kind || server != c.server {
			t.Errorf("toolKind(%q) = %q/%q, want %q/%q", c.name, kind, server, c.kind, c.server)
		}
		// An unclassified tool must fail closed on the §5 file-change signal
		// too — only the known write tools mutate.
		if kind != provider.ToolKindEdit && toolMutates(c.name) {
			t.Errorf("toolMutates(%q) = true for non-edit kind %q", c.name, kind)
		}
	}
}

// toolTitle keeps parity with the UI summarize() rules it replaces: Bash
// description||command, Grep/Glob pattern (in path), Agent description,
// WebFetch url, WebSearch query, file tools their path, TodoWrite a plan
// summary, MCP server:tool plus a best-effort value, fallback the first
// string value.
func TestToolTitle(t *testing.T) {
	cases := []struct {
		desc  string
		name  string
		input string
		want  string
	}{
		{"bash prefers description", "Bash",
			`{"command":"ls -la /tmp","description":"List files"}`, "List files"},
		{"bash falls back to command", "Bash",
			`{"command":"ls -la /tmp"}`, "ls -la /tmp"},
		{"bash multiline command clipped to first line", "Bash",
			`{"command":"cd /tmp\nmake all"}`, "cd /tmp…"},
		{"grep pattern in path", "Grep",
			`{"pattern":"ToolUse","path":"/home/u/app"}`, "ToolUse in /home/u/app"},
		{"glob pattern alone", "Glob",
			`{"pattern":"**/*.go"}`, "**/*.go"},
		{"agent description", "Agent",
			`{"subagent_type":"general-purpose","description":"Run ls in current directory","prompt":"Run ls."}`,
			"Run ls in current directory"},
		{"webfetch url", "WebFetch",
			`{"url":"https://example.com/doc","prompt":"summarize"}`, "https://example.com/doc"},
		{"websearch query", "WebSearch",
			`{"query":"go json decoder token order"}`, "go json decoder token order"},
		{"read path", "Read",
			`{"file_path":"/home/u/app/main.go"}`, "/home/u/app/main.go"},
		{"write path", "Write",
			`{"file_path":"/home/u/app/new.go","content":"package app\n"}`, "/home/u/app/new.go"},
		{"notebook path", "NotebookEdit",
			`{"notebook_path":"/home/u/nb.ipynb","new_source":"x"}`, "/home/u/nb.ipynb"},
		{"todo progress with current item", "TodoWrite",
			`{"todos":[{"content":"Add fields","status":"completed"},{"content":"Map kinds","status":"in_progress"},{"content":"Write tests","status":"pending"}]}`,
			"1/3 · Map kinds"},
		{"todo progress without current item", "TodoWrite",
			`{"todos":[{"content":"Add fields","status":"completed"},{"content":"Write tests","status":"pending"}]}`,
			"1/2 todos"},
		{"mcp server:tool with best-effort value", "mcp__github__create_issue",
			`{"title":"flaky test"}`, "github:create_issue — flaky test"},
		{"mcp server:tool bare", "mcp__github__list_repos",
			`{"per_page":30}`, "github:list_repos"},
		{"unknown tool falls back to first string value", "SomeFutureTool",
			`{"count":3,"target":"main branch"}`, "main branch"},
		{"unknown tool with a path prefers it", "SomeFutureTool",
			`{"file_path":"/home/u/x.txt","note":"hello"}`, "/home/u/x.txt"},
		{"empty input yields empty title", "SomeFutureTool", `{}`, ""},
		{"grep without pattern falls back to path", "Grep",
			`{"path":"/home/u/app"}`, "/home/u/app"},
	}
	for _, c := range cases {
		input := json.RawMessage(c.input)
		if got := toolTitle(c.name, input, toolPath(input)); got != c.want {
			t.Errorf("%s: toolTitle(%q, %s) = %q, want %q", c.desc, c.name, c.input, got, c.want)
		}
	}
}

// Long single-line values still cap at titleCap runes with an ellipsis.
func TestToolTitleCapped(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := toolTitle("Bash", json.RawMessage(`{"command":"`+long+`"}`), "")
	if r := []rune(got); len(r) != titleCap || !strings.HasSuffix(got, "…") {
		t.Fatalf("capped title: %d runes, suffix %q", len([]rune(got)), got[len(got)-3:])
	}
}

// The wire path assembles the whole enriched shape: kind, title, location
// from the input's file path, empty status (claude-code's wire states none —
// synthesis is the layer above's job, T5).
func TestToolUseEnrichedOnWire(t *testing.T) {
	l := parseWireLine(t, `{"type":"assistant","message":{"role":"assistant","content":[`+
		`{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/home/u/a.go","old_string":"a","new_string":"b"}},`+
		`{"type":"tool_use","id":"t2","name":"mcp__github__create_issue","input":{"title":"flaky test"}}]}}`)
	evs := l.events(false, false)
	if len(evs) != 2 {
		t.Fatalf("want 2 tool_use events, got %+v", evs)
	}

	edit := evs[0].Tool
	if edit.Kind != provider.ToolKindEdit || edit.Title != "/home/u/a.go" ||
		edit.Status != "" || edit.Server != "" || !edit.Mutates {
		t.Errorf("edit tool: %+v", edit)
	}
	if len(edit.Locations) != 1 || edit.Locations[0] != (provider.ToolLocation{Path: "/home/u/a.go"}) {
		t.Errorf("edit locations: %+v", edit.Locations)
	}

	mcp := evs[1].Tool
	if mcp.Kind != provider.ToolKindMCP || mcp.Server != "github" ||
		mcp.Title != "github:create_issue — flaky test" || mcp.Mutates || mcp.Status != "" {
		t.Errorf("mcp tool: %+v", mcp)
	}
	if mcp.Locations != nil {
		t.Errorf("mcp locations: want none, got %+v", mcp.Locations)
	}
}

// The real capture's two tool_use lines (the main agent's Agent spawn and
// the subagent's Bash) come out enriched.
func TestFixtureToolUsesEnriched(t *testing.T) {
	var tools []*provider.ToolUse
	for _, ev := range fixtureEvents(t, taskFixture, false) {
		if ev.Kind == provider.EventToolUse {
			tools = append(tools, ev.Tool)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("want 2 tool_use events in fixture, got %+v", tools)
	}
	agent, bash := tools[0], tools[1]
	if agent.Name != "Agent" || agent.Kind != provider.ToolKindAgent || agent.Title != fixtureTaskDesc {
		t.Errorf("agent tool: %+v", agent)
	}
	if bash.Name != "Bash" || bash.Kind != provider.ToolKindExecute ||
		bash.Title != "List files and directories in the current working directory" {
		t.Errorf("bash tool: %+v", bash)
	}
	for i, tu := range tools {
		if tu.Status != "" {
			t.Errorf("tool %d: status %q, want empty (wire states none)", i, tu.Status)
		}
	}
}
