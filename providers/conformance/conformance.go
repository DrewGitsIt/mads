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
				t.Errorf("declared %q: %v", res.Declared, res.Err)
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
	c.add(provider.BehaviorBranching, nil, "not probed: no functional probe defined yet")
	c.add(provider.BehaviorCwdReroot, nil, "not probed: no functional probe defined yet")

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
