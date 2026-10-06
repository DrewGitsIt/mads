package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

var _ provider.HistoryScanner = (*Provider)(nil)

// maxImportedTitleRunes bounds a title taken from the user's first message
// (an ai-title is claude's own short phrase and is kept as is).
const maxImportedTitleRunes = 60

// ScanHistory implements provider.HistoryScanner over claude's on-disk
// projects tree: every <TranscriptDir>/<project>/<sessionId>.jsonl is one
// conversation. The glob is exactly one level deep, which is what leaves out
// <session>/subagents/agent-*.jsonl — those belong to their parent.
//
// Skipped: conversations with no real user message; entrypoint "sdk-*" runs
// (other tools' automation, including mads's own spawned agents — a human at
// a terminal is "cli"); conversations a live claude process (LiveSessionsDir)
// still owns, since a fork would race its appends; and any with no cwd on
// any line, which leaves no folder to start from. The folder is the cwd
// stamped on the lines, never decoded from the project directory's name,
// which is lossy.
func (p *Provider) ScanHistory(ctx context.Context) (provider.HistoryScan, error) {
	scan := provider.HistoryScan{LookedFor: []string{p.cfg.TranscriptDir, p.cfg.LiveSessionsDir}}
	if p.cfg.TranscriptDir == "" {
		return scan, nil
	}
	matches, err := filepath.Glob(filepath.Join(p.cfg.TranscriptDir, "*", "*.jsonl"))
	if err != nil {
		return scan, err
	}
	sort.Strings(matches)
	live := p.liveSessionIDs()
	for _, path := range matches {
		if err := ctx.Err(); err != nil {
			return scan, err
		}
		ref := refFromPath(path)
		if live[ref] {
			continue
		}
		conv, ok := scanConversation(path)
		if !ok {
			continue
		}
		conv.Ref = ref
		scan.Conversations = append(scan.Conversations, conv)
	}
	return scan, nil
}

// liveSessionIDs returns the session ids of claude processes that are still
// running, from the <pid>.json registrations. A registration whose process
// is gone is stale and ignored; one without a sessionId (the directory also
// holds unrelated per-pid files) is no registration at all.
func (p *Provider) liveSessionIDs() map[string]bool {
	live := map[string]bool{}
	if p.cfg.LiveSessionsDir == "" {
		return live
	}
	files, _ := filepath.Glob(filepath.Join(p.cfg.LiveSessionsDir, "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var reg struct {
			SessionID string `json:"sessionId"`
			PID       int    `json:"pid"`
		}
		if json.Unmarshal(data, &reg) != nil || reg.SessionID == "" || reg.PID <= 0 {
			continue
		}
		if processAlive(reg.PID) {
			live[reg.SessionID] = true
		}
	}
	return live
}

// processAlive reports whether pid names a live process, via a signal-0
// probe (no signal delivered).
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// scanConversation reads one transcript file front to back and reports the
// conversation it holds, or ok=false when it must not be imported. Lines are
// read with no length cap: a single tool result can be megabytes.
func scanConversation(path string) (conv provider.HistoryConversation, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return conv, false
	}
	defer func() { _ = f.Close() }()

	var (
		firstUser string
		aiTitle   string
		cwd       string
		sdk       bool
		sawEntry  bool
		usage     *provider.Usage
	)
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		raw, err := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(raw))) > 0 {
			var l transcriptLine
			if json.Unmarshal(raw, &l) == nil {
				if l.AITitle != "" {
					aiTitle = l.AITitle // the last one is the current title
				}
				if !l.IsSidechain {
					if cwd == "" {
						cwd = l.Cwd
					}
					if !sawEntry && l.Entrypoint != "" {
						sawEntry = true
						sdk = strings.HasPrefix(l.Entrypoint, "sdk")
					}
					if (l.Type == "user" || l.Type == "assistant") && l.Timestamp != "" {
						if ts, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
							if conv.CreatedAt.IsZero() {
								conv.CreatedAt = ts
							}
							conv.LastActiveAt = ts
						}
					}
					if firstUser == "" && l.Type == "user" {
						firstUser = realUserText(&l)
					}
					if l.Type == "assistant" && l.Message != nil && l.Message.Usage != nil && l.Message.Model != "<synthetic>" {
						usage = historyUsage(l.Message)
					}
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return conv, false
			}
			break
		}
	}
	if sdk || firstUser == "" || cwd == "" {
		return conv, false
	}
	conv.Dir = cwd
	conv.Usage = usage
	conv.Title = strings.TrimSpace(aiTitle)
	if conv.Title == "" {
		conv.Title = titleFromMessage(firstUser)
	}
	return conv, true
}

// realUserText is the text of a user line the human actually typed — not a
// tool result, a harness-injected line (isMeta), a compaction summary, a
// background-task notice, or slash-command bookkeeping. Empty otherwise.
func realUserText(l *transcriptLine) string {
	if l.IsMeta || l.IsCompactSummary || l.Message == nil || (l.Origin != nil && l.Origin.Kind == "task-notification") {
		return ""
	}
	var texts []string
	for _, b := range l.Message.blocks() {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" && !isCommandMarkup(b.Text) {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// historyUsage maps one assistant line's accounting onto the provider-
// independent shape. Each line is a single API call, so its prompt side
// (input + both cache halves) is exactly the context the next turn carries —
// the ContextTokens the resume-compaction trigger trusts.
func historyUsage(m *wireMessage) *provider.Usage {
	u := m.Usage
	return &provider.Usage{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		Model:                    m.Model,
		ContextTokens:            u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
	}
}

// titleFromMessage is the first non-empty line of text, cut to a rail-sized
// title.
func titleFromMessage(text string) string {
	line := ""
	for _, ln := range strings.Split(text, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			line = ln
			break
		}
	}
	if runes := []rune(line); len(runes) > maxImportedTitleRunes {
		line = strings.TrimSpace(string(runes[:maxImportedTitleRunes])) + "…"
	}
	return line
}
