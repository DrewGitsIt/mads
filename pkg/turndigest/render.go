package turndigest

import (
	"fmt"
	"strings"
)

// Render is the canonical line format ("user: …", "assistant: …",
// "tool: Name Path"), moved verbatim from the session runner's
// renderAndReset / synth's renderDelta: same bytes for the same entries, so
// the session.turn_ended wire payload is unchanged. Entries of unknown Kind
// are skipped, as they always were.
func Render(entries []Entry) string {
	var sb strings.Builder
	for _, e := range entries {
		switch e.Kind {
		case KindUser:
			sb.WriteString("user: ")
			sb.WriteString(e.Text)
		case KindAssistant:
			sb.WriteString("assistant: ")
			sb.WriteString(e.Text)
		case KindTool:
			sb.WriteString("tool: ")
			sb.WriteString(e.Tool)
			if e.Path != "" {
				sb.WriteString(" ")
				sb.WriteString(e.Path)
			}
		default:
			continue
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// Parse is Render's inverse: lines starting with a known "<kind>: " prefix
// open an entry; any other line is a continuation of the previous entry's
// Text (joined with "\n"). Best-effort by design — entry text that itself
// contains a line starting with a prefix splits into two entries, which is
// acceptable for digests. A leading line with no prefix opens an assistant
// entry so no text is dropped.
func Parse(delta string) []Entry {
	var entries []Entry
	lines := strings.Split(delta, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // Render always ends with "\n"
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "user: "):
			entries = append(entries, Entry{Kind: KindUser, Text: line[len("user: "):]})
		case strings.HasPrefix(line, "assistant: "):
			entries = append(entries, Entry{Kind: KindAssistant, Text: line[len("assistant: "):]})
		case strings.HasPrefix(line, "tool: "):
			tool, path, _ := strings.Cut(line[len("tool: "):], " ")
			entries = append(entries, Entry{Kind: KindTool, Tool: tool, Path: path})
		default:
			if len(entries) == 0 {
				entries = append(entries, Entry{Kind: KindAssistant, Text: line})
				continue
			}
			last := &entries[len(entries)-1]
			last.Text += "\n" + line
		}
	}
	return entries
}

// renderTurns builds the digest prompt body: each turn normalized
// (normalizeTurn) and rendered as its user lines, a "tools:" tally, then the
// final "assistant:" reply. Multi-turn renderings get a "— turn N —" header
// per turn (omitted for unknown Number 0); a single turn renders bare. When
// the whole exceeds maxPromptBytes the oldest turns drop first, noted at
// the top; the newest turn always survives, since each of its entries is
// already capped.
func renderTurns(turns []Turn) string {
	multi := len(turns) > 1
	blocks := make([]string, len(turns))
	size := 0
	for i, t := range turns {
		blocks[i] = renderTurn(t, multi)
		size += len(blocks[i])
	}
	dropped := 0
	for dropped < len(blocks)-1 && size > maxPromptBytes {
		size -= len(blocks[dropped])
		dropped++
	}
	var sb strings.Builder
	if dropped > 0 {
		noun := "turns"
		if dropped == 1 {
			noun = "turn"
		}
		fmt.Fprintf(&sb, "[%d earlier %s omitted]\n", dropped, noun)
	}
	for _, b := range blocks[dropped:] {
		sb.WriteString(b)
	}
	return sb.String()
}

func renderTurn(t Turn, multi bool) string {
	ct := normalizeTurn(t.Entries)
	if len(ct.users) == 0 && ct.tools == "" && ct.reply == "" {
		return ""
	}
	var sb strings.Builder
	if multi && t.Number > 0 {
		fmt.Fprintf(&sb, "— turn %d —\n", t.Number)
	}
	for _, u := range ct.users {
		sb.WriteString("user: " + u + "\n")
	}
	if ct.tools != "" {
		sb.WriteString("tools: " + ct.tools + "\n")
	}
	if ct.reply != "" {
		sb.WriteString("assistant: " + ct.reply + "\n")
	}
	return sb.String()
}
