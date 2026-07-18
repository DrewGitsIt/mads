package template

import (
	"strings"
	"testing"
)

var agents = map[string]bool{
	"architect": true, "executor": true, "code-reviewer": true,
}

func known(name string) bool { return agents[name] }

const goodSource = `
name: default-implementation
stages:
  - name: architect
    agent: architect
    model: claude-haiku-4-5-20251001
    when: '!contains(labels, "trivial")'
  - name: executor
    agent: executor
  - name: build
    command: make build && make test
    on_fail: { loop_back: executor, max_loops: 2 }
  - name: code_review
    agent: code-reviewer
    on_fail: { loop_back: executor, max_loops: 2, then: block }
  - name: merge
    command: git merge --no-ff
  - name: verify
    command: make test
    on_fail: { then: block }
`

func TestCheckAcceptsFullGrammar(t *testing.T) {
	tpl, verr := Check([]byte(goodSource), known)
	if verr != nil {
		t.Fatalf("valid template refused: %v", verr)
	}
	if len(tpl.Stages) != 6 {
		t.Fatalf("parsed %d stages, want 6", len(tpl.Stages))
	}
	if tpl.Stages[3].OnFail.Then != "block" || tpl.Stages[3].OnFail.MaxLoops != 2 {
		t.Errorf("on_fail lost in parse: %+v", tpl.Stages[3].OnFail)
	}
	if tpl.Stages[0].When == "" {
		t.Error("when condition lost in parse")
	}
	if tpl.Stages[0].Model != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %q, want claude-haiku-4-5-20251001", tpl.Stages[0].Model)
	}
	if tpl.Stages[1].Model != "" {
		t.Errorf("stage without model set got %q, want empty", tpl.Stages[1].Model)
	}
}

func TestValidateReportsEverythingAtOnce(t *testing.T) {
	src := `
name: broken
stages:
  - name: plan
    agent: nonexistent-agent
  - name: plan
    command: make build
    on_fail: { loop_back: later, max_loops: 0 }
  - name: both
    agent: executor
    command: echo hi
  - name: neither
  - name: bad_then
    command: make test
    on_fail: { loop_back: bad_then, max_loops: 1, then: explode }
  - name: bad_model
    command: make lint
    model: claude-haiku-4-5-20251001
`
	_, verr := Check([]byte(src), known)
	if verr == nil {
		t.Fatal("broken template accepted")
	}
	wantFragments := []string{
		"unknown agent",              // stage 0
		"duplicate stage name",       // stage 1
		"does not exist",             // loop target "later"
		"at least 1",                 // max_loops 0
		"not both",                   // stage 2
		"needs an agent",             // stage 3
		"not an earlier stage",       // self loop
		`only "block"`,               // then: explode
		"only valid on agent stages", // stage 5 model on a command stage
	}
	all := verr.Error()
	for _, frag := range wantFragments {
		if !strings.Contains(all, frag) {
			t.Errorf("missing failure %q in: %s", frag, all)
		}
	}
	if len(verr.Errors) < len(wantFragments) {
		t.Errorf("want >= %d errors together (spec §10.3), got %d", len(wantFragments), len(verr.Errors))
	}
}

func TestForwardLoopRefused(t *testing.T) {
	src := `
name: fwd
stages:
  - name: a
    command: 'true'
    on_fail: { loop_back: b, max_loops: 1 }
  - name: b
    command: 'true'
`
	_, verr := Check([]byte(src), known)
	if verr == nil || !strings.Contains(verr.Error(), "not an earlier stage") {
		t.Fatalf("forward loop_back accepted: %v", verr)
	}
}

func TestAutoAgentAllowed(t *testing.T) {
	src := "name: auto\nstages:\n  - name: work\n    agent: auto\n"
	if _, verr := Check([]byte(src), known); verr != nil {
		t.Fatalf("agent: auto should pass without registry resolution (spec §9): %v", verr)
	}
}

func TestBadYAMLIsAValidationError(t *testing.T) {
	_, verr := Check([]byte("name: [unclosed"), known)
	if verr == nil || len(verr.Errors) == 0 || verr.Errors[0].Field != "source" {
		t.Fatalf("YAML error not reported on source field: %v", verr)
	}
}
