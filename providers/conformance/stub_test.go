package conformance

// The stub is a minimal in-memory provider whose only job is to prove the
// suite itself: an honest stub passes, and each way an adapter can lie about
// its matrix — declared-working-but-broken or declared-unsupported-but-works
// — is caught (W2.1 done criterion).

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

type lies struct {
	interruptStuck      bool // interrupt never ends the turn
	oneShotWrong        bool // one-shot ignores the prompt
	secretTranscript    bool // returns transcripts despite declaring unsupported
	forgetful           bool // resume reattaches with empty context
	zeroUsage           bool // reports no tokens despite declaring cost_reporting
	acceptAnyResume     bool // resumes despite declaring resume unsupported
	completesDisallowed bool // a disallowed tool completes (non-error result) below full trust
	dropsFocus          bool // takes compaction instructions despite declaring compaction_focus unsupported
	// oneShotFreshOnResume answers a one-shot with Resume by silently
	// starting fresh, whatever the declaration.
	oneShotFreshOnResume bool
	// stable_keys lies (each one a way the store and the stream disagree).
	keysDropResult  bool // the store never keeps the tool result
	keysRenamed     bool // the store keys the second text part differently
	keysTextDiffers bool // the store's plain reply text differs from the stream
	keysInterleave  bool // the first text part's key comes back after the tool
	keysUnsupported bool // emits keys while declaring stable_keys unsupported
	settleUnasked   bool // emits EventSettle without StartOptions.Keyed
	thinkingDropped bool // streams thinking it declares stored, but never stores it
	thinkingInPlain bool // puts thinking rows in plain Transcript too
}

type stub struct {
	matrix provider.Matrix
	lies   lies

	mu       sync.Mutex
	seq      int
	sessions map[string][]provider.Message // history by ref, survives Close
}

func newStub(overrides map[provider.Behavior]provider.Support, l lies) *stub {
	m := provider.Matrix{}
	for _, b := range provider.Behaviors() {
		m[b] = provider.Native
	}
	for b, s := range overrides {
		m[b] = s
	}
	return &stub{matrix: m, lies: l, sessions: map[string][]provider.Message{}}
}

// InspectDelegate makes the stub's native delegate_transcript row honest:
// the strategy exists, and its store simply never has a delegate.
func (s *stub) InspectDelegate(context.Context, string, string) (provider.DelegateInfo, bool, error) {
	return provider.DelegateInfo{}, false, nil
}

func (s *stub) DelegateTranscript(context.Context, string, string) ([]provider.Message, bool, error) {
	return nil, false, nil
}

func (s *stub) DelegateHistory(context.Context, string) ([]provider.DelegateRecord, error) {
	return nil, nil
}

// stubScript is the stub's prompt dialect. The attempt prompts drive the
// goose-shaped denial ordering (announce, then a failed result — see
// stubSession.Send), so the honest stub exercises the trust probe's
// "announcement is not the effect" rule, not just the suppression style.
func stubScript() Script {
	return Script{
		Echo:           func(marker string) string { return "echo " + marker },
		Slow:           func() string { return "slow" },
		Recall:         func() string { return "recall" },
		AttemptWrite:   func() string { return "attempt-write" },
		AttemptCommand: func() string { return "attempt-command" },
		ToolTurn:       func() string { return "tool-text" },
		StoreWait:      300 * time.Millisecond,
		Timeout:        2 * time.Second,
	}
}

func (s *stub) Name() string            { return "stub" }
func (s *stub) Matrix() provider.Matrix { return s.matrix }

func (s *stub) Start(_ context.Context, opts provider.StartOptions) (provider.Session, error) {
	modeRow := provider.BehaviorACP
	if opts.Mode == provider.ModePTY {
		modeRow = provider.BehaviorPTY
	}
	if !s.matrix[modeRow].Works() {
		return nil, fmt.Errorf("mode %s: %w", opts.Mode, provider.ErrUnsupported)
	}
	if opts.Resume != "" && !s.matrix[provider.BehaviorResume].Works() && !s.lies.acceptAnyResume {
		return nil, fmt.Errorf("resume: %w", provider.ErrUnsupported)
	}

	s.mu.Lock()
	ref := opts.Resume
	if ref == "" || s.lies.forgetful {
		s.seq++
		ref = fmt.Sprintf("stub-%d", s.seq)
	}
	s.mu.Unlock()

	sess := &stubSession{
		p: s, ref: ref, mode: opts.Mode, settle: opts.Keyed || s.lies.settleUnasked,
		events:    make(chan provider.Event, 256),
		interrupt: make(chan struct{}, 1),
	}
	if opts.Mode == provider.ModePTY {
		sess.ptyR, sess.ptyW = io.Pipe()
	}
	return sess, nil
}

func (s *stub) OneShot(_ context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	var history []provider.Message
	if req.Resume != "" {
		switch {
		case s.lies.oneShotFreshOnResume:
			// starts fresh: no history, no error
		case !s.matrix[provider.BehaviorOneShotResume].Works():
			return nil, provider.ErrResumeUnsupported
		default:
			s.mu.Lock()
			history = append(history, s.sessions[req.Resume]...)
			s.sessions[req.Resume] = append(s.sessions[req.Resume], provider.Message{Role: "user", Text: req.Prompt})
			s.mu.Unlock()
		}
	}
	text := s.reply(req.Prompt, history)
	if s.lies.oneShotWrong {
		text = "whatever"
	}
	if req.OnEvent != nil {
		req.OnEvent(provider.Event{Kind: provider.EventText, Text: text})
	}
	res := &provider.OneShotResult{Text: text, Usage: s.usage(text)}
	if req.Resume != "" {
		res.Ref = req.Resume
	}
	return res, nil
}

func (s *stub) Models(context.Context) ([]provider.Model, error) {
	if !s.matrix[provider.BehaviorModelDiscovery].Works() {
		return nil, provider.ErrUnsupported
	}
	return []provider.Model{{ID: "stub-1", Name: "Stub One", Default: true}}, nil
}

func (s *stub) Transcript(_ context.Context, ref string) ([]provider.Message, error) {
	if s.lies.secretTranscript {
		return []provider.Message{{Role: "agent", Text: "leaked"}}, nil
	}
	if !s.matrix[provider.BehaviorTranscript].Works() {
		return nil, provider.ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []provider.Message
	for _, m := range s.sessions[ref] {
		if m.Role != "thinking" || s.lies.thinkingInPlain {
			out = append(out, m)
		}
	}
	return out, nil
}

// KeyedTranscript is Transcript plus the thinking rows the stub stores.
func (s *stub) KeyedTranscript(_ context.Context, ref string) ([]provider.Message, error) {
	if !s.matrix[provider.BehaviorTranscript].Works() {
		return nil, provider.ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]provider.Message{}, s.sessions[ref]...), nil
}

// TranscriptSince is the stub's incremental reader: the cursor is a row
// index.
func (s *stub) TranscriptSince(ctx context.Context, ref string, cursor provider.TranscriptCursor) ([]provider.Message, provider.TranscriptCursor, error) {
	all, err := s.KeyedTranscript(ctx, ref)
	if err != nil {
		return nil, cursor, err
	}
	from, _ := cursor.(int)
	if from > len(all) {
		return nil, cursor, provider.ErrCursorStale
	}
	return all[from:], len(all), nil
}

func (s *stub) usage(text string) provider.Usage {
	if s.lies.zeroUsage {
		return provider.Usage{}
	}
	return provider.Usage{InputTokens: 3, OutputTokens: len(text)/4 + 1, Model: "stub-1"}
}

// reply computes the stub agent's answer to a prompt given prior history.
func (s *stub) reply(prompt string, history []provider.Message) string {
	switch {
	case strings.HasPrefix(prompt, "echo "):
		return "you said " + strings.TrimPrefix(prompt, "echo ")
	case prompt == "recall":
		var parts []string
		for _, m := range history {
			if m.Role == "user" && strings.HasPrefix(m.Text, "echo ") {
				parts = append(parts, strings.TrimPrefix(m.Text, "echo "))
			}
		}
		return "earlier you said: " + strings.Join(parts, ", ")
	default:
		return "ok"
	}
}

type stubSession struct {
	p         *stub
	ref       string
	mode      provider.Mode
	events    chan provider.Event
	interrupt chan struct{}
	ptyR      *io.PipeReader
	ptyW      *io.PipeWriter

	settle bool // emit EventSettle at each turn's end

	mu     sync.Mutex
	closed bool
	turn   int
}

// keys reports whether the stub keys its items: honestly when it declares
// stable_keys working, or as a lie.
func (ss *stubSession) keys() bool {
	return ss.p.matrix[provider.BehaviorStableKeys].Works() || ss.p.lies.keysUnsupported
}

// key is the stub's native id for item n of the current turn, or "" when it
// doesn't key.
func (ss *stubSession) key(n string) string {
	if !ss.keys() {
		return ""
	}
	return fmt.Sprintf("%s:t%d:%s", ss.ref, ss.turn, n)
}

func (ss *stubSession) SendKeyed(ctx context.Context, text, id string) error {
	return ss.send(ctx, text, id)
}

func (ss *stubSession) Ref() string                   { return ss.ref }
func (ss *stubSession) Mode() provider.Mode           { return ss.mode }
func (ss *stubSession) Events() <-chan provider.Event { return ss.events }
func (ss *stubSession) Terminal() io.ReadWriter       { return rw{ss.ptyR, io.Discard} }
func (ss *stubSession) Resize(cols, rows int) error   { return nil }
func (ss *stubSession) Interrupt(context.Context) error {
	select {
	case ss.interrupt <- struct{}{}:
	default:
	}
	return nil
}

// Compact is the stub's whole compaction surface: it refuses what the
// matrix declares unsupported, unless lying about focus.
func (ss *stubSession) Compact(_ context.Context, instructions string) error {
	m := ss.p.matrix
	if !m[provider.BehaviorCompaction].Works() {
		return provider.ErrUnsupported
	}
	if instructions != "" && !m[provider.BehaviorCompactionFocus].Works() && !ss.p.lies.dropsFocus {
		return provider.ErrUnsupported
	}
	return nil
}

func (ss *stubSession) Close() error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if !ss.closed {
		ss.closed = true
		close(ss.events)
		if ss.ptyW != nil {
			_ = ss.ptyW.Close()
		}
	}
	return nil
}

func (ss *stubSession) Send(ctx context.Context, text string) error {
	return ss.send(ctx, text, "")
}

func (ss *stubSession) send(_ context.Context, text, id string) error {
	ss.mu.Lock()
	ss.turn++
	ss.mu.Unlock()
	userKey := ""
	if ss.keys() {
		if id == "" {
			id = ss.key("user")
		}
		userKey = "user:" + id
	}
	ss.p.mu.Lock()
	history := append([]provider.Message{}, ss.p.sessions[ss.ref]...)
	ss.p.sessions[ss.ref] = append(ss.p.sessions[ss.ref], provider.Message{Role: "user", Text: text, Key: userKey})
	ss.p.mu.Unlock()
	store := func(m provider.Message) {
		ss.p.mu.Lock()
		ss.p.sessions[ss.ref] = append(ss.p.sessions[ss.ref], m)
		ss.p.mu.Unlock()
	}
	end := func(ev provider.Event) {
		if ss.settle {
			ss.emit(provider.Event{Kind: provider.EventSettle})
		}
		ss.emit(ev)
	}

	go func() {
		if text == "slow" {
			for i := 0; ; i++ {
				select {
				case <-ss.interrupt:
					if !ss.p.lies.interruptStuck {
						end(provider.Event{Kind: provider.EventTurnEnd, Interrupted: true})
						return
					}
				case <-time.After(5 * time.Millisecond):
					if !ss.emit(provider.Event{Kind: provider.EventText, Key: ss.key("ticks"), Text: fmt.Sprintf("tick %d ", i)}) {
						return
					}
				}
			}
		}
		if text == "attempt-write" || text == "attempt-command" {
			name := "write_file"
			if text == "attempt-command" {
				name = "run_command"
			}
			// goose ordering: the attempt is announced before it is gated —
			// the tool-use event alone must never fail the trust probe. Only
			// a lying stub lets the disallowed call complete (non-error
			// result); the honest one fails it.
			ss.emit(provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{Name: name}})
			ss.emit(provider.Event{
				Kind:    provider.EventToolResult,
				Tool:    &provider.ToolUse{Name: name},
				IsError: !ss.p.lies.completesDisallowed,
			})
			ss.emit(provider.Event{Kind: provider.EventTurnEnd})
			return
		}
		if text == "tool-text" {
			l := ss.p.lies
			first, tool, second := ss.key("a"), ss.key("tool"), ss.key("b")
			if ss.p.matrix[provider.BehaviorStoredThinking].Works() {
				think := ss.key("think")
				ss.emit(provider.Event{Kind: provider.EventThinking, Key: think, Text: "List it first."})
				if !l.thinkingDropped {
					store(provider.Message{Role: "thinking", Key: think, Text: "List it first."})
				}
			}
			ss.emit(provider.Event{Kind: provider.EventText, Key: first, Text: "Looking. "})
			ss.emit(provider.Event{Kind: provider.EventToolUse, Key: tool, Tool: &provider.ToolUse{Name: "ls"}})
			ss.emit(provider.Event{Kind: provider.EventToolResult, Key: tool, Text: "a.txt"})
			if l.keysInterleave {
				ss.emit(provider.Event{Kind: provider.EventText, Key: first, Text: "late "})
			}
			ss.emit(provider.Event{Kind: provider.EventText, Key: second, Text: "One file."})
			storedSecond := second
			if l.keysRenamed {
				storedSecond += "-renamed"
			}
			store(provider.Message{Role: "agent", Key: first, Text: "Looking. "})
			store(provider.Message{Role: "tool", Key: tool, Tool: &provider.ToolUse{Name: "ls"}})
			if !l.keysDropResult {
				store(provider.Message{Role: "tool_result", Key: tool, Text: "a.txt"})
			}
			store(provider.Message{Role: "agent", Key: storedSecond, Text: "One file."})
			usage := ss.p.usage("Looking. One file.")
			end(provider.Event{Kind: provider.EventTurnEnd, Usage: &usage})
			return
		}
		reply := ss.p.reply(text, history)
		key := ss.key("reply")
		storedText := reply
		if ss.p.lies.keysTextDiffers {
			storedText += " (edited)"
		}
		store(provider.Message{Role: "agent", Key: key, Text: storedText})
		usage := ss.p.usage(reply)
		ss.emit(provider.Event{Kind: provider.EventText, Key: key, Text: reply})
		end(provider.Event{Kind: provider.EventTurnEnd, Usage: &usage})
		if ss.mode == provider.ModePTY && ss.ptyW != nil {
			_, _ = ss.ptyW.Write([]byte(reply + "\r\n> "))
		}
	}()
	return nil
}

// emit delivers an event unless the session is closed; false stops responder
// goroutines so nothing outlives Close.
func (ss *stubSession) emit(ev provider.Event) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed {
		return false
	}
	select {
	case ss.events <- ev:
	default:
	}
	return true
}

type rw struct {
	io.Reader
	io.Writer
}
