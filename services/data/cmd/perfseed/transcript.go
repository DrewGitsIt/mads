package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// writeDelegateTranscript writes one delegate's claude-code sidechain file
// (the shape providers/claudecode reads: agentId-tagged user/assistant lines
// with tool_use / tool_result / text / thinking blocks) from the same
// session.subagent_event payloads the log holds for it, so the file and the
// log tell one story.
func writeDelegateTranscript(root, repoDir, ref string, d *seedDelegate, events [][]byte, ats []time.Time) error {
	// Claude Code's project folder: the real path of the cwd with every
	// character but [A-Za-z0-9] replaced by '-' (so "/." becomes "--", as
	// ~/.claude/projects shows for paths like /private/var/.../T/x_y).
	real := repoDir
	if r, err := filepath.EvalSymlinks(repoDir); err == nil {
		real = r
	}
	project := nonAlnum.ReplaceAllString(real, "-")
	dir := filepath.Join(root, project, ref, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	n := 0
	line := func(typ string, at time.Time, message any) {
		n++
		parent := any(nil)
		if n > 1 {
			parent = fmt.Sprintf("u%d", n-1)
		}
		l, _ := json.Marshal(map[string]any{
			"type": typ, "isSidechain": true, "agentId": d.harnessID, "uuid": fmt.Sprintf("u%d", n),
			"parentUuid": parent, "timestamp": at.UTC().Format(time.RFC3339Nano), "message": message,
		})
		b.Write(l)
		b.WriteByte('\n')
	}
	start := time.Now()
	if len(ats) > 0 {
		start = ats[0]
	}
	line("user", start, map[string]any{"role": "user", "content": d.description})
	for i, raw := range events {
		var e contracts.SubagentEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		at := ats[i]
		switch e.Kind {
		case "tool_use":
			if e.Tool == nil {
				continue
			}
			line("assistant", at, map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": e.Tool.ID, "name": e.Tool.Name, "input": e.Tool.Input},
			}})
		case "tool_result":
			id := ""
			if e.Tool != nil {
				id = e.Tool.ID
			}
			line("user", at, map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": id, "content": e.Text},
			}})
		case "text":
			line("assistant", at, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": e.Text}}})
		case "thinking":
			// The harness keeps thinking without its text (signature only).
			line("assistant", at, map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "", "signature": "perf"},
			}})
		}
	}
	meta, _ := json.Marshal(map[string]string{"agentType": d.typ, "description": d.description})
	if err := os.WriteFile(filepath.Join(dir, "agent-"+d.harnessID+".meta.json"), meta, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "agent-"+d.harnessID+".jsonl"), []byte(b.String()), 0o644)
}
