package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// Transcript reads claude-code's on-disk session file
// (<TranscriptDir>/<project>/<ref>.jsonl) and normalizes it. The project
// directory is found by glob — the ref is globally unique, and callers don't
// know claude's cwd-mangling scheme.
func (p *Provider) Transcript(_ context.Context, ref string) ([]provider.Message, error) {
	if ref == "" {
		return nil, fmt.Errorf("transcript: empty session ref")
	}
	matches, err := filepath.Glob(filepath.Join(p.cfg.TranscriptDir, "*", ref+".jsonl"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no transcript for session %s under %s", ref, p.cfg.TranscriptDir)
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return normalizeTranscript(f)
}

// transcriptLine is one JSONL entry in a claude-code project transcript.
// Beyond user/assistant messages the file holds bookkeeping (queue
// operations, attachments, summaries) — all skipped.
type transcriptLine struct {
	Type        string       `json:"type"`
	IsSidechain bool         `json:"isSidechain"`
	Message     *wireMessage `json:"message"`
}

// normalizeTranscript maps transcript JSONL onto the provider-independent
// Message form consumed by synthesis and retro (spec §4, §7): user text,
// agent text, and tool invocations; subagent sidechains and tool results
// are dropped.
func normalizeTranscript(r io.Reader) ([]provider.Message, error) {
	var msgs []provider.Message
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var l transcriptLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			continue // tolerate unknown lines; the format is claude's, not ours
		}
		if l.IsSidechain || l.Message == nil {
			continue
		}
		switch l.Type {
		case "user":
			var texts []string
			for _, b := range l.Message.blocks() {
				if b.Type == "text" && b.Text != "" {
					texts = append(texts, b.Text)
				}
			}
			if len(texts) > 0 {
				msgs = append(msgs, provider.Message{Role: "user", Text: strings.Join(texts, "\n")})
			}
		case "assistant":
			for _, b := range l.Message.blocks() {
				switch b.Type {
				case "text":
					if b.Text != "" {
						msgs = append(msgs, provider.Message{Role: "agent", Text: b.Text})
					}
				case "tool_use":
					msgs = append(msgs, provider.Message{Role: "tool", Tool: &provider.ToolUse{
						Name: b.Name, Input: b.Input, Path: toolPath(b.Input),
					}})
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}
