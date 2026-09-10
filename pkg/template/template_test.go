package template

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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

// A template with no defaults:/timeout:/retry: at all — the pre-existing
// grammar — must still parse and validate exactly as it did before this
// feature landed, with a nil Defaults and nil Timeout/Retry on every stage.
func TestTemplateWithoutNewFieldsIsUnaffected(t *testing.T) {
	tpl, verr := Check([]byte(goodSource), known)
	if verr != nil {
		t.Fatalf("goodSource should still validate: %v", verr)
	}
	if tpl.Defaults != nil {
		t.Errorf("Defaults = %+v, want nil when no defaults: block is present", tpl.Defaults)
	}
	for i, s := range tpl.Stages {
		if s.Timeout != nil || s.Retry != nil {
			t.Errorf("stage %d: Timeout=%v Retry=%v, want nil", i, s.Timeout, s.Retry)
		}
	}
}

// A defaults: block, and a stage timeout:, must both parse, and the
// template must round-trip through Marshal with "15m" still spelled "15m"
// (see Duration.MarshalYAML) rather than nanoseconds or "15m0s".
func TestDefaultsAndTimeoutParseAndRoundTrip(t *testing.T) {
	src := `
name: with-defaults
defaults:
  timeout: 15m
  model: claude-haiku-4-5-20251001
  retry: { max: 1 }
stages:
  - name: plan
    agent: architect
    timeout: 2h30m
    retry: { max: 2, when: always, backoff: 30s }
`
	tpl, verr := Check([]byte(src), known)
	if verr != nil {
		t.Fatalf("valid defaults/timeout/retry template refused: %v", verr)
	}
	if tpl.Defaults.Timeout == nil || time.Duration(*tpl.Defaults.Timeout) != 15*time.Minute {
		t.Errorf("defaults.timeout = %v, want 15m", tpl.Defaults.Timeout)
	}
	if tpl.Defaults.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("defaults.model = %q", tpl.Defaults.Model)
	}
	if tpl.Defaults.Retry == nil || tpl.Defaults.Retry.Max != 1 {
		t.Errorf("defaults.retry = %+v, want max 1", tpl.Defaults.Retry)
	}
	st := tpl.Stages[0]
	if st.Timeout == nil || time.Duration(*st.Timeout) != 2*time.Hour+30*time.Minute {
		t.Errorf("stages[0].timeout = %v, want 2h30m", st.Timeout)
	}
	if st.Retry == nil || st.Retry.Max != 2 || st.Retry.When != "always" || time.Duration(st.Retry.Backoff) != 30*time.Second {
		t.Errorf("stages[0].retry = %+v, want max 2, when always, backoff 30s", st.Retry)
	}

	out, err := yaml.Marshal(tpl)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{"timeout: 15m\n", "timeout: 2h30m\n", "backoff: 30s\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("marshaled YAML missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "900000000000") || strings.Contains(got, "15m0s") {
		t.Errorf("marshaled YAML rewrote a duration into nanoseconds or a padded form; got:\n%s", got)
	}
}

// A bad duration must report as a field error on the right stage index,
// together with any OTHER errors present in the same template (spec
// §10.3) — never as a lone parse failure that masks the rest.
func TestBadTimeoutReportsAlongsideOtherErrors(t *testing.T) {
	src := `
name: broken
stages:
  - name: a
    agent: nonexistent-agent
  - name: b
    command: 'true'
  - name: c
    command: 'true'
    timeout: banana
  - name: d
    command: 'true'
    timeout: 0
`
	_, verr := Check([]byte(src), known)
	if verr == nil {
		t.Fatal("broken template accepted")
	}
	wantFragments := []string{
		"unknown agent",        // stage 0
		"stages[2].timeout",    // bad duration string
		"not a valid duration", // bad duration string, message
		"stages[3].timeout",    // zero duration
		"greater than zero",    // zero duration, message
	}
	all := verr.Error()
	for _, frag := range wantFragments {
		if !strings.Contains(all, frag) {
			t.Errorf("missing failure %q in: %s", frag, all)
		}
	}
}

// retry: is rejected outright on the merge stage (the stage whose command
// starts with "git merge") — the land queue already retries its own
// rebase+gate cycle, and a template-level retry on top would hold the FIFO
// land queue and the workspace lock across repeated cycles.
func TestRetryRejectedOnMergeStage(t *testing.T) {
	src := `
name: merge-retry
stages:
  - name: work
    agent: executor
  - name: merge
    command: git merge --no-ff
    retry: { max: 2 }
`
	_, verr := Check([]byte(src), known)
	if verr == nil || !strings.Contains(verr.Error(), "not allowed on the merge stage") {
		t.Fatalf("retry on the merge stage accepted: %v", verr)
	}
}

// retry.max is capped at 10 (a typo'd `max: 1000` must not be able to burn
// a day of tokens) and retry.when must be one of the known values.
func TestRetryMaxCapAndWhenEnum(t *testing.T) {
	src := `
name: bad-retry
stages:
  - name: a
    command: 'true'
    retry: { max: 11 }
  - name: b
    command: 'true'
    retry: { when: sometimes }
  - name: c
    command: 'true'
    retry: { max: -1 }
`
	_, verr := Check([]byte(src), known)
	if verr == nil {
		t.Fatal("out-of-range retry accepted")
	}
	wantFragments := []string{
		"stages[0].retry.max",
		"stages[1].retry.when",
		"stages[2].retry.max",
	}
	all := verr.Error()
	for _, frag := range wantFragments {
		if !strings.Contains(all, frag) {
			t.Errorf("missing failure %q in: %s", frag, all)
		}
	}
}

// Empty source (or one that's all comments/whitespace) must not turn into
// an opaque "not valid YAML" / io.EOF failure — POST /templates/validate
// with {"source":""} is reachable straight from a brand-new, untouched
// template in the editor, and must keep getting the ordinary, friendly
// required-field errors.
func TestEmptySourceIsFriendlyNotOpaque(t *testing.T) {
	for _, src := range []string{"", "   \n", "# just a comment\n"} {
		tpl, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("Parse(%q) returned a parse error, want a zero Template: %v", src, err)
		}
		verr := Validate(tpl, known)
		if verr == nil {
			t.Fatalf("Parse(%q) then Validate: want name/stages errors, got none", src)
		}
		all := verr.Error()
		for _, frag := range []string{"name: required", "at least one stage required"} {
			if !strings.Contains(all, frag) {
				t.Errorf("Parse(%q): missing %q in: %s", src, frag, all)
			}
		}
	}
}

// The seeded system default template (services/data's
// migrations/0006_templates.up.sql, id tpl_system_default) must keep
// validating cleanly once unknown fields are rejected — it is the one
// template every workspace falls back to, so a false positive here would
// be catastrophic. This is the exact source string embedded in that
// migration's escaped JSON.
func TestSeededDefaultTemplateStillValidates(t *testing.T) {
	src := "name: default-implementation\n" +
		"stages:\n" +
		"  - name: architect\n" +
		"    agent: architect\n" +
		"    when: '!contains(labels, \"trivial\")'\n" +
		"  - name: executor\n" +
		"    agent: executor\n" +
		"  - name: build\n" +
		"    command: make build && make test\n" +
		"    on_fail: { loop_back: executor, max_loops: 2 }\n" +
		"  - name: code_review\n" +
		"    agent: code-reviewer\n" +
		"    on_fail: { loop_back: executor, max_loops: 2, then: block }\n" +
		"  - name: merge\n" +
		"    command: git merge --no-ff\n" +
		"  - name: verify\n" +
		"    command: make test\n" +
		"    on_fail: { then: block }\n"
	if _, verr := Check([]byte(src), known); verr != nil {
		t.Fatalf("seeded default-implementation template must still validate: %v", verr)
	}
}

// A near-miss spelling that isn't one of the seeded aspirational keys
// should still get a plausible did-you-mean, via edit distance against the
// real field names of whichever type yaml.v3 named in its error.
func TestUnknownFieldEditDistanceSuggestion(t *testing.T) {
	src := `
name: broken
stages:
  - name: a
    agent: architect
    retires: { max: 2 }
`
	_, verr := Check([]byte(src), known)
	if verr == nil {
		t.Fatal("unknown field accepted")
	}
	if len(verr.Errors) != 1 || verr.Errors[0].Field != "source" {
		t.Fatalf("want a single source error, got: %v", verr)
	}
	got := verr.Errors[0].Message
	for _, want := range []string{"line 6", `unknown field "retires"`, `did you mean "retry"?`} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q missing %q", got, want)
		}
	}
}

// Every seeded aspirational spelling from docs/REBUILD-SPEC.md §6's example
// YAML gets its own specific message — either a direct rename or an
// explanation that the concept isn't supported — rather than whatever
// edit distance happens to guess.
func TestSeededAspirationalSpellingsGetSpecificMessages(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"pipeline", "pipeline: {}\nname: x\nstages: [{name: a, command: 'true'}]\n",
			`unknown field "pipeline" — the top-level wrapper isn't used; stages live at the document root`},
		{"id", "name: x\nstages:\n  - id: a\n    command: 'true'\n",
			`unknown field "id" — did you mean "name"?`},
		{"type", "name: x\nstages:\n  - name: a\n    type: agent\n    command: 'true'\n",
			`unknown field "type" — a stage is an agent OR a command; set one of those instead`},
		{"run", "name: x\nstages:\n  - name: a\n    run: 'true'\n",
			`unknown field "run" — did you mean "command"?`},
		{"on_failure", "name: x\nstages:\n  - name: a\n    command: 'true'\n    on_failure: { then: block }\n",
			`unknown field "on_failure" — did you mean "on_fail"?`},
		{"goto", "name: x\nstages:\n  - name: a\n    command: 'true'\n    on_fail: { goto: a, max_loops: 1 }\n",
			`unknown field "goto" — did you mean "loop_back"?`},
		{"reads", "name: x\nstages:\n  - name: a\n    command: 'true'\n    reads: [task]\n",
			`unknown field "reads" — not supported; stages exchange context via task handoffs`},
		{"produces", "name: x\nstages:\n  - name: a\n    command: 'true'\n    produces: [out]\n",
			`unknown field "produces" — not supported; stages exchange context via task handoffs`},
		{"different_agent", "name: x\nstages:\n  - name: a\n    agent: architect\n    retry: { max: 1, different_agent: true }\n",
			`unknown field "different_agent" — not supported; retry uses the same agent`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := Check([]byte(tc.src), known)
			if verr == nil {
				t.Fatalf("%s: unknown field accepted", tc.name)
			}
			if !strings.Contains(verr.Error(), tc.want) {
				t.Errorf("%s: want message containing %q, got: %v", tc.name, tc.want, verr)
			}
		})
	}
}

// Unknown keys must be caught (and, per spec §10.3, all reported together
// in one response) at every nesting level the grammar has: top-level,
// stage, defaults, retry, and on_fail.
func TestUnknownFieldsCaughtAtEveryNestingLevel(t *testing.T) {
	src := `
name: x
bogus_top: 1
defaults:
  bogus_defaults: 1
  retry: { max: 1, bogus_retry: 1 }
stages:
  - name: a
    agent: architect
    bogus_stage: 1
    on_fail: { then: block, bogus_on_fail: 1 }
`
	_, verr := Check([]byte(src), known)
	if verr == nil {
		t.Fatal("unknown fields accepted")
	}
	wantFragments := []string{
		`unknown field "bogus_top"`,
		`unknown field "bogus_defaults"`,
		`unknown field "bogus_retry"`,
		`unknown field "bogus_stage"`,
		`unknown field "bogus_on_fail"`,
	}
	all := verr.Error()
	for _, frag := range wantFragments {
		if !strings.Contains(all, frag) {
			t.Errorf("missing failure %q in: %s", frag, all)
		}
	}
	if len(verr.Errors) < len(wantFragments) {
		t.Errorf("want all %d unknown fields reported together, got %d errors: %v", len(wantFragments), len(verr.Errors), verr)
	}
}
