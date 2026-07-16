// Package preflight defines the first-run wizard's and Settings' environment
// health check (spec §14.8-14.9). Checker is the seam both the real
// environment probes (New, in checker.go) and StubChecker plug into;
// StubChecker remains as a nil-default fallback and test fixture.
package preflight

import "context"

// Check is one environment health check's result.
type Check struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Detail  string `json:"detail,omitempty"`
	FixHint string `json:"fix_hint,omitempty"`
}

// Checker runs the wizard's environment checks.
type Checker interface {
	Run(ctx context.Context) ([]Check, error)
}

// StubChecker always reports every check healthy — a placeholder until
// W7.3's real environment probes land.
type StubChecker struct{}

// Run implements Checker.
func (StubChecker) Run(context.Context) ([]Check, error) {
	return []Check{
		{Name: "git", Healthy: true, Detail: "stubbed — real check pending W7.3"},
		{Name: "node-pty", Healthy: true, Detail: "stubbed — real check pending W7.3"},
		{Name: "provider:claude-code", Healthy: true, Detail: "stubbed — real check pending W7.3"},
	}, nil
}
