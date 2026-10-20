package claudecode

import (
	"bufio"
	"bytes"
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

// restoredCostTotal is the running cost a `claude --resume ref` process
// starts from: the totalCostUSD of the last "cost-state" line in ref's
// transcript, which the CLI appends as each process ends and reads back on
// resume. Probed on 2.1.285 (2026-10-13): a one-turn process reported
// total_cost_usd 0.0183063 and wrote a cost-state with that figure; a second
// process on --resume reported 0.0208454 for its own one turn — the restored
// 0.0183063 plus that turn's ~0.0025 — and wrote a cost-state of 0.0208454.
// Reading the same line the CLI restores from keeps the baseline in step
// with it rather than guessing. 0 when there is no transcript or no
// cost-state line (a process that never ended cleanly writes none — and
// then the CLI has nothing to restore either).
func (p *Provider) restoredCostTotal(ref string) float64 {
	if ref == "" || p.cfg.TranscriptDir == "" {
		return 0
	}
	matches, err := filepath.Glob(filepath.Join(p.cfg.TranscriptDir, "*", ref+".jsonl"))
	if err != nil || len(matches) == 0 {
		return 0
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	return lastCostState(f, ref)
}

// lastCostState scans transcript JSONL for the last cost-state line written
// for ref and returns its totalCostUSD.
func lastCostState(r io.Reader, ref string) float64 {
	marker := []byte(`"cost-state"`)
	total := 0.0
	lr := newLineReader(r)
	for lr.Scan() {
		if !bytes.Contains(lr.line, marker) {
			continue
		}
		var ln struct {
			Type         string  `json:"type"`
			SessionID    string  `json:"sessionId"`
			TotalCostUSD float64 `json:"totalCostUSD"`
		}
		if json.Unmarshal(lr.line, &ln) != nil || ln.Type != "cost-state" {
			continue
		}
		if ln.SessionID != "" && ln.SessionID != ref {
			continue
		}
		total = ln.TotalCostUSD
	}
	return total
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
	// IsAPIErrorMessage marks the assistant line the CLI writes itself when
	// a request never reached the model or failed outright (model
	// "<synthetic>": "Login expired · Please run /login", "API Error:
	// Connection refused…") — a failed delivery, not the agent's reply.
	IsAPIErrorMessage bool `json:"isApiErrorMessage"`
	// Origin marks a user-role line the CLI wrote itself: kind
	// "task-notification" is the <task-notification> block it feeds the
	// model when a background task finishes.
	// Kind "peer" is a cross-session message from another claude process
	// that arrived while the session was idle (isMeta, content the CLI's
	// wrapper; the origin carries the sender and the bare body).
	Origin  *wireOrigin  `json:"origin"`
	Message *wireMessage `json:"message"`
	// Attachment is an "attachment" line's payload. Only the queued_command
	// kind is read: a peer message that arrived mid-turn is recorded as one
	// (peerAttachment), and so is the human's own mid-turn send
	// (queuedPrompt).
	Attachment *transcriptAttachment `json:"attachment"`
	// Cwd is the session's working directory, stamped on every line —
	// candidateTranscripts's filter, not read by normalizeTranscript.
	Cwd string `json:"cwd"`
	// Entrypoint is how the CLI was launched: "cli" for a human at a
	// terminal, "sdk-cli" for an SDK/automation run. AITitle is the
	// conversation's generated title, on "ai-title" lines (history.go).
	Entrypoint string `json:"entrypoint"`
	// PermissionMode is the mode the CLI was in when it wrote the line:
	// stamped on every user line, and alone on the "permission-mode" lines
	// it writes whenever the mode changes ({"type":"permission-mode",
	// "permissionMode":"auto",…}). history.go's import reads the latest.
	PermissionMode string `json:"permissionMode"`
	AITitle        string `json:"aiTitle"`
	// Timestamp is claude-code's own RFC3339 stamp on every conversation
	// line. Unparseable or absent leaves the zero time.Time.
	Timestamp string `json:"timestamp"`
}

// transcriptAttachment is an "attachment" line's payload. A queued_command's
// Prompt is input the CLI took in while a turn ran; for a peer message it is
// the <cross-session-message …>body</cross-session-message> block, and
// Origin (when the CLI wrote one) the same peer origin an idle arrival
// carries. CommandMode says whose input it was: "prompt" (the human or a
// peer) or "task-notification".
type transcriptAttachment struct {
	Type        string      `json:"type"`
	CommandMode string      `json:"commandMode"`
	Prompt      promptText  `json:"prompt"`
	Origin      *wireOrigin `json:"origin"`
}

// promptText is a queued_command's prompt. claude writes it as a bare string
// for peer messages and task notifications, but as an array of content
// blocks for the human's own mid-turn send (2.1.285,
// testdata/transcript_queued.jsonl). As a plain string field the array
// failed the whole line's decode, so the line was skipped. Only text blocks
// contribute, as with toolResultText.
type promptText string

func (p *promptText) UnmarshalJSON(raw []byte) error {
	*p = promptText(toolResultText(raw))
	return nil
}

// queuedPrompt is the text of the human's own mid-turn send: a queued_command
// in "prompt" mode that isn't a peer message (peerAttachment reads those).
// claude records it only as this attachment, never as a user line, so
// skipping it lost the message from the conversation. Empty for any other
// attachment.
//
// TODO(task-notification dedupe): mid-turn task notifications
// (commandMode "task-notification", 199 of the 222 queued_command lines in
// the local corpus on 2026-10-20) are still skipped here. 14 of their 112
// task ids were also delivered as a user line, which already renders as a
// "notification" entry, so showing the attachment form needs a dedupe by
// task id first.
func queuedPrompt(a *transcriptAttachment) string {
	if a == nil || a.Type != "queued_command" || a.CommandMode != "prompt" {
		return ""
	}
	text := string(a.Prompt)
	if strings.TrimSpace(text) == "" || isCommandMarkup(text) {
		return ""
	}
	return text
}

var (
	crossSessionTag  = regexp.MustCompile(`(?s)^\s*<cross-session-message\b([^>]*)>\n?(.*?)\n?</cross-session-message>`)
	crossSessionAttr = regexp.MustCompile(`([a-z-]+)="([^"]*)"`)
)

// peerAttachment reads a mid-turn peer message out of a queued_command
// attachment: from its origin when the CLI wrote one, otherwise from the
// <cross-session-message> block's from/from-name attributes and body. nil
// for any other attachment.
func peerAttachment(a *transcriptAttachment) (in *provider.Inbound, body, msgID string) {
	if a == nil || a.Type != "queued_command" {
		return nil, "", ""
	}
	if a.Origin.isPeer() {
		return a.Origin.inbound(), a.Origin.Body, a.Origin.MsgID
	}
	m := crossSessionTag.FindStringSubmatch(string(a.Prompt))
	if m == nil {
		return nil, "", ""
	}
	in = &provider.Inbound{Channel: peerChannel}
	for _, kv := range crossSessionAttr.FindAllStringSubmatch(m[1], -1) {
		switch kv[1] {
		case "from":
			in.From = html.UnescapeString(kv[2])
		case "from-name":
			in.FromName = html.UnescapeString(kv[2])
		}
	}
	return in, m[2], ""
}

// peerSeen dedupes peer messages across the shapes one can be recorded in,
// so a message written both ways renders once. Two records that both carry
// a msg_id are the same message only if the ids match — a sender can send
// the same words twice. A record without one (a queued_command the CLI
// wrote no origin for) can only be matched on sender and body.
type peerSeen struct {
	ids map[string]bool
	// bodies maps sender+body to whether any record of it lacked a msg_id.
	bodies map[string]bool
}

// repeat reports whether this message was already rendered, and marks it.
func (p *peerSeen) repeat(in *provider.Inbound, body, msgID string) bool {
	if p.ids == nil {
		p.ids, p.bodies = map[string]bool{}, map[string]bool{}
	}
	key := in.From + "\x00" + strings.TrimSpace(body)
	idless, bodySeen := p.bodies[key]
	var dup bool
	if msgID == "" {
		dup = bodySeen
	} else {
		dup = p.ids[msgID] || idless
		p.ids[msgID] = true
	}
	p.bodies[key] = idless || msgID == ""
	return dup
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

// lineReader walks a transcript's JSONL lines with no length cap, with
// bufio.Scanner's Scan/Text/Err shape. claude writes a whole message as one
// line, base64 image blocks included — a pasted screenshot or an image the
// Read tool loaded can run to megabytes — and a Scanner's fixed token cap
// failed the entire transcript on that one line (bufio.ErrTooLong), so the
// conversation wouldn't load at all.
type lineReader struct {
	r    *bufio.Reader
	line []byte
	err  error
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(r, 64*1024)}
}

func (l *lineReader) Scan() bool {
	if l.err != nil {
		return false
	}
	line, err := l.r.ReadBytes('\n')
	if err != nil {
		l.err = err
		if err != io.EOF || len(line) == 0 {
			return false
		}
	}
	l.line = line
	return true
}

func (l *lineReader) Text() string { return string(l.line) }

// Err is nil at a clean end of input, like bufio.Scanner's.
func (l *lineReader) Err() error {
	if l.err == io.EOF {
		return nil
	}
	return l.err
}

// normalizeTranscript maps transcript JSONL onto the provider-independent
// Message form consumed by synthesis, retro, and the pipeline drill-in feed
// (spec §4, §7): user text, agent text, tool invocations, and tool results
// (Role "tool_result", correlated back to its tool_use via Tool.ID); subagent
// sidechains are dropped.
func normalizeTranscript(r io.Reader) ([]provider.Message, error) {
	return normalizeLines(r, false)
}

// normalizeLines is normalizeTranscript with the choice of chain: the main
// conversation's lines, or (sidechain) a subagent's own file's, which are all
// sidechain lines (DelegateTranscript).
func normalizeLines(r io.Reader, sidechain bool) ([]provider.Message, error) {
	var msgs []provider.Message
	seen := map[string]struct{}{}
	var peers peerSeen
	sc := newLineReader(r)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var l transcriptLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			continue // tolerate unknown lines; the format is claude's, not ours
		}
		if l.IsSidechain != sidechain {
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
		// A message from another claude process is neither the human nor
		// the CLI: it becomes an "inbound" entry with the bare body. Idle
		// arrival: an isMeta user line with a peer origin. Mid-turn: a
		// queued_command attachment.
		if l.Type == "attachment" {
			if in, body, id := peerAttachment(l.Attachment); in != nil {
				if !peers.repeat(in, body, id) {
					msgs = append(msgs, provider.Message{Role: "inbound", Inbound: in, Text: body, Timestamp: ts})
				}
			} else if text := queuedPrompt(l.Attachment); text != "" {
				msgs = append(msgs, provider.Message{Role: "user", Text: text, Timestamp: ts})
			}
			continue
		}
		if l.Type == "user" && l.Origin.isPeer() {
			if !peers.repeat(l.Origin.inbound(), l.Origin.Body, l.Origin.MsgID) {
				msgs = append(msgs, provider.Message{Role: "inbound", Inbound: l.Origin.inbound(), Text: l.Origin.Body, Timestamp: ts})
			}
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
						msgs = append(msgs, provider.Message{Role: "agent", Text: b.Text, Timestamp: ts, IsError: l.IsAPIErrorMessage})
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
	sc := newLineReader(f)
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
	sc := newLineReader(f)
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
