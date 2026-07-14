package synth

import "strings"

// deltaEntry is one unit of accumulated transcript delta since the last
// synthesis fire (spec §4): text chunks verbatim, tool uses reduced to
// {name, path} — raw tool input/output never leaves the session process.
type deltaEntry struct {
	Kind string // "user", "assistant", "tool"
	Text string
	Tool string
	Path string
}

// renderDelta renders the accumulated delta as the Fast Model prompt body.
func renderDelta(buf []deltaEntry) string {
	var sb strings.Builder
	for _, d := range buf {
		switch d.Kind {
		case "user":
			sb.WriteString("user: ")
			sb.WriteString(d.Text)
			sb.WriteString("\n")
		case "assistant":
			sb.WriteString("assistant: ")
			sb.WriteString(d.Text)
			sb.WriteString("\n")
		case "tool":
			sb.WriteString("tool: ")
			sb.WriteString(d.Tool)
			if d.Path != "" {
				sb.WriteString(" ")
				sb.WriteString(d.Path)
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
