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
	items := make([]item, len(entries))
	for i, e := range entries {
		items[i] = item{entry: e, count: 1}
	}
	var sb strings.Builder
	renderItems(&sb, items)
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

// renderTurns normalizes and renders each turn. Multi-turn renderings get a
// "— turn N —" header per turn (omitted for unknown Number 0); a single
// turn renders bare, so the n=1 prompt body looks exactly like today's.
func renderTurns(turns []Turn) string {
	var sb strings.Builder
	multi := len(turns) > 1
	for _, t := range turns {
		if multi && t.Number > 0 {
			fmt.Fprintf(&sb, "— turn %d —\n", t.Number)
		}
		renderItems(&sb, normalizeTurn(t.Entries))
	}
	return sb.String()
}

// renderItems writes the canonical line format, with a " ×N" suffix on tool
// lines the normalize pass collapsed (count 1 renders with no suffix).
func renderItems(sb *strings.Builder, items []item) {
	for _, it := range items {
		switch it.entry.Kind {
		case KindUser:
			sb.WriteString("user: ")
			sb.WriteString(it.entry.Text)
		case KindAssistant:
			sb.WriteString("assistant: ")
			sb.WriteString(it.entry.Text)
		case KindTool:
			sb.WriteString("tool: ")
			sb.WriteString(it.entry.Tool)
			if it.entry.Path != "" {
				sb.WriteString(" ")
				sb.WriteString(it.entry.Path)
			}
			if it.count > 1 {
				fmt.Fprintf(sb, " ×%d", it.count)
			}
		default:
			continue
		}
		sb.WriteString("\n")
	}
}
