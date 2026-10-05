// Package conformance is the §11 capability matrix as an executable test
// suite (spec §15): every provider adapter runs the same checks, and a
// mismatch in either direction — a behavior declared working that fails, or
// one declared unsupported that works — is a failure. Adapters call Run from
// their own test packages; the Check form returns per-behavior results for
// programmatic use (the Settings provider preflight renders from the same
// data).
package conformance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// Script tells the suite how to elicit known behavior from this provider's
// agent. For provider-fake these map onto scripted directives; for a real
// harness they are real prompts ("reply with exactly ...", "count slowly to
// one thousand").
type Script struct {
	// Echo returns a prompt whose reply must contain marker.
	Echo func(marker string) string
	// Slow returns a prompt that streams long enough for an interrupt to
	// land mid-turn.
	Slow func() string
	// Recall returns a prompt whose reply must contain a marker sent in an
	// earlier turn of the same (or resumed) session.
	Recall func() string
	// AttemptWrite returns a prompt that makes the agent try to write a file.
	// Used to probe BehaviorTrustReadOnly: the write must never complete
	// successfully (announcing the attempt is fine; a non-error result is not).
	AttemptWrite func() string
	// AttemptCommand returns a prompt that makes the agent try to run a
	// command. Used to probe BehaviorTrustLimited: the command must never
	// complete successfully.
	AttemptCommand func() string
	// Unprompted returns a prompt after which the agent produces more
	// main-agent output once its own turn has already ended — the claude
	// CLI waking itself to read a background task that finished late is the
	// case this exists for. Used by the TurnTermination check; leave it nil
	// on a harness that cannot elicit the shape, and the check records that
	// it was not probed rather than passing silently.
	Unprompted func() string
	// Timeout bounds each wait on the provider. Zero means 5s.
	Timeout time.Duration
}

func (s Script) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 5 * time.Second
}

// MatrixResult is the pseudo-behavior naming the matrix's own structural
// validation in Result lists.
const MatrixResult provider.Behavior = "matrix"

// TurnTermination is the pseudo-behavior naming the turn-termination
// contract. Unlike the matrix rows it is not a capability an adapter may
// decline: every adapter on the structured transport owes it unconditionally,
// which is why it has no Support column.
//
// The contract: an adapter must never leave main-agent output unterminated.
// Every run of main-agent activity it emits — text, a tool call, a thinking
// block, anything with no ParentToolUseID — must be followed by an
// EventTurnEnd before the adapter emits further main-agent activity, and
// before the stream closes.
//
// It exists because the session runner's agent state has exactly two exits,
// an EventTurnEnd or the stream closing, and both are the adapter's to give.
// An adapter that emits activity it never ends leaves the session asserting a
// turn in flight with nothing able to retire the claim. That is not
// hypothetical: on 2026-09-30 a background task finished after the user's turn
// had closed, the claude CLI ran a whole unprompted turn, and the adapter
// filtered its result line as an in-turn continuation — correct inside a turn,
// wrong outside one. Two sessions read "Thinking…" for 23 hours with healthy,
// idle agents behind them (fixed in 187007d).
const TurnTermination provider.Behavior = "turn_termination"

// Result is one behavior's verdict: how it is declared, and the mismatch if
// the probe disagreed (nil Err = declaration and behavior agree). Note
// records probes the suite could not run meaningfully, so a pass is never
// silently thinner than it looks.
type Result struct {
	Behavior provider.Behavior
	Declared provider.Support
	Err      error
	Note     string
}

// Run executes the suite under t, one subtest per behavior.
func Run(t *testing.T, p provider.Provider, s Script) {
	t.Helper()
	for _, res := range Check(context.Background(), p, s) {
		t.Run(string(res.Behavior), func(t *testing.T) {
			if res.Note != "" {
				t.Log(res.Note)
			}
			if res.Err != nil {
				if res.Declared == "" {
					t.Errorf("%v", res.Err) // structural check, not a matrix row
				} else {
					t.Errorf("declared %q: %v", res.Declared, res.Err)
				}
			}
		})
	}
}

// Check probes every behavior and returns the full verdict list. A
// structurally invalid matrix short-circuits to a single MatrixResult entry —
// there is nothing meaningful to probe against.
func Check(ctx context.Context, p provider.Provider, s Script) []Result {
	m := p.Matrix()
	if err := m.Validate(); err != nil {
		return []Result{{Behavior: MatrixResult, Err: err}}
	}
	c := checker{p: p, m: m, s: s, ctx: ctx}

	// Transport rows first; they decide how everything else is probed.
	acpErr := c.transport(provider.ModeACP, provider.BehaviorACP)
	ptyErr := c.transport(provider.ModePTY, provider.BehaviorPTY)

	structured := m[provider.BehaviorACP].Works() && acpErr == nil
	switch {
	case m[provider.BehaviorACP].Works():
		c.add(provider.BehaviorInteractive, acpErr, "")
	default:
		c.add(provider.BehaviorInteractive, ptyErr, "probed via pty (no structured transport)")
	}

	if structured {
		c.add(provider.BehaviorInterrupt, c.interrupt(), "")
	} else {
		c.add(provider.BehaviorInterrupt, nil,
			"not probed: interrupt verification needs the structured transport")
	}

	oneShot, oneShotErr := c.oneShot()
	c.add(provider.BehaviorOneShot, oneShotErr, "")
	c.add(provider.BehaviorOneShotStream, c.oneShotStream(), "")

	if m[provider.BehaviorResume].Works() {
		if structured {
			c.add(provider.BehaviorResume, c.resume(), "")
		} else {
			c.add(provider.BehaviorResume, nil,
				"not probed: resume verification needs the structured transport")
		}
	} else {
		c.add(provider.BehaviorResume, c.resumeRefused(), "")
	}

	if structured {
		c.add(provider.BehaviorRefAnnounce, c.refAnnounce(), "")
	} else {
		c.add(provider.BehaviorRefAnnounce, nil,
			"not probed: ref announcement verification needs the structured transport")
	}

	c.add(provider.BehaviorTranscript, c.transcript(structured), "")
	c.add(provider.BehaviorModelDiscovery, c.models(), "")

	if m[provider.BehaviorCostReporting].Works() {
		switch {
		case oneShotErr != nil:
			c.add(provider.BehaviorCostReporting,
				errors.New("cannot verify: one_shot probe failed"), "")
		case oneShot.Usage.OutputTokens <= 0:
			c.add(provider.BehaviorCostReporting, fmt.Errorf(
				"declared working but one-shot reported usage %+v", oneShot.Usage), "")
		default:
			c.add(provider.BehaviorCostReporting, nil, "")
		}
	} else {
		c.add(provider.BehaviorCostReporting, nil, "")
	}

	// No functional probes yet — declaration validity only. They harden as
	// the first adapter claiming them lands.
	if m[provider.BehaviorBranching].Works() {
		c.add(provider.BehaviorBranching, nil, "not probed: fork needs a real prior session to copy")
	} else {
		c.add(provider.BehaviorBranching, c.forkRefused(), "")
	}
	c.add(provider.BehaviorCwdReroot, nil, "not probed: no functional probe defined yet")

	c.add(provider.BehaviorMCPServers, c.mcpServers(), "")

	switch {
	case !structured:
		c.add(TurnTermination, nil, "not probed: needs the structured transport")
	case s.Unprompted != nil:
		c.add(TurnTermination, c.turnTermination(s.Unprompted()), "")
	case s.Echo != nil:
		// Thinner, but not nothing: it still proves the adapter goes quiet
		// after a turn rather than trailing unterminated output. Only a
		// script that can elicit post-turn output probes the case that
		// actually broke.
		c.add(TurnTermination, c.turnTermination(s.Echo("settle")),
			"probed with the Echo prompt: no Unprompted script, so a post-turn continuation was never elicited")
	default:
		c.add(TurnTermination, nil, "not probed: no Unprompted or Echo script provided")
	}

	if m[provider.BehaviorTrustLimited].Works() {
		if s.AttemptCommand != nil {
			c.add(provider.BehaviorTrustLimited, c.trust(provider.TrustLimited, s.AttemptCommand), "")
		} else {
			c.add(provider.BehaviorTrustLimited, nil, "not probed: no AttemptCommand script provided")
		}
	} else {
		c.add(provider.BehaviorTrustLimited, nil, "declared unsupported: not probed for a negative")
	}
	if m[provider.BehaviorTrustReadOnly].Works() {
		if s.AttemptWrite != nil {
			c.add(provider.BehaviorTrustReadOnly, c.trust(provider.TrustReadOnly, s.AttemptWrite), "")
		} else {
			c.add(provider.BehaviorTrustReadOnly, nil, "not probed: no AttemptWrite script provided")
		}
	} else {
		c.add(provider.BehaviorTrustReadOnly, nil, "declared unsupported: not probed for a negative")
	}

	return c.results
}

type checker struct {
	p       provider.Provider
	m       provider.Matrix
	s       Script
	ctx     context.Context
	results []Result
}

func (c *checker) add(b provider.Behavior, err error, note string) {
	c.results = append(c.results, Result{Behavior: b, Declared: c.m[b], Err: err, Note: note})
}

// transport probes one transport row and records it: a working declaration
// must complete an echo round-trip in that mode; an unsupported one must
// refuse Start with ErrUnsupported.
func (c *checker) transport(mode provider.Mode, b provider.Behavior) error {
	var err error
	if c.m[b].Works() {
		if mode == provider.ModeACP {
			_, _, err = c.acpEcho("")
		} else {
			err = c.ptyEcho()
		}
	} else {
		sess, startErr := c.p.Start(c.ctx, provider.StartOptions{Mode: mode})
		switch {
		case startErr == nil:
			_ = sess.Close()
			err = fmt.Errorf("declared unsupported but Start(%s) succeeded", mode)
		case !errors.Is(startErr, provider.ErrUnsupported):
			err = fmt.Errorf("declared unsupported: Start(%s) must return ErrUnsupported, got: %v", mode, startErr)
		}
	}
	c.add(b, err, "")
	return err
}

// acpEcho runs one echo turn over the structured transport, optionally
// resuming, and returns the session (open, for follow-on probes).
func (c *checker) acpEcho(resume string) (provider.Session, *provider.Event, error) {
	sess, err := c.p.Start(c.ctx, provider.StartOptions{Mode: provider.ModeACP, Resume: resume})
	if err != nil {
		return nil, nil, fmt.Errorf("Start(acp): %w", err)
	}
	marker := "mk-echo-acp"
	text, end, err := c.turn(sess, c.s.Echo(marker))
	if err != nil {
		_ = sess.Close()
		return nil, nil, err
	}
	if !strings.Contains(text, marker) {
		_ = sess.Close()
		return nil, nil, fmt.Errorf("echo reply %q does not contain marker %q", text, marker)
	}
	return sess, end, nil
}

// turn sends one message and collects the streamed response through turn end.
func (c *checker) turn(sess provider.Session, prompt string) (string, *provider.Event, error) {
	if err := sess.Send(c.ctx, prompt); err != nil {
		return "", nil, fmt.Errorf("Send: %w", err)
	}
	return c.collect(sess)
}

func (c *checker) collect(sess provider.Session) (string, *provider.Event, error) {
	var text strings.Builder
	deadline := time.After(c.s.timeout())
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return text.String(), nil, errors.New("event stream closed before turn end (process died mid-turn?)")
			}
			switch ev.Kind {
			case provider.EventText:
				text.WriteString(ev.Text)
			case provider.EventTurnEnd:
				return text.String(), &ev, nil
			case provider.EventError:
				return text.String(), nil, fmt.Errorf("provider error mid-turn: %s", ev.Text)
			}
		case <-deadline:
			return text.String(), nil, fmt.Errorf("no turn end within %s", c.s.timeout())
		case <-c.ctx.Done():
			return text.String(), nil, c.ctx.Err()
		}
	}
}

// turnTermination drives a turn that keeps talking after it ends, then
// watches the stream settle. Anything the adapter emits as the main agent
// past the turn's end must itself be ended; the settle window is what
// distinguishes "still arriving" from "abandoned".
//
// Deliberately reads the stream rather than the adapter's source: the failure
// this guards against was an adapter dropping a terminal line it had already
// parsed, which no amount of reading the switch statement makes obvious.
func (c *checker) turnTermination(prompt string) error {
	sess, err := c.p.Start(c.ctx, provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		return fmt.Errorf("Start(acp): %w", err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(c.ctx, prompt); err != nil {
		return fmt.Errorf("Send: %w", err)
	}
	if _, _, err := c.collect(sess); err != nil {
		return fmt.Errorf("the prompt's own turn: %w", err)
	}

	// Past the turn's end. Keep reading: if the adapter speaks again as the
	// main agent, that run owes an end too.
	open := false
	var opener provider.EventKind
	settle := time.After(c.s.timeout())
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				// The stream closing IS an end — the runner treats it as one
				// (flushTurnState). Nothing is left asserting anything.
				return nil
			}
			if ev.ParentToolUseID != "" {
				continue // a subagent's sidechain is not the main agent's turn
			}
			switch ev.Kind {
			case provider.EventText, provider.EventToolUse, provider.EventThinking:
				if !open {
					open, opener = true, ev.Kind
				}
			case provider.EventTurnEnd:
				open = false
			}
		case <-settle:
			if open {
				return fmt.Errorf(
					"emitted main-agent %s after the turn ended and never ended it: the session would assert a turn in flight with nothing able to retire the claim",
					opener)
			}
			return nil
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
	}
}

// ptyEcho completes an echo round-trip over the terminal transport.
func (c *checker) ptyEcho() error {
	sess, err := c.p.Start(c.ctx, provider.StartOptions{Mode: provider.ModePTY})
	if err != nil {
		return fmt.Errorf("Start(pty): %w", err)
	}
	defer func() { _ = sess.Close() }()
	pty, ok := sess.(provider.PTYSession)
	if !ok {
		return errors.New("pty declared working but session does not implement provider.PTYSession")
	}
	marker := "mk-echo-pty"
	if err := sess.Send(c.ctx, c.s.Echo(marker)); err != nil {
		return fmt.Errorf("Send: %w", err)
	}
	return readUntil(pty.Terminal(), marker, c.s.timeout())
}

// interrupt starts a deliberately slow turn, interrupts it mid-stream, and
// requires both that the turn ends promptly and that the session survives to
// serve another turn.
func (c *checker) interrupt() error {
	sess, err := c.p.Start(c.ctx, provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		return fmt.Errorf("Start(acp): %w", err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(c.ctx, c.s.Slow()); err != nil {
		return fmt.Errorf("Send: %w", err)
	}
	// Wait for streaming to begin so the interrupt lands mid-turn.
	select {
	case ev, ok := <-sess.Events():
		if !ok {
			return errors.New("stream closed before slow turn produced anything")
		}
		if ev.Kind == provider.EventTurnEnd {
			return errors.New("slow prompt finished before the interrupt could land — Script.Slow is not slow enough to probe with")
		}
	case <-time.After(c.s.timeout()):
		return fmt.Errorf("slow turn produced no events within %s", c.s.timeout())
	}
	if err := sess.Interrupt(c.ctx); err != nil {
		return fmt.Errorf("Interrupt: %w", err)
	}
	if _, _, err := c.collect(sess); err != nil {
		return fmt.Errorf("turn did not end after interrupt: %w", err)
	}
	// The session must survive the interrupt.
	marker := "mk-after-interrupt"
	text, _, err := c.turn(sess, c.s.Echo(marker))
	if err != nil {
		return fmt.Errorf("session dead after interrupt: %w", err)
	}
	if !strings.Contains(text, marker) {
		return fmt.Errorf("post-interrupt echo reply %q lost marker %q", text, marker)
	}
	return nil
}

func (c *checker) oneShot() (*provider.OneShotResult, error) {
	marker := "mk-oneshot"
	res, err := c.p.OneShot(c.ctx, provider.OneShotRequest{Prompt: c.s.Echo(marker)})
	if err != nil {
		return nil, fmt.Errorf("OneShot: %w", err)
	}
	if !strings.Contains(res.Text, marker) {
		return res, fmt.Errorf("one-shot reply %q does not contain marker %q", res.Text, marker)
	}
	return res, nil
}

// oneShotStream probes BehaviorOneShotStream: a declared-working provider
// must call OnEvent at least once with text, with the collected chunks
// containing the marker; a declared-unsupported provider must never call it
// at all.
func (c *checker) oneShotStream() error {
	marker := "mk-oneshot-stream"
	var chunks []string
	_, err := c.p.OneShot(c.ctx, provider.OneShotRequest{
		Prompt: c.s.Echo(marker),
		OnEvent: func(ev provider.Event) {
			if ev.Kind == provider.EventText {
				chunks = append(chunks, ev.Text)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("OneShot: %w", err)
	}
	working := c.m[provider.BehaviorOneShotStream].Works()
	switch {
	case working && len(chunks) == 0:
		return errors.New("declared working but OnEvent was never called")
	case working && !strings.Contains(strings.Join(chunks, ""), marker):
		return fmt.Errorf("streamed chunks %q do not contain marker %q", strings.Join(chunks, ""), marker)
	case !working && len(chunks) != 0:
		return fmt.Errorf("declared unsupported but OnEvent fired %d times", len(chunks))
	}
	return nil
}

// resume proves conversation context survives a close/reattach cycle: a
// marker sent before the close must be recallable after it.
func (c *checker) resume() error {
	sess, err := c.p.Start(c.ctx, provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		return fmt.Errorf("Start(acp): %w", err)
	}
	marker := "mk-resume"
	if _, _, err := c.turn(sess, c.s.Echo(marker)); err != nil {
		_ = sess.Close()
		return fmt.Errorf("pre-resume turn: %w", err)
	}
	ref := sess.Ref()
	if ref == "" {
		_ = sess.Close()
		return errors.New("resume declared working but Session.Ref is empty")
	}
	if err := sess.Close(); err != nil {
		return fmt.Errorf("Close: %w", err)
	}
	resumed, err := c.p.Start(c.ctx, provider.StartOptions{Mode: provider.ModeACP, Resume: ref})
	if err != nil {
		return fmt.Errorf("Start(resume=%s): %w", ref, err)
	}
	defer func() { _ = resumed.Close() }()
	text, _, err := c.turn(resumed, c.s.Recall())
	if err != nil {
		return fmt.Errorf("recall turn: %w", err)
	}
	if !strings.Contains(text, marker) {
		return fmt.Errorf("resumed session lost context: recall reply %q missing marker %q", text, marker)
	}
	return nil
}

// refAnnounce probes BehaviorRefAnnounce over ACP: a declared-working
// provider must make Ref() non-empty, or announce one via RefAnnouncer,
// strictly before a full echo turn completes. A declared-unsupported
// provider must never do either.
func (c *checker) refAnnounce() error {
	sess, err := c.p.Start(c.ctx, provider.StartOptions{Mode: provider.ModeACP})
	if err != nil {
		return fmt.Errorf("Start(acp): %w", err)
	}
	defer func() { _ = sess.Close() }()

	working := c.m[provider.BehaviorRefAnnounce].Works()

	if ref := sess.Ref(); ref != "" {
		if !working {
			return errors.New("declared unsupported but Ref() is non-empty at Start-return")
		}
		return nil
	}

	announcer, ok := sess.(provider.RefAnnouncer)
	if !ok {
		if working {
			return errors.New("declared working but Ref() is empty and provider does not implement RefAnnouncer")
		}
		return nil
	}

	turnDone := make(chan struct{})
	go func() {
		_, _, _ = c.turn(sess, c.s.Echo("mk-refannounce"))
		close(turnDone)
	}()

	select {
	case ref, ok := <-announcer.RefAnnounced():
		if err := verifyRefAnnounced(working, ref, ok); err != nil {
			return err
		}
		<-turnDone // still require the turn to actually finish before returning
		return nil
	case <-turnDone:
		return refAnnounceAfterTurnDone(working, announcer.RefAnnounced(), sess.Ref)
	case <-time.After(c.s.timeout()):
		return fmt.Errorf("no ref announcement or turn end within %s", c.s.timeout())
	}
}

// verifyRefAnnounced checks one RefAnnounced delivery against the
// declaration. ref/ok are exactly what a receive from RefAnnounced()
// produced.
func verifyRefAnnounced(working bool, ref string, ok bool) error {
	if !working {
		if ok && ref != "" {
			return errors.New("declared unsupported but RefAnnounced delivered a ref")
		}
		return nil
	}
	if !ok || ref == "" {
		return errors.New("declared working but RefAnnounced closed with no ref")
	}
	return nil
}

// refAnnounceAfterTurnDone resolves the case where select observed turnDone
// as ready. That alone doesn't prove the provider announced late: select
// gives no priority to whichever channel became ready first in real time,
// so once both are ready it can land here even when the provider genuinely
// announced before completing the turn (a compliant provider's announce
// happens-before it can ever emit the turn-end event — see fakeSession.Send,
// providers/fake/fake.go, which delivers the ref before spawning its
// response goroutine). A single non-blocking read settles it for real:
// RefAnnounced's value, once sent, sits in the channel until read, and
// Ref() is guarded by the same lock the provider sets it under — both are
// monotonic, so if the provider already delivered, it's observable now
// regardless of how this goroutine was scheduled.
func refAnnounceAfterTurnDone(working bool, announced <-chan string, refFn func() string) error {
	select {
	case ref, ok := <-announced:
		return verifyRefAnnounced(working, ref, ok)
	default:
	}
	if r := refFn(); r != "" {
		if !working {
			return errors.New("declared unsupported but Ref() is non-empty after the turn completed")
		}
		return nil
	}
	if working {
		return errors.New("turn completed before ref was announced")
	}
	return nil
}

// resumeRefused checks the unsupported declaration is honest: a resume
// attempt must fail with ErrUnsupported.
func (c *checker) resumeRefused() error {
	mode := provider.ModeACP
	if !c.m[provider.BehaviorACP].Works() {
		mode = provider.ModePTY
	}
	sess, err := c.p.Start(c.ctx, provider.StartOptions{Mode: mode, Resume: "some-ref"})
	switch {
	case err == nil:
		_ = sess.Close()
		return errors.New("declared unsupported but Start with Resume succeeded")
	case !errors.Is(err, provider.ErrUnsupported):
		return fmt.Errorf("declared unsupported: resume must return ErrUnsupported, got: %v", err)
	}
	return nil
}

// forkRefused checks the unsupported declaration is honest: resuming with
// Fork set must fail with ErrUnsupported, never silently resume in place —
// that would append to a conversation the caller asked to leave untouched.
func (c *checker) forkRefused() error {
	mode := provider.ModeACP
	if !c.m[provider.BehaviorACP].Works() {
		mode = provider.ModePTY
	}
	sess, err := c.p.Start(c.ctx, provider.StartOptions{Mode: mode, Resume: "some-ref", Fork: true})
	switch {
	case err == nil:
		_ = sess.Close()
		return errors.New("declared unsupported but Start with Resume+Fork succeeded")
	case !errors.Is(err, provider.ErrUnsupported):
		return fmt.Errorf("declared unsupported: fork must return ErrUnsupported, got: %v", err)
	}
	return nil
}

// transcript checks both directions: declared working → the reader exists
// and reflects a real exchange; declared unsupported → it must not quietly
// work anyway.
func (c *checker) transcript(structured bool) error {
	reader, implemented := c.p.(provider.TranscriptReader)
	if c.m[provider.BehaviorTranscript].Works() {
		if !implemented {
			return errors.New("declared working but provider does not implement provider.TranscriptReader")
		}
		if !structured {
			return nil // echo-exchange verification needs the structured transport
		}
		sess, _, err := c.acpEcho("")
		if err != nil {
			return fmt.Errorf("transcript setup turn: %w", err)
		}
		ref := sess.Ref()
		_ = sess.Close()
		msgs, err := reader.Transcript(c.ctx, ref)
		if err != nil {
			return fmt.Errorf("Transcript: %w", err)
		}
		for _, msg := range msgs {
			if strings.Contains(msg.Text, "mk-echo-acp") {
				return nil
			}
		}
		return fmt.Errorf("transcript (%d messages) does not contain the exchange marker", len(msgs))
	}
	if implemented {
		if msgs, err := reader.Transcript(c.ctx, "any-ref"); err == nil && len(msgs) > 0 {
			return errors.New("declared unsupported but Transcript returns data")
		}
	}
	return nil
}

// trust starts a session at the given trust level and requires that prompt —
// which attempts a tool use the level should forbid — never COMPLETES the
// tool; the turn must still end (a refusal, not a hang or crash). An
// EventToolUse alone is fine: the announcement is the attempt, not the
// effect. Real goose announces the tool_call update before
// session/request_permission (goosecli/testdata/captures/acp-transcript.jsonl
// — request lines 34/44/66, each immediately preceded by its tool_call at
// 33/43/65), and the acprpc engine emits EventToolUse at the announcement —
// so a correctly denied call under goose still shows an EventToolUse
// followed by a failed tool_call_update (EventToolResult IsError). Only a
// non-error EventToolResult proves the disallowed tool actually ran.
func (c *checker) trust(level provider.Trust, prompt func() string) error {
	sess, err := c.p.Start(c.ctx, provider.StartOptions{Mode: provider.ModeACP, Trust: level})
	if err != nil {
		return fmt.Errorf("Start(acp, trust=%s): %w", level, err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(c.ctx, prompt()); err != nil {
		return fmt.Errorf("Send: %w", err)
	}
	deadline := time.After(c.s.timeout())
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return fmt.Errorf("trust %s: event stream closed before turn end", level)
			}
			if ev.Kind == provider.EventToolResult && !ev.IsError {
				name := ""
				if ev.Tool != nil {
					name = ev.Tool.Name
				}
				return fmt.Errorf("trust %s: disallowed tool completed successfully (name=%s)", level, name)
			}
			if ev.Kind == provider.EventTurnEnd {
				return nil
			}
		case <-deadline:
			return fmt.Errorf("trust %s: no turn end within %s", level, c.s.timeout())
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
	}
}

// mcpServers proves the field can never fail a session launch, regardless
// of declared support — a provider with no real wiring mechanism must
// silently drop the descriptor, never error (docs/LINKED-SESSIONS-DESIGN.md
// item 5; kirocli is the concrete case this protects). Verifying a
// "working" declaration actually threads the descriptor into real launch
// args/config is adapter-specific (argv/config shape differs per CLI) and
// lives in each adapter's own scripted-stand-in test, not here.
func (c *checker) mcpServers() error {
	mode := provider.ModeACP
	if !c.m[provider.BehaviorACP].Works() {
		mode = provider.ModePTY
	}
	sess, err := c.p.Start(c.ctx, provider.StartOptions{
		Mode:       mode,
		MCPServers: []provider.MCPServerRef{{Name: "conformance-probe", Command: "true", Env: []string{"FOO=bar"}}},
	})
	if err != nil {
		return fmt.Errorf("Start with MCPServers set: %w", err)
	}
	_ = sess.Close()
	return nil
}

func (c *checker) models() error {
	disc, implemented := c.p.(provider.ModelDiscoverer)
	if c.m[provider.BehaviorModelDiscovery].Works() {
		if !implemented {
			return errors.New("declared working but provider does not implement provider.ModelDiscoverer")
		}
		models, err := disc.Models(c.ctx)
		if err != nil {
			return fmt.Errorf("Models: %w", err)
		}
		if len(models) == 0 {
			return errors.New("declared working but Models returned an empty list")
		}
		return nil
	}
	if implemented {
		if models, err := disc.Models(c.ctx); err == nil && len(models) > 0 {
			return errors.New("declared unsupported but Models returns data")
		}
	}
	return nil
}

// readUntil consumes r until marker appears or the deadline passes.
func readUntil(r io.Reader, marker string, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		var acc strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			acc.Write(buf[:n])
			if strings.Contains(acc.String(), marker) {
				done <- nil
				return
			}
			if err != nil {
				done <- fmt.Errorf("terminal closed before marker %q appeared (saw %q)", marker, acc.String())
				return
			}
		}
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("marker %q not seen in terminal within %s", marker, timeout)
	}
}
