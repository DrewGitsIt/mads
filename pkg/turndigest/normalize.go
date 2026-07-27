package turndigest

import "unicode/utf8"

// maxEntryRunes caps a single entry's Text after merging — one pasted wall
// of text must not dominate the prompt.
const maxEntryRunes = 400

// item is a normalized entry plus its collapse count. The count exists only
// for rendering ("tool: Read foo.go ×6") — it is deliberately not part of
// the shared Entry contract.
type item struct {
	entry Entry
	count int
}

// normalizeTurn is the deterministic pass: merge runs of adjacent assistant
// fragments (Text joined with "\n", same bytes as rendering them separately
// minus the repeated prefixes), collapse runs of adjacent tool calls with
// identical (Tool, Path) into one counted item, then cap every entry's Text
// at maxEntryRunes.
func normalizeTurn(entries []Entry) []item {
	var items []item
	for _, e := range entries {
		if n := len(items); n > 0 {
			last := &items[n-1]
			if e.Kind == KindAssistant && last.entry.Kind == KindAssistant {
				last.entry.Text += "\n" + e.Text
				continue
			}
			if e.Kind == KindTool && last.entry.Kind == KindTool &&
				e.Tool == last.entry.Tool && e.Path == last.entry.Path {
				last.count++
				continue
			}
		}
		items = append(items, item{entry: e, count: 1})
	}
	for i := range items {
		items[i].entry.Text = capText(items[i].entry.Text)
	}
	return items
}

// capText cuts s rune-safe at maxEntryRunes, appending "…" when it cuts.
func capText(s string) string {
	if utf8.RuneCountInString(s) <= maxEntryRunes {
		return s
	}
	n := 0
	for i := range s {
		if n == maxEntryRunes {
			return s[:i] + "…"
		}
		n++
	}
	return s
}
