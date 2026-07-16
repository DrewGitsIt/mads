// Package template parses and validates pipeline templates (spec §6). It is
// shared by the Data Service (POST /templates/validate, saves) and the
// Pipeline Engine (launch) so "Validate" in the editor runs literally the
// same checks as starting a run — one artifact, one grammar, one judge.
//
// A stage's `when:` guard is parsed by ParseCond; see Cond for the grammar.
package template

import (
	"fmt"

	"github.com/drewkidwell/mads/pkg/contracts"
	"gopkg.in/yaml.v3"
)

// Template is the parsed form of a template's YAML source.
type Template struct {
	Name   string  `yaml:"name"`
	Stages []Stage `yaml:"stages"`
}

// Stage is one pipeline stage: exactly one of Agent (registry agent name, or
// "auto" for auto-selection, spec §9) or Command (shell). When guards
// execution; OnFail describes the failure route.
type Stage struct {
	Name    string  `yaml:"name"`
	Agent   string  `yaml:"agent,omitempty"`
	Command string  `yaml:"command,omitempty"`
	When    string  `yaml:"when,omitempty"`
	OnFail  *OnFail `yaml:"on_fail,omitempty"`
}

// OnFail routes a failed stage (spec §6): loop back to an earlier stage up to
// MaxLoops times, then block the task (Then "block" — also the behavior when
// no loop target is given).
type OnFail struct {
	LoopBack string `yaml:"loop_back,omitempty"`
	MaxLoops int    `yaml:"max_loops,omitempty"`
	Then     string `yaml:"then,omitempty"`
}

// Parse decodes YAML source. Parse errors are returned as a ValidationError
// on the "source" field so callers report them the same way as semantic ones.
func Parse(source []byte) (*Template, error) {
	var t Template
	if err := yaml.Unmarshal(source, &t); err != nil {
		verr := &contracts.ValidationError{}
		verr.Add("source", "not valid YAML: "+err.Error())
		return nil, verr
	}
	return &t, nil
}

// Validate runs every launch-time check and reports all failures together
// (spec §10.3, M9): stage shape, unique names, agent resolution, loop-back
// targets exist and point backward (stages are linear in v1 — a forward or
// self loop_back would be an unbounded cycle), sane max_loops, known `then`.
// knownAgent answers whether a registry agent name resolves; the Data
// Service backs it with the agent registry, tests with a set.
func Validate(t *Template, knownAgent func(string) bool) *contracts.ValidationError {
	var v contracts.ValidationError
	if t.Name == "" {
		v.Add("name", "required")
	}
	if len(t.Stages) == 0 {
		v.Add("stages", "at least one stage required")
	}

	index := map[string]int{}
	for i, s := range t.Stages {
		field := fmt.Sprintf("stages[%d]", i)
		if s.Name == "" {
			v.Add(field+".name", "required")
		} else if prev, dup := index[s.Name]; dup {
			v.Add(field+".name", fmt.Sprintf("duplicate stage name %q (also stages[%d])", s.Name, prev))
		} else {
			index[s.Name] = i
		}

		switch {
		case s.Agent != "" && s.Command != "":
			v.Add(field, "a stage is an agent OR a command, not both")
		case s.Agent == "" && s.Command == "":
			v.Add(field, "a stage needs an agent: or a command:")
		case s.Agent != "" && s.Agent != "auto" && !knownAgent(s.Agent):
			v.Add(field+".agent", fmt.Sprintf("unknown agent %q — not in the registry (spec §9)", s.Agent))
		}

		if _, err := ParseCond(s.When); err != nil {
			v.Add(field+".when", err.Error())
		}
	}

	for i, s := range t.Stages {
		if s.OnFail == nil {
			continue
		}
		field := fmt.Sprintf("stages[%d].on_fail", i)
		if s.OnFail.LoopBack != "" {
			target, ok := index[s.OnFail.LoopBack]
			switch {
			case !ok:
				v.Add(field+".loop_back", fmt.Sprintf("target stage %q does not exist", s.OnFail.LoopBack))
			case target >= i:
				v.Add(field+".loop_back",
					fmt.Sprintf("target %q is not an earlier stage — loops must point backward (spec §6)", s.OnFail.LoopBack))
			}
			if s.OnFail.MaxLoops < 1 {
				v.Add(field+".max_loops", "must be at least 1 when loop_back is set")
			}
		}
		if s.OnFail.Then != "" && s.OnFail.Then != "block" {
			v.Add(field+".then", fmt.Sprintf("only %q is supported, got %q", "block", s.OnFail.Then))
		}
	}

	if v.Empty() {
		return nil
	}
	return &v
}

// Check is the one-call form used at save and launch: parse, then validate.
func Check(source []byte, knownAgent func(string) bool) (*Template, *contracts.ValidationError) {
	t, err := Parse(source)
	if err != nil {
		verr := err.(*contracts.ValidationError)
		return nil, verr
	}
	if verr := Validate(t, knownAgent); verr != nil {
		return nil, verr
	}
	return t, nil
}
