//go:build perf

package fake

// The perf harness's dialects (tools/perf/README.md), compiled only with
// -tags perf: the harness builds its services with the tag; no release does.
//
//	"permission [tool]" → calls tool (default Bash) and parks on the
//	              session's permission hook (StartOptions.Permissions) until
//	              it is decided, then ends the turn with "allowed"/"denied"
//
//	"perf-stream <chars> <rate> <delta>" → one reply of <chars> characters
//	              of generated Markdown, streamed as <delta>-character text
//	              deltas at <rate> a second (scenario 10x's live reply)
//
// MADS_FAKE_START_DELAY_MS in the environment delays every Start by that
// long (a slow harness start, for watching provisioning).
//
// MADS_FAKE_PERF_HISTORY_DIR names a dir of <ref>.json histories (the
// fake's transcript file format) that Start may resume from: the session
// child resumes a seeded conversation (perfseed chat) it never ran.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

func init() {
	startDelay = perfStartDelay
	perfPlan = func(prompt string) (turnPlan, bool) {
		if p, ok := permissionPlan(prompt); ok {
			return p, true
		}
		return streamPlan(prompt)
	}
	perfHistory = perfHistoryFromDir
}

// perfHistoryFromDir reads ref's history from MADS_FAKE_PERF_HISTORY_DIR.
func perfHistoryFromDir(ref string) ([]provider.Message, bool) {
	dir := os.Getenv("MADS_FAKE_PERF_HISTORY_DIR")
	if dir == "" || ref == "" || strings.ContainsAny(ref, `/\`) {
		return nil, false
	}
	msgs, err := readTranscriptFile(dir, ref)
	if err != nil {
		return nil, false
	}
	return msgs, true
}

// perfStartDelay: a harness that takes its time to start, so a session's
// provisioning steps can be watched (and reloaded) mid-way.
func perfStartDelay() {
	if ms, err := strconv.Atoi(os.Getenv("MADS_FAKE_START_DELAY_MS")); err == nil && ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}

func permissionPlan(prompt string) (turnPlan, bool) {
	if prompt != "permission" && !strings.HasPrefix(prompt, "permission ") {
		return turnPlan{}, false
	}
	tool := strings.TrimSpace(strings.TrimPrefix(prompt, "permission"))
	if tool == "" {
		tool = "Bash"
	}
	return turnPlan{perf: func(s *fakeSession, prompt string, turn int) { s.respondPermission(prompt, tool, turn) }}, true
}

// respondPermission plays the "permission" dialect: a tool call that parks
// on the session's permission hook, as a harness does when its trust makes
// it ask, until the user decides (or the turn is interrupted).
func (s *fakeSession) respondPermission(prompt, toolName string, turn int) {
	key := s.itemKey(turn, "tool0")
	tool := &provider.ToolUse{ID: key, Name: toolName, Input: json.RawMessage(`{"command":"echo fake permission"}`)}
	s.store(provider.Message{Role: "tool", Tool: tool, Key: key})
	if !s.emit(provider.Event{Kind: provider.EventToolUse, Key: key, Tool: tool}) {
		return
	}
	if s.asker == nil {
		s.emit(provider.Event{Kind: provider.EventError, Text: "fake: no permission hook to ask"})
		s.emit(provider.Event{Kind: provider.EventTurnEnd})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.interrupt:
			cancel()
		case <-ctx.Done():
		}
	}()
	d := s.asker(ctx, provider.PermissionAsk{ToolUseID: key, Tool: toolName, Input: tool.Input, Reason: "fake: the permission dialect always asks"})
	reply := "denied"
	if d.Allow {
		reply = "allowed"
	}
	s.finish(prompt, s.itemKey(turn, "reply"), reply, ctx.Err() != nil)
}

// streamPlan parses "perf-stream <chars> <rate> <delta>".
func streamPlan(prompt string) (turnPlan, bool) {
	f := strings.Fields(prompt)
	if len(f) != 4 || f[0] != "perf-stream" {
		return turnPlan{}, false
	}
	n := make([]int, 3)
	for i, x := range f[1:] {
		v, err := strconv.Atoi(x)
		if err != nil || v <= 0 {
			return turnPlan{chunks: []string{"perf-stream needs <chars> <rate> <delta>, all positive"}}, true
		}
		n[i] = v
	}
	chars, rate, delta := n[0], n[1], n[2]
	return turnPlan{perf: func(s *fakeSession, prompt string, turn int) { s.respondStream(prompt, turn, chars, rate, delta) }}, true
}

// respondStream plays the "perf-stream" dialect: StreamText(chars) in
// delta-character text events, at most rate a second (one per tick of a
// time.Ticker), then the reply stored and the turn ended. A tick that comes
// while emit is still blocked on a slow consumer is dropped, not made up,
// so such a consumer stretches the stream: the harness reports the rate the
// page actually got (chat_deltas_per_s). An interrupt stores what was sent.
func (s *fakeSession) respondStream(prompt string, turn, chars, rate, delta int) {
	text := StreamText(chars)
	key := s.itemKey(turn, "reply")
	tick := time.NewTicker(time.Second / time.Duration(rate))
	defer tick.Stop()
	sent := 0
	for sent < len(text) {
		select {
		case <-s.interrupt:
			s.finish(prompt, key, text[:sent], true)
			return
		case <-tick.C:
		}
		end := min(sent+delta, len(text))
		if !s.emit(provider.Event{Kind: provider.EventText, Key: key, Text: text[sent:end]}) {
			return
		}
		sent = end
	}
	s.finish(prompt, key, text, false)
}

// streamWords is StreamText's vocabulary: letters only, so the alphanumeric
// content of the rendered reply is exactly the text's.
var streamWords = strings.Fields(`the a an of to in for on with by from as at into over after before under between
reply stream render frame batch commit layout paint session agent message turn row page view list item key
value state store reducer hook effect memo props callback timer scroll bottom tail follow anchor runway
spacer cursor marker probe metric sample median window budget cost growth width height node tree branch
merge rebase commit push land diff change file folder repo worktree shadow index status ledger build test
check lint vet run fix bug patch review design plan draft note idea reason result error retry timeout
quick slow steady calm busy idle long short wide narrow fresh stale clean dirty early late first last
next prior every each some most few many more less often rarely always never usually simply mostly
measure compare report track watch read write send fetch parse format split join wrap fold open close
keeps holds moves grows shrinks lands starts stops waits shows hides makes takes gives finds uses needs`)

// StreamText is the perf-stream reply: chars characters of Markdown
// (headings, paragraphs with inline code and bold, bullet lists, fenced
// code blocks), the same for the same length. ASCII only; no ordered
// lists, links or HTML, so what renders is the text minus its Markdown.
func StreamText(chars int) string {
	rng := rand.New(rand.NewPCG(10, 0x5eed))
	word := func() string { return streamWords[rng.IntN(len(streamWords))] }
	words := func(n int) string {
		w := make([]string, n)
		for i := range w {
			w[i] = word()
		}
		return strings.Join(w, " ")
	}
	sentence := func() string {
		s := words(6 + rng.IntN(10))
		switch rng.IntN(6) {
		case 0:
			s += " `" + word() + word() + "`"
		case 1:
			s = "**" + words(2) + "** " + s
		}
		return strings.ToUpper(s[:1]) + s[1:] + "."
	}
	var b strings.Builder
	for b.Len() < chars {
		h := words(3)
		fmt.Fprintf(&b, "## %s\n\n", strings.ToUpper(h[:1])+h[1:])
		for p := 0; p < 2+rng.IntN(2); p++ {
			n := 3 + rng.IntN(3)
			for i := 0; i < n; i++ {
				if i > 0 {
					b.WriteString(" ")
				}
				b.WriteString(sentence())
			}
			b.WriteString("\n\n")
		}
		if rng.IntN(2) == 0 {
			for i := 0; i < 3+rng.IntN(3); i++ {
				fmt.Fprintf(&b, "- %s\n", sentence())
			}
			b.WriteString("\n")
		} else {
			b.WriteString("```\n")
			for i := 0; i < 4+rng.IntN(5); i++ {
				fmt.Fprintf(&b, "%s := %s(%s, %d)\n", word(), word(), word(), rng.IntN(100))
			}
			b.WriteString("```\n\n")
		}
	}
	return b.String()[:chars]
}
