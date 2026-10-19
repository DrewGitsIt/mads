package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// get_session_turns reads the subject's transcript, not its
// session.turn_ended digest deltas (2026-10-19). The deltas were built for
// digests: assistant text in streaming fragments, tool calls reduced to a
// bare name, no tool output, no opening message, and event-log seqs for
// turn numbers. A leader auditing a session needs the opposite — its words
// verbatim and each tool call with what went in and what came out. Raw tool
// content leaving the subject is fine here: a link is user-granted, with
// explicit permissions, for exactly this kind of reading.
//
// The caps keep one tool call from flooding the leader's context. They're
// counted in characters (runes), the unit the truncation markers report.
const (
	// turnsToolInputMax caps one tool call's input (compact JSON).
	turnsToolInputMax = 1000
	// turnsToolOutputMax caps one tool call's output.
	turnsToolOutputMax = 2000
	// turnsResponseMax caps the whole response; the oldest turns in the
	// requested slice are dropped first to fit.
	turnsResponseMax = 40000
)

// subjectTranscript reads subject's transcript the same way
// handleSessionTranscript does: the running child's own /transcript first
// (the only record of an in-flight turn, and the only one carrying link
// attributions), the provider's durable TranscriptReader otherwise.
func (s *server) subjectTranscript(ctx context.Context, subject contracts.Session) []contracts.TranscriptMessage {
	var msgs []contracts.TranscriptMessage
	if addr, ok := s.sup.SessionAddr(subject.ID); ok {
		if err := s.fetchChildJSON(ctx, addr, "/transcript", &msgs); err != nil {
			msgs = nil
		}
	}
	if msgs == nil {
		msgs = s.transcriptFor(ctx, subject)
	}
	return msgs
}

// splitTurns segments a transcript into turns: each "user"-role message —
// the human's, or one a link injected (Attribution set) — starts a turn
// that runs until the next one. Anything before the first user message
// (a fork's inherited head, an opening notification) folds into turn 1
// rather than becoming a turn of its own, so the turn count is the number
// of messages the session was sent. A transcript with entries but no user
// message is one turn; an empty one is none.
func splitTurns(msgs []contracts.TranscriptMessage) [][]contracts.TranscriptMessage {
	var turns [][]contracts.TranscriptMessage
	sawUser := false
	for _, m := range msgs {
		if m.Role == "user" {
			if sawUser || len(turns) == 0 {
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

// turnSourceLabel is the "## Turn N · <label>" half naming who sent the
// message that opened the turn, worded as the conversation pane words its
// chips (ui/src/conversation/ChatRenderer/ChatRenderer.tsx): a mads notice
// with no link reads "from <source>", a digest push "digest · <source>",
// any other link injection "from Leader · <source>".
func turnSourceLabel(m contracts.TranscriptMessage) string {
	a := m.Attribution
	switch {
	case a == nil:
		return "user"
	case a.LinkID == "":
		name := a.SourceName
		if name == "" {
			name = "mads"
		}
		return "from " + name
	case a.DigestAttributed:
		return "digest · " + a.SourceName
	default:
		return "from Leader · " + a.SourceName
	}
}

// renderTurn writes one turn as plain text: role, words, and each tool call
// with its input and paired output — no statuses, ids, or locations, which
// only cost the reader tokens.
func renderTurn(n int, msgs []contracts.TranscriptMessage) string {
	var sb strings.Builder
	opener := -1
	for i, m := range msgs {
		if m.Role == "user" {
			opener = i
			break
		}
	}
	if opener >= 0 {
		fmt.Fprintf(&sb, "## Turn %d · %s\n", n, turnSourceLabel(msgs[opener]))
	} else {
		fmt.Fprintf(&sb, "## Turn %d\n", n)
	}

	results, paired := pairToolResults(msgs)
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case "user":
			sb.WriteString(m.Text)
			sb.WriteString("\n")
		case "agent":
			// Consecutive agent entries are one reply some transports
			// record as separate fragments; concatenate rather than print
			// a line per fragment.
			text := m.Text
			for i+1 < len(msgs) && msgs[i+1].Role == "agent" && msgs[i+1].IsError == m.IsError {
				i++
				text += msgs[i].Text
			}
			if m.IsError {
				sb.WriteString("agent error: ")
			} else {
				sb.WriteString("agent: ")
			}
			sb.WriteString(text)
			sb.WriteString("\n")
		case "tool":
			sb.WriteString("tool ")
			if m.Tool != nil {
				sb.WriteString(m.Tool.Name)
				if in := compactToolInput(m.Tool.Input); in != "" {
					sb.WriteString(" ")
					sb.WriteString(truncateChars(in, turnsToolInputMax))
				}
			}
			sb.WriteString("\n")
			if r, ok := results[i]; ok {
				writeToolOutput(&sb, msgs[r])
			}
		case "tool_result":
			if !paired[i] {
				writeToolOutput(&sb, m)
			}
		case "compact":
			sb.WriteString("[context compacted]\n")
		case "notification":
			sb.WriteString("notification: ")
			sb.WriteString(m.Text)
			sb.WriteString("\n")
		default:
			if m.Text != "" {
				sb.WriteString(m.Role + ": " + m.Text + "\n")
			}
		}
	}
	return sb.String()
}

// pairToolResults maps each tool call's index in msgs to its tool_result's
// index (and marks the paired results): by tool id when both carry one,
// else positionally — a result with no id (an ACP replay's) answers the
// nearest earlier call still waiting. A result matching no call renders on
// its own.
func pairToolResults(msgs []contracts.TranscriptMessage) (pairs map[int]int, paired map[int]bool) {
	pairs = map[int]int{}
	paired = map[int]bool{}
	byID := map[string]int{}
	var open []int
	for i, m := range msgs {
		switch m.Role {
		case "tool":
			if m.Tool != nil && m.Tool.ID != "" {
				byID[m.Tool.ID] = i
			}
			open = append(open, i)
		case "tool_result":
			call := -1
			if m.Tool != nil && m.Tool.ID != "" {
				if c, ok := byID[m.Tool.ID]; ok {
					call = c
				}
			} else if len(open) > 0 {
				call = open[len(open)-1]
			}
			if call < 0 {
				continue
			}
			if _, taken := pairs[call]; taken {
				continue
			}
			pairs[call] = i
			paired[i] = true
			for k, c := range open {
				if c == call {
					open = append(open[:k], open[k+1:]...)
					break
				}
			}
		}
	}
	return pairs, paired
}

// writeToolOutput writes a tool_result under its call: "  → " on the first
// line, continuation lines indented to match, errors marked.
func writeToolOutput(sb *strings.Builder, m contracts.TranscriptMessage) {
	text := truncateChars(strings.TrimRight(m.Text, "\n"), turnsToolOutputMax)
	if m.IsError {
		text = "error: " + text
	}
	sb.WriteString("  → ")
	sb.WriteString(strings.ReplaceAll(text, "\n", "\n    "))
	sb.WriteString("\n")
}

// compactToolInput renders a tool call's input as one line of compact JSON.
func compactToolInput(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return strings.ReplaceAll(string(raw), "\n", " ")
	}
	return buf.String()
}

// truncateChars cuts s to max characters, rune-safe, with a marker giving
// the true size so the reader knows how much it didn't see.
func truncateChars(s string, max int) string {
	n := utf8.RuneCountInString(s)
	if n <= max {
		return s
	}
	cut := 0
	for i := range s {
		if max == 0 {
			cut = i
			break
		}
		max--
	}
	return s[:cut] + "…(truncated, " + commaInt(n) + " chars)"
}

// commaInt formats n with thousands separators: 18430 → "18,430".
func commaInt(n int) string {
	if n < 0 {
		return "-" + commaInt(-n)
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// turnRange renders "41" or "41–42".
func turnRange(from, to int) string {
	if from == to {
		return strconv.Itoa(from)
	}
	return strconv.Itoa(from) + "–" + strconv.Itoa(to)
}

// renderTurnsSlice answers get_session_turns for a transcript already split
// into turns. count alone is the last count turns; fromTurn (1-based) plus
// count is count turns starting there. A slice running past either end is
// clamped and the header says so; fromTurn past the last turn is the one
// request with nothing to clamp to, reported as ok=false with the text
// saying how many turns there are.
func renderTurnsSlice(name string, turns [][]contracts.TranscriptMessage, count, fromTurn int) (text string, ok bool) {
	total := len(turns)
	if count < 1 {
		count = 1
	}
	if fromTurn > total {
		return fmt.Sprintf("%s has %d turns; from_turn %d is past the end. Omit from_turn to read the last turns.", name, total, fromTurn), false
	}
	if total == 0 {
		return name + " has no turns yet.", true
	}

	var notes []string
	var start, end int
	if fromTurn > 0 {
		start, end = fromTurn, fromTurn+count-1
		if end > total {
			notes = append(notes, fmt.Sprintf("asked for turns %s; it has %d", turnRange(start, end), total))
			end = total
		}
	} else {
		if count > total {
			notes = append(notes, fmt.Sprintf("asked for the last %d; it has %d", count, total))
			count = total
		}
		start, end = total-count+1, total
	}

	rendered := make([]string, 0, end-start+1)
	size := 0
	for n := start; n <= end; n++ {
		r := renderTurn(n, turns[n-1])
		rendered = append(rendered, r)
		size += utf8.RuneCountInString(r) + 1
	}
	// Drop the oldest turns first: the newest are what a catching-up
	// reader most likely wants, and it can page back with from_turn.
	first := start
	for size > turnsResponseMax && len(rendered) > 1 {
		size -= utf8.RuneCountInString(rendered[0]) + 1
		rendered = rendered[1:]
		first++
	}
	if first > start {
		notes = append(notes, fmt.Sprintf("turns %s omitted to fit; ask with a smaller count", turnRange(start, first-1)))
	}
	if size > turnsResponseMax {
		rendered[0] = truncateChars(rendered[0], turnsResponseMax)
		notes = append(notes, fmt.Sprintf("turn %d cut to fit", first))
	}

	var sb strings.Builder
	if first == end {
		fmt.Fprintf(&sb, "%s — turn %d of %d\n", name, first, total)
	} else {
		fmt.Fprintf(&sb, "%s — turns %s of %d\n", name, turnRange(first, end), total)
	}
	if len(notes) > 0 {
		sb.WriteString("(" + strings.Join(notes, "; ") + ")\n")
	}
	for _, r := range rendered {
		sb.WriteString("\n")
		sb.WriteString(r)
	}
	return sb.String(), true
}

// handleOrchestrateGetTurns answers {id}'s (the leader) get_session_turns
// tool call for the subject named by "for": a slice of the subject's
// transcript turns (count, from_turn — see renderTurnsSlice), rendered as
// plain text the tool hands the model as is. Gated by the same link check
// as get_session_summary.
func (s *server) handleOrchestrateGetTurns(w http.ResponseWriter, r *http.Request) {
	leaderID := r.PathValue("id")
	subjectID := r.URL.Query().Get("for")
	link, err := s.findOrchestrateLink(r.Context(), leaderID, subjectID)
	if err != nil {
		writeError(w, err)
		return
	}
	if link == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no orchestrate link to that subject"})
		return
	}
	subject, err := s.data.Session(r.Context(), subjectID)
	if err != nil {
		writeError(w, err)
		return
	}
	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	fromTurn, _ := strconv.Atoi(r.URL.Query().Get("from_turn"))
	name := subject.Name
	if name == "" {
		name = subject.ID
	}

	text, ok := renderTurnsSlice(name, splitTurns(s.subjectTranscript(r.Context(), subject)), count, fromTurn)
	status := http.StatusOK
	if !ok {
		status = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(text))
}
