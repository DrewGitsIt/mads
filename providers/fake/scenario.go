package fake

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/drewkidwell/mads/providers/provider"
)

// Scenario scripts the fake agent. Rules are matched in order against each
// incoming prompt; the first match runs. Prompts no rule matches fall through
// to the built-in dialect (echo/slow/recall/crash/ratelimit), so a fake with
// any scenario still passes conformance.
type Scenario struct {
	Name   string           `yaml:"name"`
	Models []provider.Model `yaml:"models,omitempty"` // replaces the default list when set
	Rules  []Rule           `yaml:"rules,omitempty"`
}

// Rule is one scripted response.
type Rule struct {
	// Match is a substring the prompt must contain. Empty matches every
	// prompt — put catch-alls last.
	Match string `yaml:"match"`
	// Reply is the streamed response text. When Chunks is set, Reply is
	// ignored and the chunks stream one by one instead.
	Reply  string   `yaml:"reply,omitempty"`
	Chunks []string `yaml:"chunks,omitempty"`
	// DelayMs waits between chunks — the delay-on-cue lever for tests that
	// need an observable in-flight window.
	DelayMs int `yaml:"delay_ms,omitempty"`
	// Tools are emitted (and executed — a Content+Path tool writes the file
	// under the session cwd) before the reply streams.
	Tools []ToolStep `yaml:"tools,omitempty"`
	// Crash kills the fake agent mid-turn: any chunks emitted so far, then
	// the event stream closes with no turn end — the process-died signal
	// downstream supervision consumes.
	Crash bool `yaml:"crash,omitempty"`
	// RateLimit makes the turn fail with *provider.RateLimitError instead
	// of running (§11.9 inform-and-wait paths).
	RateLimit bool `yaml:"rate_limit,omitempty"`
}

// ToolStep is one scripted tool invocation.
type ToolStep struct {
	Name string `yaml:"name"`
	// Path marks the tool as touching a file (§5 attribution origin
	// tool_use). With Content set, the file is really written relative to
	// the session's cwd so downstream file-tracking tests see real changes.
	Path    string `yaml:"path,omitempty"`
	Content string `yaml:"content,omitempty"`
	Input   string `yaml:"input,omitempty"` // raw JSON for the event; defaults to {}
}

// Validate reports every problem at once (§10.3 ethos).
func (sc Scenario) Validate() error {
	var probs []string
	for i, r := range sc.Rules {
		if r.Crash && r.RateLimit {
			probs = append(probs, fmt.Sprintf("rule %d: crash and rate_limit are mutually exclusive", i))
		}
		for j, ts := range r.Tools {
			if ts.Name == "" {
				probs = append(probs, fmt.Sprintf("rule %d tool %d: name is required", i, j))
			}
			if ts.Content != "" && ts.Path == "" {
				probs = append(probs, fmt.Sprintf("rule %d tool %d: content without a path", i, j))
			}
		}
		if r.Match == "" && i != len(sc.Rules)-1 {
			probs = append(probs, fmt.Sprintf("rule %d: catch-all (empty match) shadows later rules", i))
		}
	}
	if len(probs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid scenario %q: %s", sc.Name, strings.Join(probs, "; "))
}

// LoadScenario reads a scenario file (YAML).
func LoadScenario(path string) (Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	var sc Scenario
	if err := yaml.Unmarshal(raw, &sc); err != nil {
		return Scenario{}, fmt.Errorf("scenario %s: %w", path, err)
	}
	if err := sc.Validate(); err != nil {
		return Scenario{}, err
	}
	return sc, nil
}

// match returns the first rule matching prompt, or nil for built-in handling.
func (sc Scenario) match(prompt string) *Rule {
	for i := range sc.Rules {
		if sc.Rules[i].Match == "" || strings.Contains(prompt, sc.Rules[i].Match) {
			return &sc.Rules[i]
		}
	}
	return nil
}
