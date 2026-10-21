package goosecli

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/drewkidwell/mads/providers/acprpc"
	"github.com/drewkidwell/mads/providers/provider"
)

// storedToolDisplay is the display kind and title a stored tool call
// streamed with (stored_tool_display): goose keeps a call's name, arguments
// and extension (`_meta.goose_extension`), not the ACP title, so the reader
// rebuilds both the way the live side got them. The kind is the engine's
// own ToolClassTable lookup on the _meta identity goose puts on the wire
// (toolName is the raw name, extensionName the stored extension or the
// name's "<ext>__" prefix); the title is goose's default_tool_title
// (crates/goose/src/acp/server/tool_calls/conversion.rs, v1.50.1). goose
// only puts an LLM-generated title there for a client that asks for label
// enrichment, which mads doesn't.
func storedToolDisplay(name string, args json.RawMessage, extension string) (provider.ToolKind, string) {
	if extension == "" {
		if ext, _, ok := strings.Cut(name, "__"); ok {
			extension = ext
		}
	}
	class := acprpc.ClassFromTable(gooseToolClassTable, extension, name, args)
	return acprpc.ToolKindFor(class), gooseToolTitle(name, args)
}

// goose's format_tool_name: "<ext>: <tool>" for a prefixed name, underscores
// as spaces.
func gooseToolName(name string) string {
	if ext, tool, ok := strings.Cut(name, "__"); ok {
		return strings.ReplaceAll(ext, "_", " ") + ": " + strings.ReplaceAll(tool, "_", " ")
	}
	return strings.ReplaceAll(name, "_", " ")
}

// gooseToolTitle is goose's default_tool_title: the formatted name, then
// " · " and the first non-empty of a few argument keys (command first for
// the shell), first line only, cut at 57 characters with "…".
func gooseToolTitle(name string, args json.RawMessage) string {
	base := gooseToolName(name)
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) != nil || obj == nil {
		return base
	}
	keys := []string{"path", "file", "command", "query", "url", "uri", "name", "pattern", "source"}
	if name == "developer__shell" || name == "shell" {
		keys = []string{"command", "path", "file", "query", "url", "uri", "name", "pattern", "source"}
	}
	for _, k := range keys {
		raw, ok := obj[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			s = compactJSON(raw) // serde's to_string of a non-string value
		}
		if s == "" {
			continue
		}
		// Rust's str::lines: a trailing newline ends the last line rather
		// than starting another, and "\r\n" counts as one.
		first, rest, _ := strings.Cut(s, "\n")
		first = strings.TrimSuffix(first, "\r")
		if len(first) > 60 || rest != "" {
			return base + " · " + safeTruncate(first, 57) + "…"
		}
		return base + " · " + first
	}
	return base
}

// safeTruncate is goose's utils::safe_truncate: up to max characters, else
// the first max-3 and "...".
func safeTruncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-3]) + "..."
}

func compactJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}
