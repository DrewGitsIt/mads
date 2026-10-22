package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"maps"
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
	path, err := p.transcriptPath(ref)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return normalizeTranscript(f)
}

func (p *Provider) transcriptPath(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("transcript: empty session ref")
	}
	matches, err := filepath.Glob(filepath.Join(p.cfg.TranscriptDir, "*", ref+".jsonl"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no transcript for session %s under %s", ref, p.cfg.TranscriptDir)
	}
	return matches[0], nil
}

// transcriptCursor is TranscriptSince's position in a session's JSONL: the
// file (path, and its identity, so a replaced file is noticed), the byte
// offset just past the last line read, and the reader's state at that
// point (seen uuids, peer messages, per-message block counts), so rows
// read incrementally carry exactly the keys a full Transcript gives them.
// A read never changes the cursor it was given.
type transcriptCursor struct {
	path   string
	file   os.FileInfo
	offset int64
	state  *transcriptState
}

// TranscriptSince implements provider.IncrementalTranscriptReader: the rows
// written after cursor (nil: the start). Only whole lines are read; a line
// claude is still writing waits for the next call. ErrCursorStale when the
// file shrank below the cursor or is no longer the file it was (claude's
// JSONL is append-only, so either means it was replaced).
//
// One hold-back: a compact_boundary whose summary line hasn't been read yet
// stays unread, because a full read folds the summary into the boundary's
// row (normalizeTranscript). Claude writes the two together, so the window
// in which the chained rows lag a full read for it is a write's width.
func (p *Provider) TranscriptSince(_ context.Context, ref string, cursor provider.TranscriptCursor) ([]provider.Message, provider.TranscriptCursor, error) {
	c, _ := cursor.(*transcriptCursor)
	if cursor != nil && c == nil {
		return nil, cursor, provider.ErrCursorStale // not one of ours
	}
	path := ""
	if c != nil {
		path = c.path
	} else {
		var err error
		if path, err = p.transcriptPath(ref); err != nil {
			return nil, cursor, err
		}
	}
	f, err := os.Open(path)
	if err != nil {
		if c != nil && errors.Is(err, fs.ErrNotExist) {
			return nil, cursor, provider.ErrCursorStale
		}
		return nil, cursor, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, cursor, err
	}
	var offset int64
	st := newTranscriptState(false)
	st.thinking = true // the keyed form (provider.KeyedTranscriptReader)
	if c != nil {
		if !os.SameFile(c.file, info) || info.Size() < c.offset {
			return nil, cursor, provider.ErrCursorStale
		}
		if info.Size() == c.offset {
			return nil, c, nil
		}
		offset, st = c.offset, c.state.clone()
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, cursor, err
	}

	var msgs []provider.Message
	// held: the state, offset and row count just before the latest
	// compact_boundary line, to rewind to if its row ends the read.
	var held *transcriptCursor
	heldRows := 0
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break // EOF, or a line claude is still writing: read it next time
		}
		if bytes.Contains(line, []byte(`"compact_boundary"`)) {
			held, heldRows = &transcriptCursor{offset: offset, state: st.clone()}, len(msgs)
		}
		msgs = st.add(line, msgs)
		offset += int64(len(line))
	}
	if held != nil && len(msgs) == heldRows+1 && msgs[heldRows].Role == "compact" && msgs[heldRows].Text == "" {
		msgs, offset, st = msgs[:heldRows], held.offset, held.state
	}
	return msgs, &transcriptCursor{path: path, file: info, offset: offset, state: st}, nil
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
	// (peerAttachment), and so are the human's own mid-turn send
	// (queuedPrompt) and a background task's mid-turn notification
	// (taskNotice).
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
	// SourceUUID is the uuid of the stdin line the input came in on: for
	// a send through SendKeyed, mads's id (keys.go).
	SourceUUID string `json:"source_uuid"`
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
// attachment. Mid-turn task notifications (commandMode
// "task-notification") are taskNotice's.
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
	notificationSummary   = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	notificationStatus    = regexp.MustCompile(`<status>(.*?)</status>`)
	notificationToolUseID = regexp.MustCompile(`<tool-use-id>(.*?)</tool-use-id>`)
)

// taskNotice is the <task-notification> block a queued_command attachment
// carries: claude's record of a background task's notice that arrived
// while a turn ran (idle, the same block is a user line with origin
// "task-notification"). Empty for any other attachment.
func taskNotice(a *transcriptAttachment) string {
	if a == nil || a.Type != "queued_command" || a.CommandMode != "task-notification" {
		return ""
	}
	return string(a.Prompt)
}

// messageText is a user line's text blocks, concatenated.
func messageText(m *wireMessage) string {
	var raw string
	for _, b := range m.blocks() {
		if b.Type == "text" {
			raw += b.Text
		}
	}
	return raw
}

// noticeIdentity says which <task-notification> blocks are one notice, so
// a notice written in both forms (attachment and user line) renders once.
// The task id alone is not one: a subagent that is messaged again, or
// that stops with work of its own still running, notifies again under the
// same task id (one local transcript holds 42 notices for one task: 9
// tool-use ids, and many notices with none). With a <tool-use-id> the
// pair (task id, tool-use id) is the identity. Without one (a subagent's
// interim re-notification, a Monitor event, an artifact-watch notice with
// no task id at all) the block's whole text is: those notices differ in
// their <result> or <event>, so only a byte-identical block is taken for
// the same notice.
// (The local corpus on 2026-10-22: 615 user-line and 196 attachment
// notices, 207 of them without a tool-use id; 14 task ids notified in both
// forms, but no pair and no whole text repeats, so on real lines this
// dedupe drops nothing yet.)
func noticeIdentity(raw string) string {
	if tu := notificationToolUseID.FindStringSubmatch(raw); tu != nil {
		var task string
		if t := notificationTaskID.FindStringSubmatch(raw); t != nil {
			task = t[1]
		}
		return task + "\x00" + tu[1]
	}
	return "\x00\x00" + strings.TrimSpace(raw)
}

// taskNotificationText is the one line worth showing from a
// <task-notification> block: its <summary> (the CLI's own sentence, e.g.
// `Background command "make check" completed (exit code 0)`), unescaped, or
// failing that the bare status.
func taskNotificationText(raw string) string {
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

// KeyedTranscript implements provider.KeyedTranscriptReader: Transcript's
// rows plus a Role "thinking" row for every thinking block claude stored
// with text (showThinkingSummaries on), keyed "<message.id>:<index>" like
// the live EventThinking. Signature-only blocks (the default, no
// summaries) make no row, as they make no live item.
func (p *Provider) KeyedTranscript(_ context.Context, ref string) ([]provider.Message, error) {
	path, err := p.transcriptPath(ref)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return normalizeKeyed(f)
}

// normalizeKeyed is normalizeTranscript with the thinking rows.
func normalizeKeyed(r io.Reader) ([]provider.Message, error) {
	st := newTranscriptState(false)
	st.thinking = true
	return st.readAll(r)
}

// normalizeLines is normalizeTranscript with the choice of chain: the main
// conversation's lines, or (sidechain) a subagent's own file's, which are all
// sidechain lines (DelegateTranscript).
func normalizeLines(r io.Reader, sidechain bool) ([]provider.Message, error) {
	return newTranscriptState(sidechain).readAll(r)
}

// readAll normalizes every line of r.
func (st *transcriptState) readAll(r io.Reader) ([]provider.Message, error) {
	var msgs []provider.Message
	sc := newLineReader(r)
	for sc.Scan() {
		msgs = st.add(sc.line, msgs)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

// transcriptState is what reading a transcript carries from one line to
// the next: the uuids already read (the CLI can append verbatim copies of
// earlier entries, same uuid), the peer messages and task notices already
// rendered, and each assistant message's blocks counted so far (its keys'
// indices, keys.go). TranscriptSince keeps one in its cursor, so an incremental
// read keys and filters exactly as a full one.
type transcriptState struct {
	sidechain bool
	// thinking: emit Role "thinking" rows (the keyed form only; plain
	// Transcript never carries thinking).
	thinking bool
	seen     map[string]struct{}
	peers    peerSeen
	// notices: the noticeIdentity of every task notice rendered.
	notices map[string]bool
	blocks  map[string]int
}

func newTranscriptState(sidechain bool) *transcriptState {
	return &transcriptState{sidechain: sidechain, seen: map[string]struct{}{}, notices: map[string]bool{}, blocks: map[string]int{}}
}

// clone is a deep copy, so a cursor stays valid after a read from it.
func (st *transcriptState) clone() *transcriptState {
	c := &transcriptState{sidechain: st.sidechain, thinking: st.thinking, seen: maps.Clone(st.seen), notices: maps.Clone(st.notices), blocks: maps.Clone(st.blocks)}
	if st.peers.ids != nil {
		c.peers = peerSeen{ids: maps.Clone(st.peers.ids), bodies: maps.Clone(st.peers.bodies)}
	}
	return c
}

// notice appends a task notice's "notification" row, keyed by its line's
// uuid, unless the same notice (noticeIdentity) was already rendered in
// either form.
func (st *transcriptState) notice(raw, uuid string, ts time.Time, msgs []provider.Message) []provider.Message {
	id := noticeIdentity(raw)
	if st.notices[id] {
		return msgs
	}
	st.notices[id] = true
	return append(msgs, provider.Message{Role: "notification", Text: taskNotificationText(raw), Timestamp: ts, Key: uuid, Notice: true})
}

// add normalizes one JSONL line onto msgs.
func (st *transcriptState) add(line []byte, msgs []provider.Message) []provider.Message {
	raw := bytes.TrimSpace(line)
	if len(raw) == 0 {
		return msgs
	}
	var l transcriptLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return msgs // tolerate unknown lines; the format is claude's, not ours
	}
	if l.IsSidechain != st.sidechain {
		return msgs
	}
	if l.UUID != "" {
		if _, dup := st.seen[l.UUID]; dup {
			return msgs
		}
		st.seen[l.UUID] = struct{}{}
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
		return append(msgs, provider.Message{Role: "compact", Compact: marker, Timestamp: ts, Key: l.UUID})
	}
	// A message from another claude process is neither the human nor
	// the CLI: it becomes an "inbound" entry with the bare body. Idle
	// arrival: an isMeta user line with a peer origin. Mid-turn: a
	// queued_command attachment. Unkeyed: its live event carries the
	// replayed line's uuid, which no capture has shown to equal the
	// stored line's (or the attachment's source_uuid).
	if l.Type == "attachment" {
		if raw := taskNotice(l.Attachment); raw != "" {
			// A task notice that arrived mid-turn: the same row as the
			// idle user-line form below.
			return st.notice(raw, l.UUID, ts, msgs)
		}
		if in, body, id := peerAttachment(l.Attachment); in != nil {
			if !st.peers.repeat(in, body, id) {
				msgs = append(msgs, provider.Message{Role: "inbound", Inbound: in, Text: body, Timestamp: ts})
			}
		} else if text := unpaste(queuedPrompt(l.Attachment)); text != "" {
			// A mid-turn send: keyed by the stdin uuid it was sent with.
			id := l.Attachment.SourceUUID
			if id == "" {
				id = l.UUID
			}
			msgs = append(msgs, provider.Message{Role: "user", Text: text, Timestamp: ts, Key: userKey(id)})
		}
		return msgs
	}
	if l.Type == "user" && l.Origin.isPeer() {
		if !st.peers.repeat(l.Origin.inbound(), l.Origin.Body, l.Origin.MsgID) {
			msgs = append(msgs, provider.Message{Role: "inbound", Inbound: l.Origin.inbound(), Text: l.Origin.Body, Timestamp: ts})
		}
		return msgs
	}
	if l.Message == nil {
		return msgs
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
			msgs = append(msgs, provider.Message{Role: "compact", Compact: &provider.CompactMarker{}, Text: summary, Timestamp: ts, Key: l.UUID})
		}
		return msgs
	}
	// A background task's completion notice is the CLI talking to the
	// model, not the human: as a user bubble it showed up as a wall of
	// raw <task-notification> XML in the human's voice (2026-09-30), and
	// it can open a whole unprompted turn with no explanation. It becomes
	// a "notification" entry the conversation draws as a system line.
	// Mid-turn the CLI records the same block as a queued_command
	// attachment instead (above); a notice is rendered once either way.
	if l.Type == "user" && (l.Origin != nil && l.Origin.Kind == "task-notification") {
		return st.notice(messageText(l.Message), l.UUID, ts, msgs)
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
					Key:       b.ToolUseID,
				})
			}
		}
		if len(texts) > 0 {
			text := unpaste(strings.Join(texts, "\n"))
			m := provider.Message{Role: "user", Text: text, Timestamp: ts, Key: userKey(l.UUID)}
			if isInterruptMarker(text) {
				// claude's own marker, not the human's words.
				m.Key, m.Notice = l.UUID, true
			}
			msgs = append(msgs, m)
		}
	case "assistant":
		blocks := l.Message.blocks()
		// Every block counts toward the message's indices, the ones no
		// row is made for (thinking, empty text) included: the live
		// stream numbers them all.
		// A line claude wrote itself ("No response requested.", an API
		// error) is a notice keyed by its uuid, as is a line with no
		// message id at all (no live stream ever keyed one).
		synthetic := l.Message.Model == syntheticModel
		key := func(int) string { return l.UUID }
		if !synthetic && l.Message.ID != "" {
			first := st.blocks[l.Message.ID]
			st.blocks[l.Message.ID] = first + len(blocks)
			key = func(i int) string { return blockKey(l.Message.ID, first+i) }
		}
		for i, b := range blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					msgs = append(msgs, provider.Message{Role: "agent", Text: b.Text, Timestamp: ts, IsError: l.IsAPIErrorMessage, Key: key(i), Notice: synthetic})
				}
			case "thinking":
				if st.thinking && b.Thinking != "" {
					msgs = append(msgs, provider.Message{Role: "thinking", Text: b.Thinking, Timestamp: ts, Key: key(i)})
				}
			case "tool_use":
				msgs = append(msgs, provider.Message{Role: "tool", Tool: newToolUse(b.ID, b.Name, b.Input), Timestamp: ts, Key: b.ID})
			}
		}
	}
	return msgs
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
			return unpaste(strings.Join(texts, "\n")), true
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
