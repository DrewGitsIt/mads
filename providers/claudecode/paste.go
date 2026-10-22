package claudecode

import (
	"regexp"
	"strings"
)

// A paste of more than a couple of lines is stored by claude-code's
// terminal UI wrapped in a tag pair that names it, around the pasted text
// exactly (2.1.285, captured 2026-10-22 from a PTY Send):
//
//	"\n\n<pasted_content id=\"012e\">\n<mads-message …>\n…\n</mads-message>\n</pasted_content id=\"012e\">\n"
//
// and its prompt index (history.jsonl) keeps "[Pasted text #1 +4 lines]"
// with the text under pastedContents. A two-line paste is stored as is.
// mads's own PTY sends are pastes (provider.TypedMessage), so a linked
// session's message typed into the terminal comes back this way.
var pasteOpenRe = regexp.MustCompile(`<pasted_content id="([^"]*)">`)

// unpaste replaces each stored paste in a user message with the text that
// was pasted, so the row reads as what was sent — a wrapped linked-session
// message still parses (pkg/linkwrap) — and the locator's first-message
// match holds. Text with no paste in it is returned unchanged.
func unpaste(text string) string {
	if !strings.Contains(text, "<pasted_content id=") {
		return text
	}
	var b strings.Builder
	rest := text
	changed := false
	for {
		loc := pasteOpenRe.FindStringSubmatchIndex(rest)
		if loc == nil {
			break
		}
		closeTag := `</pasted_content id="` + rest[loc[2]:loc[3]] + `">`
		end := strings.Index(rest[loc[1]:], closeTag)
		if end < 0 {
			break // never closed: leave the rest as stored
		}
		body := rest[loc[1] : loc[1]+end]
		body = strings.TrimSuffix(strings.TrimPrefix(body, "\n"), "\n")
		b.WriteString(rest[:loc[0]])
		b.WriteString(body)
		rest = rest[loc[1]+end+len(closeTag):]
		changed = true
	}
	if !changed {
		return text
	}
	b.WriteString(rest)
	return strings.TrimSpace(b.String())
}

// pastePlaceholderRe is history.jsonl's stand-in for a paste in display.
var pastePlaceholderRe = regexp.MustCompile(`\[Pasted text #(\d+)(?: \+\d+ lines)?\]`)

// historyPaste is one pastedContents entry of a history.jsonl line.
type historyPaste struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// expandDisplay puts each pasted text back in place of its placeholder.
// A placeholder with no text stored for it is left as it is.
func expandDisplay(display string, pasted map[string]historyPaste) string {
	if len(pasted) == 0 {
		return display
	}
	return pastePlaceholderRe.ReplaceAllStringFunc(display, func(ph string) string {
		n := pastePlaceholderRe.FindStringSubmatch(ph)[1]
		if p, ok := pasted[n]; ok && p.Type == "text" {
			return p.Content
		}
		return ph
	})
}
