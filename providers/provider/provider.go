package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrUnsupported is returned by any call exercising a behavior the provider's
// matrix declares unsupported. Callers gate features on it; conformance
// asserts it appears exactly where the matrix says it should.
var ErrUnsupported = errors.New("behavior unsupported by provider")

// ErrDiscoveryUnavailable marks a ModelDiscoverer failure that's an expected
// runtime condition — the provider's binary is missing or exec itself
// failed (the same class of problem pkg/preflight checks for ahead of time)
// — as opposed to a genuine internal error. Handlers that surface discovery
// failures to callers degrade to an empty list on this, and only this.
var ErrDiscoveryUnavailable = errors.New("model discovery unavailable")

// RateLimitError is the typed surfacing of a provider rate limit (spec §11.9:
// inform and wait — the platform never silently retry-loops).
type RateLimitError struct {
	RetryAfter time.Duration // zero when the provider didn't say
	Message    string
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate limited: %s (retry after %s)", e.Message, e.RetryAfter)
	}
	return "rate limited: " + e.Message
}

// Mode selects the transport for an interactive session (spec §11):
// ACP-style structured events, or a raw terminal.
type Mode string

const (
	ModeACP Mode = "acp"
	ModePTY Mode = "pty"
)

// StartOptions configures an interactive session. Resume carries the
// provider-native ref of a previous session (Session.Ref); empty starts
// fresh. Resuming is the same spawn path as starting — that is how every
// real harness works — so it lives here rather than on a separate method.
type StartOptions struct {
	Mode         Mode
	Model        string
	Cwd          string
	SystemPrompt string   // the agent definition's prompt (registry, spec §9)
	Resume       string   // provider session ref to reattach to
	Env          []string // extra environment, KEY=value
}

// OneShotRequest is a single non-interactive invocation — the Fast Model
// baseline (spec §11): prompt in, final text out, no session left behind.
type OneShotRequest struct {
	Prompt string
	System string
	Model  string
	Cwd    string
}

// OneShotResult is the completed invocation.
type OneShotResult struct {
	Text  string `json:"text"`
	Usage Usage  `json:"usage"`
}

// Model is one selectable model (Launchpad model popover, spec §11.5).
type Model struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default,omitempty"`
}

// Provider is the core interface every adapter implements. Behaviors that
// vary by harness (transcript access, model discovery) are optional strategy
// interfaces below — the matrix declares which are present, and conformance
// holds the two in agreement.
type Provider interface {
	// Name is the registry key ("claude-code", "fake", ...).
	Name() string
	// Matrix declares support for every known behavior. Must validate.
	Matrix() Matrix
	// Start launches (or with opts.Resume, reattaches to) an interactive
	// session in the requested mode. ErrUnsupported for an unsupported mode
	// or an unsupported resume.
	Start(ctx context.Context, opts StartOptions) (Session, error)
	// OneShot runs a single non-interactive invocation to completion.
	OneShot(ctx context.Context, req OneShotRequest) (*OneShotResult, error)
}

// Session is a live interactive agent conversation. One Send begins a turn;
// the response streams as Events and finishes with EventTurnEnd. Sessions
// survive Interrupt (the turn ends, the session lives). Close terminates the
// underlying process; Ref stays valid for StartOptions.Resume afterward.
type Session interface {
	// Ref is the provider-native handle used to resume this session later.
	Ref() string
	Mode() Mode
	Send(ctx context.Context, text string) error
	// Interrupt stops the in-flight response. The turn ends (EventTurnEnd
	// with Interrupted set where the transport can express it) and the
	// session remains usable.
	Interrupt(ctx context.Context) error
	// Events is the session's ordered event stream. It closes when the
	// session ends — after Close, or when the underlying process exits.
	// A close with no preceding EventTurnEnd means the process died
	// mid-turn (the Router's supervision signal). PTY sessions may return
	// nil; their output flows through Terminal instead.
	Events() <-chan Event
	Close() error
}

// PTYSession is the terminal transport: the same Session contract with the
// byte stream exposed. Send still injects user messages programmatically;
// Terminal carries everything the harness renders.
type PTYSession interface {
	Session
	Terminal() io.ReadWriter
	Resize(cols, rows int) error
}

// ModelDiscoverer is the optional strategy for BehaviorModelDiscovery.
type ModelDiscoverer interface {
	Models(ctx context.Context) ([]Model, error)
}

// TranscriptReader is the optional strategy for BehaviorTranscript: session
// history in normalized form, by provider-native ref.
type TranscriptReader interface {
	Transcript(ctx context.Context, ref string) ([]Message, error)
}
