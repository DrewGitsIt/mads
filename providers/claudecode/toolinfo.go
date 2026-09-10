package claudecode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// newToolUse builds the enriched normalized ToolUse for one wire tool_use
// block: name→kind classification, a server-built one-line title, and the
// touched file as a location. Status stays empty on purpose — claude-code's
// wire never states one, and synthesizing the lifecycle (in_progress on
// arrival, interrupted on a resultless turn end, ...) is the defensive layer
// above's job (T5), not the adapter's.
func newToolUse(id, name string, input json.RawMessage) *provider.ToolUse {
	kind, server := toolKind(name)
	path := toolPath(input)
	tu := &provider.ToolUse{
		ID:      id,
		Name:    name,
		Input:   input,
		Path:    path,
		Mutates: toolMutates(name),
		Kind:    kind,
		Server:  server,
		Title:   toolTitle(name, input, path),
	}
	if path != "" {
		tu.Locations = []provider.ToolLocation{{Path: path}}
	}
	return tu
}

// toolKind maps a claude-code tool name onto the normalized kind vocabulary,
// returning the MCP server name alongside for mcp__<server>__<tool> names.
// Unknown names are ToolKindOther — and, matching toolMutates's fail-closed
// rule, an unclassified tool is never reported as mutating either.
func toolKind(name string) (provider.ToolKind, string) {
	if server, _, ok := mcpTool(name); ok {
		return provider.ToolKindMCP, server
	}
	switch name {
	case "Bash":
		return provider.ToolKindExecute, ""
	case "Grep", "WebSearch":
		return provider.ToolKindSearch, ""
	case "Read", "Glob":
		return provider.ToolKindRead, ""
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		return provider.ToolKindEdit, ""
	case "WebFetch":
		return provider.ToolKindFetch, ""
	case "Agent", "Task":
		// claude-code 2.x names the subagent tool "Agent"; older streams
		// (and its own system-prompt vocabulary) say "Task".
		return provider.ToolKindAgent, ""
	case "TodoWrite":
		return provider.ToolKindPlan, ""
	case "Skill":
		return provider.ToolKindSkill, ""
	}
	return provider.ToolKindOther, ""
}

// mcpTool splits an mcp__<server>__<tool> name; ok is false for any other
// shape. A truncated mcp__<server> still counts as MCP with an empty tool —
// better a bare server badge than misclassifying as "other".
func mcpTool(name string) (server, tool string, ok bool) {
	rest, found := strings.CutPrefix(name, "mcp__")
	if !found || rest == "" {
		return "", "", false
	}
	server, tool, _ = strings.Cut(rest, "__")
	return server, tool, true
}

// titleCap bounds a title to one concise line — the UI start-truncates
// further as needed, but the wire should never carry a whole script.
const titleCap = 120

// toolTitle builds the one-line human summary the conversation pane renders
// as the row title — the server-side successor of the UI's ToolCard
// summarize() (ui/src/conversation/ChatRenderer/ToolCard.tsx), same rules:
// what the command does (Bash), what's being searched for and where
// (Grep/Glob), the delegated task (Agent), the target (WebFetch/WebSearch),
// the file (anything with a path), a progress line for the plan tool, and
// server:tool for MCP. Falls back to the referenced file, then the first
// string value in the input. Empty when the input says nothing at all.
func toolTitle(name string, input json.RawMessage, path string) string {
	var in struct {
		Description string     `json:"description"`
		Command     string     `json:"command"`
		Pattern     string     `json:"pattern"`
		Path        string     `json:"path"`
		URL         string     `json:"url"`
		Query       string     `json:"query"`
		Todos       []wireTodo `json:"todos"`
	}
	_ = json.Unmarshal(input, &in)

	switch name {
	case "Bash":
		if t := in.Description; t != "" {
			return clip(t)
		}
		if in.Command != "" {
			return clip(in.Command)
		}
	case "Grep", "Glob":
		if in.Pattern != "" {
			if in.Path != "" {
				return clip(in.Pattern + " in " + in.Path)
			}
			return clip(in.Pattern)
		}
	case "Agent", "Task":
		if in.Description != "" {
			return clip(in.Description)
		}
	case "WebFetch":
		if in.URL != "" {
			return clip(in.URL)
		}
	case "WebSearch":
		if in.Query != "" {
			return clip(in.Query)
		}
	case "Read", "Write", "Edit", "MultiEdit", "NotebookEdit":
		if path != "" {
			return path
		}
	case "TodoWrite":
		if t := todoTitle(in.Todos); t != "" {
			return t
		}
	}
	if server, tool, ok := mcpTool(name); ok {
		base := server + ":" + tool
		if v := firstString(input); v != "" {
			return clip(base + " — " + v)
		}
		return base
	}
	if path != "" {
		return path
	}
	return clip(firstString(input))
}

// wireTodo is one entry of the TodoWrite plan tool's input.
type wireTodo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// todoTitle summarizes a plan update as progress plus the item in flight:
// "2/5 · Fix the parser", or just "2/5 todos" when nothing is in_progress.
func todoTitle(todos []wireTodo) string {
	if len(todos) == 0 {
		return ""
	}
	done, current := 0, ""
	for _, td := range todos {
		if td.Status == "completed" {
			done++
		}
		if current == "" && td.Status == "in_progress" {
			current = td.Content
		}
	}
	if current != "" {
		return clip(fmt.Sprintf("%d/%d · %s", done, len(todos), current))
	}
	return fmt.Sprintf("%d/%d todos", done, len(todos))
}

// firstString returns the input object's first non-blank string value in
// document order — the last-resort title. json.Unmarshal into a map loses
// key order, so this walks tokens instead; non-string values are skipped.
func firstString(input json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(input))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return ""
	}
	for dec.More() {
		if _, err := dec.Token(); err != nil { // key
			return ""
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return ""
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// clip collapses a value to one line capped at titleCap runes, marking any
// truncation with an ellipsis.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + "…"
	}
	if r := []rune(s); len(r) > titleCap {
		s = string(r[:titleCap-1]) + "…"
	}
	return s
}
