package turndigest

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// The digest prompt is conversation-first: what the user said and how the
// agent finally answered carry nearly all of a turn's meaning, so those are
// kept nearly whole, while tool calls shrink to one line proving the agent
// was working. Limits:
//
//   - maxEntryRunes caps one user message or final reply. Over the cap it
//     keeps the head and the tail — the tail is where a reply's conclusion
//     lives — around an explicit omission marker, so the reading model
//     never mistakes our cut for the agent trailing off mid-sentence.
//   - maxPromptBytes caps the whole rendering (~6k tokens): leader catch-up
//     digests up to 20 turns, and a local Fast Model can silently truncate
//     an oversized prompt. The oldest turns drop first.
const (
	maxEntryRunes  = 4000
	entryHeadRunes = 1500
	maxPromptBytes = 24000
)

// conversationTurn is one turn reduced for the digest prompt: every user
// entry in order, a tool-call tally, and only the terminal assistant reply
// (the last run of adjacent assistant fragments, merged) — interim
// narration between tool calls is dropped.
type conversationTurn struct {
	users []string
	tools string // "" when the turn made no tool calls
	reply string // "" when the turn ended without a reply
}

func normalizeTurn(entries []Entry) conversationTurn {
	var ct conversationTurn
	counts := map[string]int{}
	var order []string // tool names, first-seen order
	total := 0
	var reply []string
	replyOpen := false // the current reply run is still accepting fragments
	for _, e := range entries {
		switch e.Kind {
		case KindUser:
			ct.users = append(ct.users, capText(e.Text))
			replyOpen = false
		case KindAssistant:
			if !replyOpen {
				reply = reply[:0] // a newer run supersedes the interim one
				replyOpen = true
			}
			reply = append(reply, e.Text)
		case KindTool:
			if counts[e.Tool] == 0 {
				order = append(order, e.Tool)
			}
			counts[e.Tool]++
			total++
			replyOpen = false
		}
	}
	if len(reply) > 0 {
		ct.reply = capText(strings.Join(reply, "\n"))
	}
	if total > 0 {
		ct.tools = toolTally(total, counts, order)
	}
	return ct
}

// toolTally renders "18 calls (Read ×12, Bash ×4, Edit ×2)": busiest tool
// first, ties in first-seen order, no ×1 suffix.
func toolTally(total int, counts map[string]int, order []string) string {
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	parts := make([]string, len(order))
	for i, name := range order {
		parts[i] = name
		if n := counts[name]; n > 1 {
			parts[i] = fmt.Sprintf("%s ×%d", name, n)
		}
	}
	noun := "calls"
	if total == 1 {
		noun = "call"
	}
	return fmt.Sprintf("%d %s (%s)", total, noun, strings.Join(parts, ", "))
}

// capText keeps s whole up to maxEntryRunes; beyond that, the first
// entryHeadRunes and the last maxEntryRunes-entryHeadRunes runes around a
// "[…N chars omitted…]" marker. Rune-safe.
func capText(s string) string {
	n := utf8.RuneCountInString(s)
	if n <= maxEntryRunes {
		return s
	}
	r := []rune(s)
	tail := maxEntryRunes - entryHeadRunes
	return fmt.Sprintf("%s\n[…%d chars omitted…]\n%s",
		string(r[:entryHeadRunes]), n-maxEntryRunes, string(r[n-tail:]))
}
