package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
	// UUID identifies the entry. The CLI can append verbatim copies of
	// earlier entries, same UUID, when it compacts (observed on 2.x: 622
	// re-appended lines after a manual /compact), so normalizeTranscript
	// keeps only the first occurrence.
	UUID string `json:"uuid"`
	Type string `json:"type"`
	// Subtype and CompactMetadata are set on the "system" line the CLI
	// writes for each compaction (subtype "compact_boundary").
	Subtype         string           `json:"subtype"`
	CompactMetadata *compactMetadata `json:"compactMetadata"`
	// IsCompactSummary marks the user-role line right after a boundary that
	// holds the summary the agent carries forward ("This session is being
	// continued…") — the CLI's text, not the human's.
	IsCompactSummary bool `json:"isCompactSummary"`
	IsSidechain      bool `json:"isSidechain"`
	// IsMeta marks harness-injected user lines (caveat preambles, scheduled
	// prompt re-invocations, hook payloads) — text the human never typed.
	IsMeta bool `json:"isMeta"`
	// Origin marks a user-role line the CLI wrote itself: kind
	// "task-notification" is the <task-notification> block it feeds the
	// model when a background task finishes.
	Origin  *wireOrigin  `json:"origin"`
	Message *wireMessage `json:"message"`
	// Cwd is the session's working directory, stamped on every line —
	// candidateTranscripts's filter, not read by normalizeTranscript.
	Cwd string `json:"cwd"`
	// Entrypoint is how the CLI was launched: "cli" for a human at a
	// terminal, "sdk-cli" for an SDK/automation run. AITitle is the
	// conversation's generated title, on "ai-title" lines (history.go).
	Entrypoint string `json:"entrypoint"`
	AITitle    string `json:"aiTitle"`
	// Timestamp is claude-code's own RFC3339 stamp on every conversation
	// line. Unparseable or absent leaves the zero time.Time.
	Timestamp string `json:"timestamp"`
}

type compactMetadata struct {
	Trigger    string `json:"trigger"`
	PreTokens  int    `json:"preTokens"`
	PostTokens int    `json:"postTokens"`
	DurationMS int64  `json:"durationMs"`
}

// isCommandMarkup reports whether a user text block is claude CLI
// slash-command bookkeeping rather than typed input: `/compact` and friends
// log `<command-name>…` request lines and `<local-command-stdout>…` output
// lines as user-role text without the isMeta flag (observed on real 2.x
// transcripts; the caveat preamble variant does carry isMeta).
func isCommandMarkup(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "<command-name>") || strings.HasPrefix(t, "<local-command-")
}

var (
	notificationSummary = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	notificationStatus  = regexp.MustCompile(`<status>(.*?)</status>`)
)

// taskNotificationText is the one line worth showing from a
// <task-notification> block: its <summary> (the CLI's own sentence, e.g.
// `Background command "make check" completed (exit code 0)`), unescaped, or
// failing that the bare status.
func taskNotificationText(m *wireMessage) string {
	var raw string
	for _, b := range m.blocks() {
		if b.Type == "text" {
			raw += b.Text
		}
	}
	if sm := notificationSummary.FindStringSubmatch(raw); sm != nil {
		if t := strings.TrimSpace(html.UnescapeString(sm[1])); t != "" {
			return t
		}
	}
	if st := notificationStatus.FindStringSubmatch(raw); st != nil {
		return "Background task " + strings.TrimSpace(st[1])
	}
	return "Background task finished"
}

// normalizeTranscript maps transcript JSONL onto the provider-independent
// Message form consumed by synthesis, retro, and the pipeline drill-in feed
// (spec §4, §7): user text, agent text, tool invocations, and tool results
// (Role "tool_result", correlated back to its tool_use via Tool.ID); subagent
// sidechains are dropped.
func normalizeTranscript(r io.Reader) ([]provider.Message, error) {
	var msgs []provider.Message
	seen := map[string]struct{}{}
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
		if l.IsSidechain {
			continue
		}
		if l.UUID != "" {
			if _, dup := seen[l.UUID]; dup {
				continue
			}
			seen[l.UUID] = struct{}{}
		}
		var ts time.Time
		if l.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
				ts = parsed
			}
		}
		// A compaction becomes one "compact" entry where it happened: the
		// boundary line supplies the metadata, the summary line after it
		// the text. A summary with no boundary before it (older CLIs) still
		// gets its own entry, so it never renders as a user message.
		if l.Type == "system" && l.Subtype == "compact_boundary" {
			marker := &provider.CompactMarker{}
			if md := l.CompactMetadata; md != nil {
				*marker = provider.CompactMarker{Trigger: md.Trigger, PreTokens: md.PreTokens, PostTokens: md.PostTokens, DurationMS: md.DurationMS}
			}
			msgs = append(msgs, provider.Message{Role: "compact", Compact: marker, Timestamp: ts})
			continue
		}
		if l.Message == nil {
			continue
		}
		if l.IsCompactSummary {
			var parts []string
			for _, b := range l.Message.blocks() {
				if b.Type == "text" && b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			summary := strings.Join(parts, "\n")
			if n := len(msgs); n > 0 && msgs[n-1].Role == "compact" && msgs[n-1].Text == "" {
				msgs[n-1].Text = summary
			} else {
				msgs = append(msgs, provider.Message{Role: "compact", Compact: &provider.CompactMarker{}, Text: summary, Timestamp: ts})
			}
			continue
		}
		// A background task's completion notice is the CLI talking to the
		// model, not the human: as a user bubble it showed up as a wall of
		// raw <task-notification> XML in the human's voice (2026-09-30), and
		// it can open a whole unprompted turn with no explanation. It becomes
		// a "notification" entry the conversation draws as a system line.
		if l.Type == "user" && (l.Origin != nil && l.Origin.Kind == "task-notification") {
			msgs = append(msgs, provider.Message{Role: "notification", Text: taskNotificationText(l.Message), Timestamp: ts})
			continue
		}
		switch l.Type {
		case "user":
			var texts []string
			for _, b := range l.Message.blocks() {
				switch b.Type {
				case "text":
					// Harness-injected lines (isMeta) and the CLI's
					// slash-command bookkeeping (<command-name>,
					// <local-command-stdout>) are not something the human
					// typed — rendering them as user bubbles misattributes
					// them. Tool results on such lines still flow below.
					if b.Text != "" && !l.IsMeta && !isCommandMarkup(b.Text) {
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
					msgs = append(msgs, provider.Message{Role: "tool", Tool: newToolUse(b.ID, b.Name, b.Input), Timestamp: ts})
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
