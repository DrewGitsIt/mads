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
	"time"

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
	// Cwd is the session's working directory, stamped on every line —
	// candidateTranscripts's filter, not read by normalizeTranscript.
	Cwd string `json:"cwd"`
	// Timestamp is claude-code's own RFC3339 stamp on every conversation
	// line. Unparseable or absent leaves the zero time.Time.
	Timestamp string `json:"timestamp"`
}

// normalizeTranscript maps transcript JSONL onto the provider-independent
// Message form consumed by synthesis, retro, and the pipeline drill-in feed
// (spec §4, §7): user text, agent text, tool invocations, and tool results
// (Role "tool_result", correlated back to its tool_use via Tool.ID); subagent
// sidechains are dropped.
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
		var ts time.Time
		if l.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
				ts = parsed
			}
		}
		switch l.Type {
		case "user":
			var texts []string
			for _, b := range l.Message.blocks() {
				switch b.Type {
				case "text":
					if b.Text != "" {
						texts = append(texts, b.Text)
					}
				case "tool_result":
					msgs = append(msgs, provider.Message{
						Role:      "tool_result",
						Text:      toolResultText(b.Content),
						Tool:      &provider.ToolUse{ID: b.ToolUseID},
						Timestamp: ts,
						IsError:   b.IsError,
					})
				}
			}
			if len(texts) > 0 {
				msgs = append(msgs, provider.Message{Role: "user", Text: strings.Join(texts, "\n"), Timestamp: ts})
			}
		case "assistant":
			for _, b := range l.Message.blocks() {
				switch b.Type {
				case "text":
					if b.Text != "" {
						msgs = append(msgs, provider.Message{Role: "agent", Text: b.Text, Timestamp: ts})
					}
				case "tool_use":
					msgs = append(msgs, provider.Message{Role: "tool", Tool: &provider.ToolUse{
						ID: b.ID, Name: b.Name, Input: b.Input, Path: toolPath(b.Input),
					}, Timestamp: ts})
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

// candidateTranscripts returns every transcript under dir (a JSONL file
// under dir/*/, mirroring Transcript's glob shape — the path-slug scheme is
// claude's, not ours) modified after spawnedAfter whose content's cwd field
// matches sessionCwd. A small clock-skew margin is subtracted from
// spawnedAfter before comparing: a transcript write racing microseconds
// behind this process's own spawn timestamp can otherwise carry an mtime
// that compares before it.
//
// Ambiguity is the caller's problem — this returns every survivor (0, 1, or
// many); a caller that needs a single answer must narrow further (see
// discoverRefByContent) and decline rather than guess, since a wrong ref
// would silently resume someone else's conversation.
func candidateTranscripts(dir, sessionCwd string, spawnedAfter time.Time) []string {
	margin := spawnedAfter.Add(-2 * time.Second)
	matches, err := filepath.Glob(filepath.Join(dir, "*", "*.jsonl"))
	if err != nil {
		return nil
	}
	var survivors []string
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil || !fi.ModTime().After(margin) {
			continue
		}
		cwd, ok := probeCwd(m)
		if !ok || cwd != sessionCwd {
			continue
		}
		survivors = append(survivors, m)
	}
	return survivors
}

// firstUserText returns the text of the first non-sidechain user-role
// message in the transcript at path, and whether one was found at all.
func firstUserText(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var l transcriptLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			continue
		}
		if l.IsSidechain || l.Type != "user" || l.Message == nil {
			continue
		}
		var texts []string
		for _, b := range l.Message.blocks() {
			if b.Type == "text" && b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n"), true
		}
	}
	return "", false
}

// discoverRefByContent narrows candidateTranscripts's survivors (cwd- and
// spawn-time-filtered already) to the one whose first user-role message is
// exactly firstMessage — SessionLocator's rung 2 (locate.go), a direct
// content scan used when the cheaper history.jsonl index (rung 1) has no
// usable entry. Declines (ok=false) on 0 or 2+ matches, the same
// never-guess rule as candidateTranscripts.
func discoverRefByContent(dir, sessionCwd string, spawnedAfter time.Time, firstMessage string) (ref string, ok bool) {
	var survivors []string
	for _, path := range candidateTranscripts(dir, sessionCwd, spawnedAfter) {
		text, found := firstUserText(path)
		if !found || text != firstMessage {
			continue
		}
		survivors = append(survivors, path)
	}
	if len(survivors) != 1 {
		return "", false
	}
	return refFromPath(survivors[0]), true
}

// probeCwd scans a transcript's lines for the first non-empty cwd field.
func probeCwd(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var l transcriptLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			continue
		}
		if l.Cwd != "" {
			return l.Cwd, true
		}
	}
	return "", false
}

// refFromPath extracts the provider-native ref from a transcript file's
// path: claude names each file <ref>.jsonl.
func refFromPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}
