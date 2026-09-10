// Package template parses and validates pipeline templates (spec §6). It is
// shared by the Data Service (POST /templates/validate, saves) and the
// Pipeline Engine (launch) so "Validate" in the editor runs literally the
// same checks as starting a run — one artifact, one grammar, one judge.
//
// A stage's `when:` guard is parsed by ParseCond; see Cond for the grammar.
package template

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"gopkg.in/yaml.v3"
)

// Template is the parsed form of a template's YAML source.
type Template struct {
	Name   string  `yaml:"name"`
	Stages []Stage `yaml:"stages"`
	// Defaults gives template-wide fallbacks a stage inherits when it does
	// not set its own timeout:/retry: (and, for Model, when a stage's agent
	// has no Preferences.Model either). Resolving the precedence between
	// these three levels is the engine's job (a separate change); this
	// package only parses and validates the grammar.
	Defaults *Defaults `yaml:"defaults,omitempty"`
}

// Defaults holds template-wide fallback settings (spec §6). Nil means the
// template set no defaults: block at all, distinct from an empty one — not
// that either currently changes validation, but it keeps Parse -> Marshal
// round-trips faithful to what the author wrote.
type Defaults struct {
	// Timeout is a pointer so nil unambiguously means "timeout: was
	// omitted" — distinct from a present-but-zero "timeout: 0", which
	// time.ParseDuration happily parses to the same underlying value a
	// plain Duration zero value would have. checkDuration relies on this:
	// nil skips the check entirely, non-nil is validated (and, per spec,
	// must be > 0).
	Timeout *Duration `yaml:"timeout,omitempty"`
	Model   string    `yaml:"model,omitempty"`
	Retry   *Retry    `yaml:"retry,omitempty"`
}

// Retry describes a bounded automatic re-run of a failed stage (spec §6).
//
// There is deliberately no "different_agent" field, even though the spec
// also wants "retry with a different agent": accepting a field here and
// silently ignoring it is exactly the bug this codebase is elsewhere trying
// to stamp out (a template author would have no way to know their retry
// policy wasn't doing what they wrote), so it's left out entirely and a
// template that writes it fails validation instead.
type Retry struct {
	Max int `yaml:"max"`
	// When selects which failures retry: "error" (the default, meaning
	// "retry"), "always" (retry even a stage that reported success — rare,
	// but useful for flaky-detection stages), or "never" (defined for
	// completeness/explicitness; equivalent to omitting retry: entirely).
	When    string   `yaml:"when,omitempty"`
	Backoff Duration `yaml:"backoff,omitempty"`
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
	// Model overrides the resolved agent's model for this stage only
	// (engine precedence: CLI -model > stage Model > agent Preferences.Model).
	// Agent stages only — freeform, unvalidated (the provider is the
	// authority on valid ids, spec §11's "fail loud" for bad models).
	Model string `yaml:"model,omitempty"`
	// Timeout overrides Defaults.Timeout (and the engine's own compiled-in
	// default) for this stage only. nil means "no override" — see
	// Defaults.Timeout for why this is a pointer rather than a plain
	// Duration.
	Timeout *Duration `yaml:"timeout,omitempty"`
	// Retry overrides Defaults.Retry for this stage only. It is rejected
	// outright on the merge stage — see isMergeStage and its use in
	// Validate.
	Retry *Retry `yaml:"retry,omitempty"`
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
// on the "source" field so callers report them the same way as semantic
// ones.
//
// This uses a Decoder with KnownFields(true) rather than yaml.Unmarshal so
// that a key the grammar doesn't recognize — a typo like `retires:`, or one
// of the aspirational spec §6 spellings (`on_failure:`, `type:`, `run:`, ...)
// that docs/REBUILD-SPEC.md's example predates the implementation with —
// fails loud instead of silently vanishing. Before this, such a template
// validated green and the field it named simply never took effect; the
// author had no way to know.
//
// IMPORTANT — do not add UnmarshalYAML to Template, Stage, Defaults, Retry,
// or OnFail. KnownFields is a property of *this* Decoder; it is not
// inherited by a nested type's custom UnmarshalYAML(node *yaml.Node), which
// (if it calls node.Decode) constructs its own fresh decoder internally —
// one with KnownFields left at its default of false. Add an UnmarshalYAML
// to any of these five types and unknown keys nested under it go back to
// being silently dropped, defeating this entire feature without a single
// test failing to say so (a hand-rolled UnmarshalYAML that decodes field by
// field wouldn't even look at the keys it doesn't recognize). That's why
// these types use plain field tags and *Duration pointers for presence
// tracking instead. Duration itself has an UnmarshalYAML, but it decodes a
// YAML scalar (a string), where KnownFields has no meaning anyway — so it's
// exempt from this trap.
func Parse(source []byte) (*Template, error) {
	var t Template
	dec := yaml.NewDecoder(bytes.NewReader(source))
	dec.KnownFields(true)
	err := dec.Decode(&t)
	if err == nil {
		return &t, nil
	}
	if errors.Is(err, io.EOF) {
		// An empty source (or one that's all comments/whitespace) has no
		// YAML document at all, so the Decoder reports io.EOF — unlike
		// yaml.Unmarshal, which quietly succeeded with a zero Template in
		// the same situation. Keep that old, friendlier behavior: hand
		// back the zero Template and let Validate produce its normal
		// "name required" / "at least one stage required" pair instead of
		// this turning into an opaque parse failure. The editor's
		// POST /templates/validate is reachable with {"source":""}
		// (e.g. a brand-new, untouched template), and that path is
		// expected to report ordinary required-field errors.
		return &t, nil
	}
	verr := &contracts.ValidationError{}
	if terr, ok := err.(*yaml.TypeError); ok {
		// yaml.v3 collects every problem in the document into one
		// TypeError before returning (spec §10.3's report-everything rule,
		// for free) — one entry per unknown/duplicate field, each already
		// carrying its own line number.
		for _, msg := range terr.Errors {
			verr.Add("source", unknownFieldMessage(msg))
		}
		return nil, verr
	}
	verr.Add("source", "not valid YAML: "+err.Error())
	return nil, verr
}

// unknownFieldErrorPattern matches yaml.v3's KnownFields rejection message,
// e.g. `line 7: field retires not found in type template.Stage`. The field
// name and reported Go type name are exactly what Parse needs to build a
// did-you-mean suggestion; see suggestionFor.
var unknownFieldErrorPattern = regexp.MustCompile(`^(line \d+): field (.+?) not found in type (\S+)$`)

// unknownFieldMessage turns one raw yaml.v3 TypeError line into the
// user-facing message reported on the "source" field. It preserves the
// line number yaml.v3 already computed (an unknown key has no widget in
// the visual editor to point at anyway, so the line number — directly
// actionable in YAML mode — is the right thing to surface, not a
// synthesized field path like stages[2].retires).
//
// Not every TypeError line matches unknownFieldErrorPattern — yaml.v3 also
// uses this same error type for a duplicate key ("... already set in type
// ..."), which it checks regardless of KnownFields. Those are surfaced
// verbatim rather than mangled by a pattern they don't match.
func unknownFieldMessage(raw string) string {
	m := unknownFieldErrorPattern.FindStringSubmatch(raw)
	if m == nil {
		return raw
	}
	line, field, typ := m[1], m[2], m[3]
	if suggestion := suggestionFor(typ, field); suggestion != "" {
		return fmt.Sprintf("%s: unknown field %q — %s", line, field, suggestion)
	}
	return fmt.Sprintf("%s: unknown field %q", line, field)
}

// suggestionFor produces the text that follows "— " in an unknown-field
// message, or "" if nothing useful can be said. It checks two sources, in
// order:
//
//  1. seededSpellings — the aspirational key spellings from
//     docs/REBUILD-SPEC.md §6's example YAML, which predates this package
//     and is the single most likely source of a real, silently-broken
//     template: anyone who copy-pasted that example has been losing these
//     fields ever since. These get a specific, deliberate message rather
//     than whatever edit distance happens to turn up.
//  2. closestField — plain edit distance against typ's real field names,
//     for ordinary typos (retires/retry, timout/timeout, ...) that aren't
//     one of the seeded spellings.
func suggestionFor(typ, field string) string {
	if seed, ok := seededSpellings[field]; ok {
		if seed.rename != "" {
			return fmt.Sprintf("did you mean %q?", seed.rename)
		}
		return seed.explanation
	}
	if best := closestField(knownFieldsByType[typ], field); best != "" {
		return fmt.Sprintf("did you mean %q?", best)
	}
	return ""
}

// seededSpelling is one entry of seededSpellings: either a direct 1:1
// rename (the concept survived, just under a different key) or, when the
// aspirational feature has no implemented equivalent at all, a short
// explanation of what to do instead.
type seededSpelling struct {
	rename      string
	explanation string
}

// seededSpellings maps every key spelling used in docs/REBUILD-SPEC.md §6's
// "Stage Definition (YAML)" example — explicitly marked aspirational, not
// the implemented grammar — to what actually happened to it. See
// docs/REBUILD-SPEC.md §6 "Implemented Surface (v1 conformance)" for the
// authoritative rename/drop table this mirrors.
var seededSpellings = map[string]seededSpelling{
	"pipeline":        {explanation: "the top-level wrapper isn't used; stages live at the document root"},
	"id":              {rename: "name"},
	"type":            {explanation: "a stage is an agent OR a command; set one of those instead"},
	"run":             {rename: "command"},
	"on_failure":      {rename: "on_fail"},
	"goto":            {rename: "loop_back"}, // inside on_fail:
	"reads":           {explanation: "not supported; stages exchange context via task handoffs"},
	"produces":        {explanation: "not supported; stages exchange context via task handoffs"},
	"different_agent": {explanation: "not supported; retry uses the same agent"},
}

// knownFieldsByType maps the Go type name yaml.v3 embeds in its "not found
// in type" message (e.g. "template.Stage", from out.Type().String() in
// yaml.v3's decoder) to that type's actual yaml key names, so an unknown
// field that isn't one of the seededSpellings can still get an
// edit-distance guess scoped to the fields that are actually legal at that
// nesting level.
var knownFieldsByType = map[string][]string{
	reflect.TypeOf(Template{}).String(): yamlFieldNames(reflect.TypeOf(Template{})),
	reflect.TypeOf(Stage{}).String():    yamlFieldNames(reflect.TypeOf(Stage{})),
	reflect.TypeOf(Defaults{}).String(): yamlFieldNames(reflect.TypeOf(Defaults{})),
	reflect.TypeOf(Retry{}).String():    yamlFieldNames(reflect.TypeOf(Retry{})),
	reflect.TypeOf(OnFail{}).String():   yamlFieldNames(reflect.TypeOf(OnFail{})),
}

// yamlFieldNames extracts the yaml key each exported field of t serializes
// as, in the same form yaml.v3 itself would use, by reading the same
// `yaml:"..."` struct tags the decoder does.
func yamlFieldNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		if name, _, _ := strings.Cut(tag, ","); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// closestField returns whichever candidate is the most plausible typo
// target for field, or "" if none is close enough to be worth guessing.
// The distance cap is relative to the candidate's own length so a short
// field name doesn't attract a suggestion from a field that merely shares
// a couple of letters (e.g. "model" should not suggest "name").
func closestField(candidates []string, field string) string {
	best := ""
	bestDist := 0
	for _, c := range candidates {
		d := levenshtein(field, c)
		maxDist := len(c)/2 + 1
		if d > maxDist {
			continue
		}
		if best == "" || d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// levenshtein computes the classic single-character insert/delete/replace
// edit distance between a and b.
func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
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
	if t.Defaults != nil {
		checkDuration(&v, "defaults.timeout", t.Defaults.Timeout)
		// Defaults isn't "on" any one stage, so the merge-stage retry
		// rejection below doesn't apply here — whether defaults.retry ends
		// up governing the merge stage is a resolution-time question for
		// the engine, not a grammar question for this package.
		checkRetry(&v, "defaults.retry", t.Defaults.Retry, false)
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
		case s.Command != "" && s.Model != "":
			v.Add(field+".model", "model is only valid on agent stages")
		}

		if _, err := ParseCond(s.When); err != nil {
			v.Add(field+".when", err.Error())
		}

		checkDuration(&v, field+".timeout", s.Timeout)
		checkRetry(&v, field+".retry", s.Retry, isMergeStage(s))
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

// isMergeStage recognizes the land-attempt stage by the same convention the
// engine uses to find it (services/pipeline/engine/engine.go's
// isMergeStage): a command beginning with "git merge". It's replicated here
// rather than imported from the engine because pkg/template is a shared
// leaf package the engine itself imports — importing back from the engine
// would be a dependency inversion.
func isMergeStage(st Stage) bool {
	return strings.HasPrefix(strings.TrimSpace(st.Command), "git merge")
}

// checkRetry validates a retry: block wherever one can appear — a stage's
// own or the template-level defaults.retry: — and, when onMergeStage is
// true, rejects it outright regardless of its contents.
//
// The merge stage already gets a bounded retry for free: the land queue
// retries its own rebase -> gate -> merge cycle a fixed number of times
// (that's the land queue's job, not the template's). A template-level
// retry wrapped around the merge stage on top of that would hold the FIFO
// land queue and the cross-process workspace lock for the duration of
// however many repeated rebase+gate cycles the outer retry adds — exactly
// the kind of thing the "Not Rocket Science Rule" serialization exists to
// prevent.
func checkRetry(v *contracts.ValidationError, field string, r *Retry, onMergeStage bool) {
	if r == nil {
		return
	}
	if onMergeStage {
		v.Add(field, "retry is not allowed on the merge stage: the land queue already retries its rebase+gate cycle a bounded number of times, and a template-level retry around it would hold the FIFO land queue and the cross-process workspace lock across repeated cycles")
		return
	}
	switch {
	case r.Max < 0:
		v.Add(field+".max", "must be >= 0")
	case r.Max > 10:
		// Capped low and deliberately: a typo'd `max: 1000` on an
		// expensive agent stage must not be able to burn a day of tokens
		// before anyone notices.
		v.Add(field+".max", "must be <= 10")
	}
	switch r.When {
	case "", "error", "always", "never":
	default:
		v.Add(field+".when", fmt.Sprintf("must be %q, %q, or %q, got %q", "error", "always", "never", r.When))
	}
	// backoff has no positivity requirement — a zero backoff (retry
	// immediately) is a legitimate choice — but it still has to have
	// actually parsed as a duration.
	if !r.Backoff.valid() {
		v.Add(field+".backoff", fmt.Sprintf("not a valid duration — use a Go duration string like %q", "15m"))
	}
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
