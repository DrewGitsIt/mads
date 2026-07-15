// Package preflight defines the first-run wizard's environment health check
// (MADS-UX §3.9 Preflight step). W7.3's real implementation (git presence,
// node-pty availability, provider credential checks) does not exist yet —
// Checker is the seam it plugs into; StubChecker keeps the wizard usable in
// the meantime by always reporting healthy.
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
