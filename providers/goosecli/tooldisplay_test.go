package goosecli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// The stored side rebuilds the title goose put on the live tool_call. The
// first cases are the live titles in the captures (keys/run.wire.jsonl,
// acp-transcript-1.48.0.jsonl); the rest pin default_tool_title's cuts.
func TestStoredToolDisplay(t *testing.T) {
	long := strings.Repeat("x", 70)
	for _, tc := range []struct {
		name, ext string
		args      string
		kind      provider.ToolKind
		title     string
	}{
		{"shell", "developer", `{"command":"ls"}`, provider.ToolKindExecute, "shell · ls"},
		{"shell", "developer", `{"command":"echo acp-capture-ok"}`, provider.ToolKindExecute, "shell · echo acp-capture-ok"},
		{"todo__todo_write", "todo", `{"content":"- [ ] a"}`, provider.ToolKindThink, "todo: todo write"},
		{"todo__todo_write", "", `{"content":"- [ ] a"}`, provider.ToolKindThink, "todo: todo write"}, // extension from the prefix
		{"shell", "developer", `{"command":"` + long + `"}`, provider.ToolKindExecute, "shell · " + strings.Repeat("x", 54) + "...…"},
		{"shell", "developer", `{"command":"a\nb"}`, provider.ToolKindExecute, "shell · a…"},
		{"shell", "developer", `{"command":"ls\n"}`, provider.ToolKindExecute, "shell · ls"},
		{"shell", "developer", `{"path":"/x","command":"ls"}`, provider.ToolKindExecute, "shell · ls"}, // command first for the shell
		{"text_editor", "developer", `{"command":"view","path":"/a.go"}`, provider.ToolKindRead, "text editor · /a.go"},
		{"mystery__do_it", "", `{"count":3}`, provider.ToolKindOther, "mystery: do it"},
		{"shell", "developer", `{"command":""}`, provider.ToolKindExecute, "shell"},
	} {
		kind, title := storedToolDisplay(tc.name, json.RawMessage(tc.args), tc.ext)
		if kind != tc.kind || title != tc.title {
			t.Errorf("%s %s: got %q %q, want %q %q", tc.name, tc.args, kind, title, tc.kind, tc.title)
		}
	}
}
