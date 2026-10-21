// Package linkwrap is the in-text label MADS puts on every message it inserts
// into a session's conversation as a user turn — a linked session's message,
// a digest, a signal wake, a notice (docs/LINKED-SESSIONS-DESIGN.md, "Message
// wrapper (2026-10-20)"):
//
//	<mads-message kind="digest" from="Pipeline Study" relation="a session you lead" turn="42">
//	…body…
//	</mads-message>
//
// The label lives in the message itself, so it is durable (it is part of the
// provider's own transcript), provider-neutral, and the agent reads who is
// speaking too. The session runner wraps (Wrap) at the one point it hands an
// injected message to the provider; the runner's live transcript and the
// Router's durable one both parse (Expand), so a message is labelled the same
// whether its session is running or asleep. Not retroactive: a message with no
// wrapper is the human's own.
package linkwrap

import (
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Kinds of wrapped message (contracts.Attribution.Kind).
const (
	KindMessage = "message" // a linked session's message (a leader's message_session, a link inject)
	KindDigest  = "digest"  // a per-turn digest of a session this one watches
	KindSignal  = "signal"  // a wake: a watched session needs attention, or a pipeline task finished
	KindNotice  = "notice"  // MADS itself telling the agent something
)

// Relations, always from the recipient's point of view
// (contracts.Attribution.Relation).
const (
	RelationLeader      = "your leader"              // the link's granting side, holding orchestrate, messaging its subject
	RelationMonitoredBy = "a session monitoring you" // the granting side without orchestrate
	RelationLed         = "a session you lead"       // the subject, to a granting side holding orchestrate
	RelationMonitored   = "a session you monitor"    // the subject, to a granting side without orchestrate
	RelationPipelineRun = "your pipeline run"        // the implicit hand-raiser: a session's own run
)

// NoticeSource is the from attribute of a notice MADS itself sends.
const NoticeSource = "mads"

const (
	openPrefix = "<mads-message"
	closeTag   = "</mads-message>"
)

// Wrap renders a's label around body. An empty Kind is a "message"; an empty
// SourceName on a notice is "mads". IDs are deliberately left out: the agent
// has no use for them, and the label must read the same after a restart.
func Wrap(body string, a contracts.Attribution) string {
	kind := a.Kind
	if kind == "" {
		kind = KindMessage
	}
	from := a.SourceName
	if from == "" && kind == KindNotice {
		from = NoticeSource
	}
	var b strings.Builder
	b.WriteString(openPrefix)
	attr(&b, "kind", kind)
	attr(&b, "from", from)
	if a.Relation != "" {
		attr(&b, "relation", a.Relation)
	}
	if a.Turn > 0 {
		attr(&b, "turn", strconv.Itoa(a.Turn))
	}
	b.WriteString(">\n")
	b.WriteString(escapeTags(body))
	b.WriteString("\n")
	b.WriteString(closeTag)
	return b.String()
}

// The body's own tags are escaped so a sender can't close its block and
// open a forged one (say, a notice "from mads") inside its own message;
// Parse reverses it. Bodies quoting a wrapped message read back unchanged.
var (
	tagEscaper   = strings.NewReplacer(openPrefix, "&lt;mads-message", "</mads-message", "&lt;/mads-message")
	tagUnescaper = strings.NewReplacer("&lt;mads-message", openPrefix, "&lt;/mads-message", "</mads-message")
)

func escapeTags(body string) string   { return tagEscaper.Replace(body) }
func unescapeTags(body string) string { return tagUnescaper.Replace(body) }

func attr(b *strings.Builder, name, value string) {
	b.WriteString(" ")
	b.WriteString(name)
	b.WriteString(`="`)
	b.WriteString(html.EscapeString(value))
	b.WriteString(`"`)
}

// Block is one wrapped message found in a user turn's text.
type Block struct {
	Attribution contracts.Attribution
	Body        string
}

// Segment is a piece of a parsed message: a wrapped Block, or (Block nil)
// text ahead of a block, outside any wrapper — the re-root note the runner
// prepends to the first turn it sends after a re-root.
type Segment struct {
	Block *Block
	Text  string
}

// openRe matches an opening tag at the start of a line: kind is required,
// the rest optional, attribute values double-quoted (Wrap escapes quotes).
var openRe = regexp.MustCompile(`(?m)^<mads-message((?:\s+[a-z]+="[^"]*")*)\s*>`)

var attrRe = regexp.MustCompile(`([a-z]+)="([^"]*)"`)

// Parse splits text into segments when it holds one or more complete
// wrapper blocks; ok is false — leave the message as it is — when it holds
// none, or a block is malformed (no kind, never closed).
//
// A body may itself contain the tags (a digest quoting a wrapped message, an
// agent reply describing the format), so a block ends at the OUTERMOST close
// tag: one on its own line that is followed only by whitespace and then
// either the end of the text or the next block's opening tag. Among those,
// the first that leaves the tags inside the body balanced wins.
func Parse(text string) (segs []Segment, ok bool) {
	rest := text
	for {
		loc := openRe.FindStringSubmatchIndex(rest)
		if loc == nil {
			break
		}
		if pre := strings.TrimSpace(rest[:loc[0]]); pre != "" {
			segs = append(segs, Segment{Text: pre})
		}
		a, good := parseAttrs(rest[loc[2]:loc[3]])
		if !good {
			return nil, false
		}
		bodyStart := loc[1]
		end := closeAt(rest, bodyStart)
		if end < 0 {
			return nil, false
		}
		body := strings.TrimPrefix(rest[bodyStart:end], "\n")
		body = unescapeTags(strings.TrimSuffix(body, "\n"))
		segs = append(segs, Segment{Block: &Block{Attribution: a, Body: body}})
		rest = rest[end+len(closeTag):]
	}
	hasBlock := false
	for _, s := range segs {
		if s.Block != nil {
			hasBlock = true
		}
	}
	if !hasBlock {
		return nil, false
	}
	// Nothing MADS writes trails the last block; if something does, it
	// stays with the block it follows rather than posing as a turn.
	if tail := strings.TrimSpace(rest); tail != "" {
		last := segs[len(segs)-1].Block
		last.Body += "\n\n" + tail
	}
	return segs, true
}

// closeAt finds the index of the close tag ending the block whose body
// starts at from, or -1.
func closeAt(s string, from int) int {
	first := -1
	for i := from; i < len(s); {
		j := strings.Index(s[i:], closeTag)
		if j < 0 {
			break
		}
		at := i + j
		i = at + len(closeTag)
		if at > 0 && s[at-1] != '\n' {
			continue
		}
		after := strings.TrimLeft(s[at+len(closeTag):], " \t\r\n")
		if after != "" {
			if loc := openRe.FindStringIndex(after); loc == nil || loc[0] != 0 {
				continue
			}
		}
		body := s[from:at]
		if len(openRe.FindAllStringIndex(body, -1)) == countLineCloses(body) {
			return at
		}
		if first < 0 {
			first = at
		}
	}
	return first
}

// countLineCloses counts close tags that start a line.
func countLineCloses(s string) int {
	n := 0
	for i := 0; ; {
		j := strings.Index(s[i:], closeTag)
		if j < 0 {
			return n
		}
		at := i + j
		if at == 0 || s[at-1] == '\n' {
			n++
		}
		i = at + len(closeTag)
	}
}

func parseAttrs(raw string) (contracts.Attribution, bool) {
	var a contracts.Attribution
	for _, m := range attrRe.FindAllStringSubmatch(raw, -1) {
		v := html.UnescapeString(m[2])
		switch m[1] {
		case "kind":
			a.Kind = v
		case "from":
			a.SourceName = v
		case "relation":
			a.Relation = v
		case "turn":
			a.Turn, _ = strconv.Atoi(v)
		}
	}
	if a.Kind == "" {
		return a, false
	}
	a.DigestAttributed = a.Kind != KindMessage
	return a, true
}

// Expand labels a transcript from its wrappers: every user-role message
// holding complete wrapper blocks becomes one message per block, carrying the
// block's Attribution and its body as Text. Text MADS put outside the
// wrappers (the re-root note) becomes a notice from mads of its own, ahead of
// the block it preceded. Every other message passes through untouched —
// including any Attribution it already carries.
func Expand(msgs []contracts.TranscriptMessage) []contracts.TranscriptMessage {
	var out []contracts.TranscriptMessage
	for i, m := range msgs {
		var segs []Segment
		ok := false
		if m.Role == "user" && m.Attribution == nil {
			segs, ok = Parse(m.Text)
		}
		if !ok {
			if out != nil {
				out = append(out, m)
			}
			continue
		}
		if out == nil {
			out = append(make([]contracts.TranscriptMessage, 0, len(msgs)+len(segs)), msgs[:i]...)
		}
		for _, s := range segs {
			tm := m
			if s.Block != nil {
				a := s.Block.Attribution
				tm.Attribution = &a
				tm.Text = s.Block.Body
			} else {
				tm.Attribution = &contracts.Attribution{Kind: KindNotice, SourceName: NoticeSource, DigestAttributed: true}
				tm.Text = s.Text
			}
			out = append(out, tm)
		}
	}
	if out == nil {
		return msgs
	}
	return out
}

// SameSend reports whether user message cur continues the send prev began:
// both came out of one wrapped message Expand split up (a coalesced digest
// run, a re-root note ahead of its block), with nothing in between. A turn
// counter must treat such a pair as one turn, since the session was sent
// one message.
func SameSend(prev, cur contracts.TranscriptMessage) bool {
	return prev.Role == "user" && cur.Role == "user" && prev.Attribution != nil && cur.Attribution != nil
}

// SplitTurns segments a transcript into turns: each user-role message — the
// human's, or one MADS injected — starts a turn that runs until the next
// one, except one that continues the same send (SameSend). Anything before
// the first user message (a fork's inherited head, an opening notification)
// folds into turn 1 rather than becoming a turn of its own, so the turn
// count is the number of messages the session was sent. A transcript with
// entries but no user message is one turn; an empty one is none. This is
// get_session_turns' turn model, and what a digest's turn attribute counts.
func SplitTurns(msgs []contracts.TranscriptMessage) [][]contracts.TranscriptMessage {
	var turns [][]contracts.TranscriptMessage
	sawUser := false
	for i, m := range msgs {
		if m.Role == "user" {
			if len(turns) == 0 || sawUser && !(i > 0 && SameSend(msgs[i-1], m)) {
				turns = append(turns, nil)
			}
			sawUser = true
		} else if len(turns) == 0 {
			turns = append(turns, nil)
		}
		turns[len(turns)-1] = append(turns[len(turns)-1], m)
	}
	return turns
}

// ToSubject is how link l's granting session reads to its subject, the
// recipient of a message sent over l.
func ToSubject(l contracts.Link) string {
	if l.HasGrant(contracts.GrantOrchestrate) {
		return RelationLeader
	}
	return RelationMonitoredBy
}

// ToGranting is how link l's subject reads to its granting session, the
// recipient of a digest or signal pushed over l.
func ToGranting(l contracts.Link) string {
	switch {
	case l.Implicit:
		return RelationPipelineRun
	case l.HasGrant(contracts.GrantOrchestrate):
		return RelationLed
	default:
		return RelationMonitored
	}
}
