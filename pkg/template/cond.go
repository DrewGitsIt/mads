package template

import (
	"fmt"
	"strings"
)

// Cond is a parsed `when:` guard. The v1 grammar is deliberately tiny
// (spec §6):
//
//	contains(labels, "x")
//	!contains(labels, "x")
//
// It lives here — not in the engine — so the template editor rejects exactly
// the expressions launch would reject (M9), and grows only by extending this
// one parser.
type Cond struct {
	negated bool
	arg     string
}

// ParseCond parses a when-expression. Empty input is the always-true guard.
func ParseCond(s string) (*Cond, error) {
	expr := strings.TrimSpace(s)
	if expr == "" {
		return &Cond{}, nil
	}
	c := &Cond{}
	if strings.HasPrefix(expr, "!") {
		c.negated = true
		expr = strings.TrimSpace(expr[1:])
	}
	rest, ok := strings.CutPrefix(expr, "contains")
	if !ok {
		return nil, fmt.Errorf("unknown when-expression %q: the supported form is [!]contains(labels, \"value\")", s)
	}
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "(") || !strings.HasSuffix(rest, ")") {
		return nil, fmt.Errorf("malformed when-expression %q: expected contains(labels, \"value\")", s)
	}
	parts := strings.SplitN(rest[1:len(rest)-1], ",", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("malformed when-expression %q: contains takes (labels, \"value\")", s)
	}
	if list := strings.TrimSpace(parts[0]); list != "labels" && list != "task.labels" {
		return nil, fmt.Errorf("when-expression %q: only the task's labels can be tested, got %q", s, list)
	}
	arg := strings.TrimSpace(parts[1])
	if len(arg) < 2 || (arg[0] != '"' && arg[0] != '\'') || arg[len(arg)-1] != arg[0] {
		return nil, fmt.Errorf("when-expression %q: the value must be a quoted string", s)
	}
	c.arg = arg[1 : len(arg)-1]
	if c.arg == "" {
		return nil, fmt.Errorf("when-expression %q: the value must not be empty", s)
	}
	return c, nil
}

// Eval answers the guard against a task's labels.
func (c *Cond) Eval(labels []string) bool {
	if c.arg == "" {
		return true // the always-true guard
	}
	found := false
	for _, l := range labels {
		if l == c.arg {
			found = true
			break
		}
	}
	if c.negated {
		return !found
	}
	return found
}
