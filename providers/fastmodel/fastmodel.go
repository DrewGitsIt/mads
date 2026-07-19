// Package fastmodel is the one-shot LLM utility every transient job rides
// (spec §11): task synthesis, summaries, retro steps, NL agent generation.
// Prompt in, structured JSON out, with validation and bounded feedback
// retries.
//
// Two transports, invisible to callers: the baseline goes through the
// provider layer (any registered provider's OneShot), and when Settings
// configures an OpenAI-compatible endpoint the same jobs go straight to it.
// Rate limits are never retried — the typed error bubbles up so the platform
// can inform and wait (§11.9).
package fastmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/drewkidwell/mads/providers/provider"
)

// ErrUnusableOutput is the error Do returns after MaxAttempts when the
// backend never produced parseable, valid JSON — the shape an unconfigured
// backend (e.g. providers/fake's "ok" fallback, standing in for the Fast
// Model until -fastmodel-base-url is set) always takes. Distinct from
// transport failures so best-effort callers (e.g. session summaries) can
// treat "the backend can't do structured output" as "nothing to report"
// rather than a hard failure.
var ErrUnusableOutput = errors.New("fastmodel: no usable structured output")

// Job is one transient invocation.
type Job struct {
	System string
	Prompt string
	Model  string // overrides the runner's default
}

// Validator lets output types veto semantically-bad JSON; failures feed the
// retry prompt, mirroring the validate-all-at-once ethos (§10.3).
type Validator interface {
	Validate() error
}

// Backend is the transport: baseline provider one-shots, or the
// OpenAI-compatible endpoint.
type Backend interface {
	Complete(ctx context.Context, system, prompt, model string) (string, provider.Usage, error)
}

// Config tunes a Runner.
type Config struct {
	// Model is the default fast model (empty = the backend's default).
	Model string
	// MaxAttempts bounds structured-output retries (default 3).
	MaxAttempts int
	// BaseURL, when set, flips the transport to an OpenAI-compatible
	// endpoint (spec §11 optimization). Callers don't change.
	BaseURL string
	// APIKeyEnv names the environment variable holding the endpoint
	// credential — names only, never values, cross service boundaries
	// (spec §13).
	APIKeyEnv string
}

type Runner struct {
	backend     Backend
	model       string
	maxAttempts int
}

// New builds a runner over p, unless cfg.BaseURL selects the endpoint path.
func New(p provider.Provider, cfg Config) *Runner {
	var b Backend
	if cfg.BaseURL != "" {
		b = &openAIBackend{baseURL: cfg.BaseURL, apiKeyEnv: cfg.APIKeyEnv}
	} else {
		b = &providerBackend{p: p}
	}
	return NewWithBackend(b, cfg)
}

// NewWithBackend builds a runner over an explicit transport.
func NewWithBackend(b Backend, cfg Config) *Runner {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	return &Runner{backend: b, model: cfg.Model, maxAttempts: cfg.MaxAttempts}
}

// DoText runs a plain-text job: one attempt, no JSON handling.
func (r *Runner) DoText(ctx context.Context, job Job) (string, provider.Usage, error) {
	return r.backend.Complete(ctx, job.System, job.Prompt, r.jobModel(job))
}

// Do runs a structured job: the reply must contain JSON that unmarshals into
// out (a pointer) and passes its Validate, if it has one. Parse and
// validation failures are fed back to the model and retried up to
// MaxAttempts; transport failures and rate limits are not retried. Usage
// accumulates across attempts — every token spent is accounted for (§8).
func (r *Runner) Do(ctx context.Context, job Job, out any) (provider.Usage, error) {
	var total provider.Usage
	prompt := job.Prompt
	var lastErr error
	for attempt := 1; attempt <= r.maxAttempts; attempt++ {
		text, usage, err := r.backend.Complete(ctx, job.System, prompt, r.jobModel(job))
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens
		if total.Model == "" {
			total.Model = usage.Model
		}
		if err != nil {
			return total, err // transport / rate limit: the caller's problem, not a retry
		}
		lastErr = decode(text, out)
		if lastErr == nil {
			return total, nil
		}
		prompt = fmt.Sprintf(
			"%s\n\nYour previous reply could not be used: %v.\nReply with ONLY valid JSON of the requested shape — no prose, no code fences.",
			job.Prompt, lastErr)
	}
	return total, fmt.Errorf("%w after %d attempts: %v", ErrUnusableOutput, r.maxAttempts, lastErr)
}

func (r *Runner) jobModel(job Job) string {
	if job.Model != "" {
		return job.Model
	}
	return r.model
}

// decode extracts the reply's JSON payload, unmarshals, and validates.
func decode(text string, out any) error {
	raw, err := extractJSON(text)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if v, ok := out.(Validator); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("JSON parsed but failed validation: %w", err)
		}
	}
	return nil
}

// extractJSON finds the first balanced JSON object or array in text — models
// wrap payloads in prose and code fences no matter how firmly asked not to.
func extractJSON(text string) (string, error) {
	start := -1
	for i, c := range text {
		if c == '{' || c == '[' {
			start = i
			break
		}
	}
	if start < 0 {
		return "", errors.New("reply contains no JSON object or array")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		c := text[i]
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
			if depth == 0 {
				return text[start : i+1], nil
			}
		}
	}
	return "", errors.New("reply's JSON is unbalanced (truncated?)")
}

// providerBackend is the baseline: any provider's one-shot path.
type providerBackend struct {
	p provider.Provider
}

func (b *providerBackend) Complete(ctx context.Context, system, prompt, model string) (string, provider.Usage, error) {
	res, err := b.p.OneShot(ctx, provider.OneShotRequest{Prompt: prompt, System: system, Model: model})
	if err != nil {
		return "", provider.Usage{}, err
	}
	return res.Text, res.Usage, nil
}
