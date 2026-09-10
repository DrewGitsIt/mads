# Pipeline template configuration — retry, timeouts, and a strict grammar

Status: **implemented** (uncommitted working tree, 2026-09-09). All eight tasks
in `docs/pipeline-template-config-tasks.yaml` landed; `REBUILD-SPEC.md` §6's
conformance table has been updated to match. Retained as the rationale record —
the reasoning below explains *why* the shipped design is shaped as it is.
Scope: close the configurable-stage-behavior gaps in `pkg/template` (spec §6),
without changing what `loop_back` means or how the land queue works.

## Owner decisions already made

1. **Strict field checking is in** — unknown YAML keys become validation
   errors instead of being silently dropped. This obliges us to give the user
   a way to *discover* an already-saved template that goes invalid under the
   new rule; see [Strictness and its discovery path](#strictness-and-its-discovery-path).
   That discovery work is part of this change, not a follow-up.
2. **A retry never resets the worktree.** This is a pipeline rule, stated
   once here and applied everywhere: the failing stage may be far down the
   pipeline, and resetting would discard everything the earlier stages
   legitimately produced. A retried stage re-enters the same worktree with
   the previous attempt's partial work in place. This is already how
   `loop_back` behaves today, so retry introduces no new semantics — it just
   makes the existing rule explicit.

## Current state

`pkg/template/template.go` is the whole configurable surface: `name` plus
stages of `{name, agent|command, when, model, on_fail{loop_back, max_loops,
then}}`. `Validate()` is shared by the editor (`POST /templates/validate`),
the Data Service create/update handlers, and `pipelinerun.LoadTemplate` at
launch — one grammar, one judge. Everything below preserves that property:
every new rule lands in `pkg/template` and is therefore enforced identically
in all three places.

## Reconciliation: how §6 and the code diverged

The code is **not** drifting from its own plan. §6 was deliberately narrowed
in good faith — but the narrowing was recorded in `MADS-UX.md` and
`MADS-WORK-ITEMS.md`, never in `REBUILD-SPEC.md` §6 itself, which still reads
as the contract. Git bears this out: `REBUILD-SPEC.md` has three commits
total, and the two after Wave 0 are a port-number change and a §12 bullet.
§6 has not been touched since the repo was scaffolded.

The narrowing lives in **`MADS-UX.md` §3.5 (line 139)**:

> each stage card carries the **full §6 grammar** — type agent|command, agent
> binding, `when:` condition, `on_fail` policy with loop-back target,
> max-loop cap, `then: block`

That sentence *is* the adaptation, and it is faithful to what shipped. The
problem is the phrase "the full §6 grammar", which asserts an equivalence
that is false — and is why the divergence has been invisible.
`StageCard.tsx`'s doc comment inherits the same claim ("covering
pkg/template's actual grammar (§6)"). `MADS-WORK-ITEMS.md` W1.5/W3.1/W3.2
match the UX narrowing exactly, and the code matches the work items.

So there are four layers, and only the top one is stale:

| Layer | State |
|---|---|
| `REBUILD-SPEC.md` §6 | Aspirational. Untouched since Wave 0. Never marked superseded. |
| `MADS-UX.md` §3.5 | The real v1 grammar — but mislabelled "the full §6 grammar". |
| `MADS-WORK-ITEMS.md` W1.5/W3.1/W3.2 | Matches the UX narrowing. |
| Code | Matches the work items. |

### Classified against §6

**A. Deliberately scoped out** — in §6, in no work item, never built:
`retry`, per-stage `timeout`, the `defaults:` block, the `gate` stage type,
`reads`/`produces`, `{{}}` interpolation, and the whole typed data-contract
namespace (`stages.<id>.<key>`, `file_changes`, `input`, `pipeline`). Also
four pure renames: `id:`→`name:`, `run:`→`command:`, `on_failure:`→`on_fail:`,
and `type:` dropped in favour of inferring agent-vs-command from which field
is set.

**B. Planned but not delivered.** W1.5 includes "saves version-stamped so
in-flight runs keep their frozen copy (§6)". There is no `version` column in
`0006_templates.up.sql` and no `Version` field on `contracts.Template`. The
*behaviour* was achieved another way — the engine holds a parsed in-memory
copy, so saves genuinely don't disturb in-flight runs — so this is not a
correctness gap. What's missing is the audit trail: nothing records which
template revision a historical run executed, which W5.7's pipeline
run-history drill-in will want.

**C. Delivered beyond the plan, never documented back.** Per-stage `model:`
(commit 65e9a2e) is in the code, the validator, and the editor, but is absent
from `MADS-UX.md` §3.5's grammar list. The documentation debt runs in both
directions.

**D. Not a scope question at all.** Gap 7 below (attempt-counter
misattribution) is a plain bug.

**E. A forward dependency already assumed elsewhere.** W5.7's analytics
promise an "agent leaderboard (usage, success rate, **retries**)". With no
stage retry and a miscounting attempt meter, that metric has nothing to count
— retry is load-bearing for a later wave, not just a §6 nicety.

Worth distinguishing so it isn't mistaken for coverage: `POST
/tasks/{id}/retry` **does** exist (`api.go:186`). That is the Board's manual
"retry this blocked task" action from UX §3.2 — a human-initiated re-run,
unrelated to automatic per-stage retry policy.

### Recommended reconciliation

Don't rewrite §6 to match the code — that would destroy its roadmap value,
and items A and E are things we still want. Instead:

1. **Append a conformance subsection to §6** stating the v1 grammar exactly,
   with each §6 feature marked shipped / deferred / renamed. §6 is what every
   `(§6)` reference in the repo points at, so it must not read as a contract
   it doesn't keep.
2. **Annotate §6's example YAML as aspirational**, or rewrite it in the
   implemented grammar. As written it uses `id`, `type`, `run`, `on_failure`,
   `reads`, `produces`, and a top-level `pipeline:` wrapper — seven spellings,
   none of them real. It is the single most likely source of an invalid
   template once strictness lands.
3. **Fix `MADS-UX.md` §3.5**: "the full §6 grammar" → "the implemented §6
   grammar", and add `model:` to the list (item C).
4. **Add the version stamp** (item B). *Owner decision: keep it in, and
   surface the version in the template UX.* `contracts.Template` gains
   `Version int`, bumped in the templates API `handleUpdate` (not in the
   generic `Registry[T].Update` — that is shared with agents, capabilities
   and workspaces). The run stamp rides on `pipeline.started`, which already
   carries `template`; add `template_version` beside it, joined to a run by
   `e.runSeq` (the event's own Seq, already the run's stable identity).
   Surfaced in the shelf card, the editor header, and the run header.

Note this changes the priority of the did-you-mean table designed below: with
§6's example reconciled, that table shifts from *load-bearing* to *belt and
braces*, since the main source of spec-spelled templates goes away.

## Gaps

| # | Gap | Evidence |
|---|---|---|
| 1 | **No stage retry at all.** Spec §6 specifies `retry: {max, when}` with template defaults, per-stage override, and "different agent on retry". None is implemented. | The only retry behaviors are hardcoded and non-configurable: rate-limit backoff (`engine.Config.RateLimitMaxRetries`, default 12) and land-attempt flake retry (`maxLandRetries = 2`, `triage.go:14`). |
| 2 | **Timeouts are process-global, and the two launch paths disagree.** | `cmd/mads/run.go:45` defaults `-stage-timeout` to 30m. `services/pipeline/main.go:170` — the Router-spawned child, i.e. *every UI-launched run* — never sets it, so it falls to the engine's 10m default (`engine.go:151`). No knob exists in Settings. Live bug, not just a missing feature. |
| 3 | **Unknown keys are silently swallowed.** | `Parse` uses `yaml.Unmarshal` with no `KnownFields`. `on_failure:` (the spec's own spelling), `timeout:`, `retry:`, or a misspelled `loop_back` all validate green and vanish. This is what makes gap 1 dangerous: users will believe they configured retry. |
| 4 | **No template-level `defaults:` block.** | Per-stage `model` exists; no template default. Note `engine.go:1199` has CLI `-model` *override* stage model, which is deliberate but inverts spec's defaults semantics — `defaults.model` sits between `st.Model` and `agent.Preferences.Model`, not above them. |
| 5 | **Structural roles ride on magic names.** | `isMergeStage` = command prefix `git merge`; `buildStageCommand` = stage named `build`; `gateStage` = stage named `verify`. Rename `verify` → `test` in the editor and the post-rebase gate silently disappears, taking the Not-Rocket-Science guarantee with it. No error. |
| 6 | **`on_fail.then` is a one-value enum** (`block`). | Most valuable missing value is `continue`: an advisory stage (lint, ux_review) that shouldn't block the task. |
| 7 | **Attempt numbering is wrong — and it blocks the retry work.** | `engine.go:1208`: `attempt := e.loops[task.ID+"|"+st.Name] + 1`. But `e.loops` is keyed on the stage that *failed* (`engine.go:655`), not the one looped back to. A `build` failure looping to `executor` increments `loops["t\|build"]`, so executor's attempt stays 1 forever: the second run `O_APPEND`s into `executor-attempt-1.log`, and `usage.recorded` / `stage.attempt_ref` are misattributed. The UI's attempt badge (`TaskCard.tsx:18`) and drill-in log picker both undercount. |

Gaps 5 and 6 are **out of scope** for this pass — both are real, neither is
mild, and gap 5 needs a migration story for the magic-name stages. Tracked as
follow-ups at the end.

## Design: grammar

```yaml
name: default-implementation
defaults:
  timeout: 15m
  retry: { max: 2, when: error }
stages:
  - name: executor
    agent: executor
    timeout: 45m
    retry: { max: 3, when: error, different_agent: true }
    on_fail: { loop_back: architect, max_loops: 2, then: block }
```

```go
type Template struct {
    Name     string    `yaml:"name"`
    Defaults *Defaults `yaml:"defaults,omitempty"`
    Stages   []Stage   `yaml:"stages"`
}

type Defaults struct {
    Timeout Duration `yaml:"timeout,omitempty"`
    Model   string   `yaml:"model,omitempty"`
    Retry   *Retry   `yaml:"retry,omitempty"`
}

type Retry struct {
    Max            int      `yaml:"max"`
    When           string   `yaml:"when,omitempty"`    // "error" (default) | "always" | "never"
    Backoff        Duration `yaml:"backoff,omitempty"`
    DifferentAgent bool     `yaml:"different_agent,omitempty"`
}

// Stage gains:
//   Timeout Duration `yaml:"timeout,omitempty"`
//   Retry   *Retry   `yaml:"retry,omitempty"`
```

`Duration` is a named `type Duration time.Duration` with `UnmarshalYAML`
using `time.ParseDuration`, so `15m` works and a bad value surfaces as a
`ValidationError` on `stages[i].timeout` rather than a YAML type error. It
also implements `MarshalYAML` returning the string form, so the editor's
YAML round-trip preserves `15m` instead of rewriting it to `900000000000`.

### Retry vs. loop_back — the central semantic

**Retry re-runs *this* stage for a transient failure. `loop_back` re-runs an
*earlier* stage for a substantive rejection.** They compose in one direction
only:

```
run stage → fail → retry (up to retry.max) → still failing → consult on_fail → loop_back or block
```

Retry nests strictly inside the existing failure path. Nothing about
`loop_back` accounting, `on_loop` guards, or `then: block` changes.

`retry.when` maps onto a distinction the engine already makes internally but
does not expose. In `runAgent` there are two failure shapes:

- an error from `Provider.OneShot` — crash, transport failure, or the stage
  timeout cancelling `sctx`;
- the `STAGE: fail` marker in the agent's own final line — a considered
  verdict.

Retrying a reviewer's considered "fail" is waste; it will say it again. So:

| `when` | retries `OneShot` error / timeout | retries `STAGE: fail` verdict / non-zero exit |
|---|---|---|
| `error` (default) | yes | no |
| `always` | yes | yes |
| `never` | no | no |

For command stages the same split applies: a non-zero exit is a verdict, a
deadline kill is an error. Implementing this needs the two cases returned
distinguishably — add a `stageVerdictError` sentinel, exactly parallel to the
existing `gateFailure` type, and wrap the verdict paths in `runAgent` and
`runCommand`.

## Design: engine

All changes are contained in `runTask` and the attempt loop; nothing in
`landAttempt`, `worktree.go`, or the scheduler moves.

**A real attempt counter (fixes gap 7, and retry needs it anyway).** Add
`e.attempts map[string]int` keyed `task|stage`, incremented once per stage
*entry* — including loop-back re-entries and retries — and use it for
`openOutputLog`, `recordUsage`, and `recordRef`. Keep `e.loops` exactly as
it is for `loop_back` accounting. **Two separate meters; do not merge them.**
They answer different questions and merging them would make `max_loops`
consume retry budget.

**Fold retry into the existing attempt loop.** `runStageRateLimited` already
owns a retry loop for rate limits; extend it to own the retry policy too,
rather than nesting a second loop around it. Critically: **a rate-limit pause
must not count against `retry.max`.** It has its own cap and its own
semantics (inform and wait, spec §11.9).

**Per-stage timeout** in `runStage`, replacing the single `e.cfg.StageTimeout`
read at `engine.go:971`:

```
st.Timeout → tpl.Defaults.Timeout → cfg.StageTimeout → engine default (10m)
```

The wall-clock watchdog (`stageTimeoutPoll`, for suspend survival) is
unchanged — it just reads the resolved value.

**New event** `stage.retried`, payload `{stage, attempt, max, reason}`. Add to
`pkg/contracts/event.go`, the type registry, and regenerate the registry
golden. The UI's attempt badge starts being correct for free once `e.attempts`
increments properly.

### Validation rules (all through the existing `ValidationError`)

- `retry.max` ≥ 0, capped at 10 — a typo'd `max: 1000` must not burn a day of
  tokens.
- `retry.when` ∈ {`error`, `always`, `never`}.
- `retry.different_agent` only on agent stages, and only when `agent: auto` —
  a hard-named agent has nowhere else to go. Hard error, not a warning.
- `timeout` parseable and > 0.
- **`retry` is rejected on the merge stage.** `landAttempt` holds the FIFO
  land queue *and* the cross-process workspace lock; a template retry wrapped
  around it would hold both across repeated full rebase+gate cycles. Message:
  "the merge stage's retry is the land queue's own (`maxLandRetries`)".

## Strictness and its discovery path

Turning on `KnownFields(true)` is the easy half. The half that matters is
that a template saved *before* this change can go invalid without the user
touching anything.

The population is bounded, which shapes the answer: `handleCreate` and
`handleUpdate` already validate, so **no new invalid template can be stored
once this lands**. The at-risk set is exactly the templates saved under the
old lax grammar, plus the shipped default (which we control and update in the
same change). This is a one-time upgrade problem, not an ongoing one.

Four surfaces, in order of how a user actually hits them:

**(a) Validate on editor open.** `TemplateEditor` currently only calls
`validate(source)` from the Validate button and from Save. Add a mount-time
`useEffect` calling the same hook. This turns "press a button to discover"
into "the errors are already on screen", and it is the single cheapest fix —
it's where a user who clicks a badged template lands.

**(b) Badge broken templates in the shelf.** `TemplatesShelf` shows name and
description only; a broken template is invisible until opened. Add a sweep
endpoint — `POST /templates/validate-all`, returning
`[{id, name, valid, errors}]` — that the shelf calls once on mount alongside
the list.

Deliberately *not* extending `GET /templates` with a `valid` field:
`handleList` returns a bare `[]contracts.Template`, so adding a field means
either changing `contracts.Template` (wrong — validity isn't a property of
the record) or wrapping the response in an envelope, which is a breaking
change for every client including the session-start template picker and
workspace binding. Those callers also shouldn't pay for per-stage agent
registry lookups (`knownAgent` → `agents.GetByName`) on a cheap read.

**(c) `mads template validate [name...]`** — no args validates all. The
pipeline is launchable from the CLI, so the discovery path cannot be
UI-only; this is also what makes the upgrade instruction writable ("run this
after updating") and gives CI a hook. Fits the existing `cmd/mads/main.go`
dispatch alongside `run`, `seed`, `tasks`.

**(d) A log-only sweep at Data Service startup**, after `store.Bootstrap`:
validate every stored template, log one warning line per invalid one. No
schema change, no UI dependency, and it surfaces in the Router log where an
upgrade problem is normally noticed. Must not block startup.

**Rejected: auto-repair.** Stripping unknown keys during a migration would be
strictly worse than the bug we're fixing — the key meant something to the
user, and silently dropping it is precisely the current failure mode. Tell
them; don't rewrite their file.

**Rejected: folding this into `pkg/preflight`.** Preflight is *environment*
health — git, node-pty, provider binaries — surfaced in the first-run wizard
and Settings. "Your template has a typo in stage 3" is a different category
and would be out of place there.

### Error shape

`yaml.v3`'s `KnownFields(true)` returns a `*yaml.TypeError` whose `Errors`
slice carries every unknown field at once — which fits `ValidationError`'s
all-errors-together contract well — but the messages are *line-numbered*
(`line 7: field retires not found in type template.Stage`), not field-pathed.

Recommendation: emit them as `source`-field errors with the line preserved,
plus a did-you-mean against the known field set:

```
source: line 7: unknown field "retires" — did you mean "retry"?
```

Don't build a yaml.Node-walking path mapper to get `stages[2].retires` for
v1. In visual mode an unknown key can't be rendered anyway — there is no
widget for it — so directing the user to YAML mode, where the line number is
directly actionable, is the correct behavior rather than a compromise.

**Seed the did-you-mean table with the spec's own spellings.** `REBUILD-SPEC.md`
§6's example YAML uses `id`, `type`, `run`, `on_failure`, `reads`, `produces`,
and a top-level `pipeline:` wrapper — *none* of which are the implemented
grammar. Anyone who copy-pasted from the spec has a silently-broken template
today and will get a wall of errors the moment strictness lands. Mapping those
seven spellings explicitly turns that wall into a fixable list.

Corollary doc task: reconcile §6's example with the grammar we actually
implement, or mark it aspirational. It is currently the most likely source of
invalid templates.

### Constraint: no custom `UnmarshalYAML` on the template structs

`KnownFields(true)` lives on the **Decoder**. When a type implements
`yaml.Unmarshaler`, the decoder hands the method a `*yaml.Node`, and the
conventional `node.Decode(&shadow)` inside builds a *fresh* decoder via
`newDecoder()` with `knownFields = false`. Every key inside that mapping then
escapes the check. Probed directly:

```
stages[0] unknown key -> err = <nil>                       # NOT caught
top-level unknown key -> err = field bogustop not found    # caught
```

Stage level is where nearly every unknown key will actually appear
(`on_failure:`, `type:`, `run:`, `id:`, `reads:`, `produces:`, and every
misspelling of `retry`/`timeout`/`loop_back`), so a custom unmarshaler on
`Stage` or `Defaults` silently guts the entire strictness feature while every
local test still passes.

**Rule: `Template`, `Stage`, `Defaults` and `Retry` must not implement
`UnmarshalYAML`.** Where "was this key present?" matters and the zero value is
ambiguous — `timeout: 0` parses fine, so it is indistinguishable from an
omitted `timeout:` — use a **pointer** (`*Duration`) and let nil carry
absence, rather than reconstructing presence by walking the node.

`Duration` itself may keep its custom `UnmarshalYAML`: it decodes a scalar,
has no struct fields of its own, and `*Duration` routes through it normally.

### Gotcha: empty source

`Parse` moves from `yaml.Unmarshal(source, &t)` to
`yaml.NewDecoder(bytes.NewReader(source))` + `KnownFields(true)` + `Decode`.
`Decode` returns `io.EOF` on empty input, where `Unmarshal` succeeded with a
zero `Template`. Treat `io.EOF` as an empty template so it continues to fail
with the friendly "name required" / "at least one stage required" pair rather
than an opaque parse error. `POST /templates/validate` with `{"source":""}`
is reachable from the editor.

## Config wiring (the real fix for gap 2)

- Add to `contracts.Config` (§3.7 Pipeline panel): `PipelineStageTimeout` and
  `PipelineRetryMax`, with a migration and defaults.
- **`services/pipeline/main.go` must pass `StageTimeout`** — today it passes
  nothing, which is the 10m-vs-30m split. Both launch paths read the same
  Config value.
- Full precedence: stage `timeout` → template `defaults.timeout` → CLI
  `-stage-timeout` / global Config → engine default.

## UI

- `ui/src/router/types.ts`: `TemplateStage` gains `timeout?: string` and
  `retry?: {...}`; `ParsedTemplate` gains `defaults?`.
- **`templateSync.parse` destroys unknown top-level keys.** It returns
  `{ name: doc.name ?? "", stages: doc.stages ?? [] }`, and the editor
  round-trips YAML → object → YAML on *every* visual edit
  (`applyParsedChange` → `serialize`). A `defaults:` block would be deleted
  the instant the user touched any field. Fix: `{...doc, name, stages}`.
  One line, mandatory, easy to miss. Stage-level keys already survive, so
  this bites `defaults:` specifically.
- `StageCard`: a "Retry" `<details>` block mirroring the existing "On fail"
  one, plus a timeout input. The field-error plumbing already works by prefix
  stripping — `errorFor(errors, "retry.max")` needs no new machinery.
- `TemplatesShelf`: broken-template badge fed by the sweep endpoint.
- `TemplateEditor`: validate on mount.

## Files to touch

| Area | Files |
|---|---|
| Grammar | `pkg/template/template.go` (types, strict Parse, new validation), new `pkg/template/duration.go` |
| Engine | `services/pipeline/engine/engine.go` (`e.attempts`, retry loop, per-stage timeout, `stageVerdictError`) |
| Events | `pkg/contracts/event.go` + registry golden |
| Config | `pkg/contracts/config.go`, new store migration, `services/pipeline/main.go`, `cmd/mads/run.go` |
| Validation surfaces | `services/data/internal/api/templates.go` (sweep endpoint), `services/data/internal/store/shipped.go` or Bootstrap caller (startup sweep), `services/router/internal/api/` (proxy route + manifest), new `cmd/mads/template.go` |
| UI | `ui/src/router/types.ts`, `client.ts`, `templateSync.ts`, `StageCard.tsx`, `TemplateEditor.tsx`, `TemplatesShelf.tsx` |
| Seed | `0006_templates.up.sql` default template — or a new migration if the shipped default gains `defaults:` |
| Docs | `REBUILD-SPEC.md` §6 example reconciliation |

## Sequencing

1. **Attempt counter fix alone** (gap 7). Self-contained, fixes a live
   misattribution bug, and everything else builds on it. Verifiable by
   asserting `executor-attempt-2.log` exists after a `build` → `executor`
   loop-back.
2. **Timeout: per-stage + defaults + config wiring** (gaps 2, 4). No new
   failure semantics; lowest risk of the three features.
3. **Retry** (gap 1), on top of step 1's counter.
4. **Strictness + the four discovery surfaces** (gap 3). Last, so the
   did-you-mean table can name the fields steps 2–3 just added — a user who
   wrote `retries:` or `timout:` gets a suggestion that actually resolves.

## Risks

- **Strictness is a breaking change at launch, not just at save.**
  `pipelinerun.LoadTemplate` calls the same `Check`, so an upgrade can break a
  run the user never touched. Mitigated by the four discovery surfaces and by
  making the error message name the offending key; accepted deliberately
  (pre-1.0, and the shipped default is the only template on most installs).
- **`retry.max × timeout` is now a large wall-clock product.** This is why
  `max` is capped at 10. The lease heartbeat renews on a timer rather than
  per-stage, so lease expiry is not at risk, but a stuck stage can now occupy
  a concurrency slot far longer than before.
- **Golden churn**: the contracts event registry golden regenerates. `make
  check` covers it.
- **Retry sees a partially-edited tree** — by rule (decision 2). The failure
  feedback already reaches the retried agent via the existing `feedback`
  string, so the prompt should say the tree may carry partial work from the
  previous attempt.

## Deferred

- Gap 5 (explicit stage `type:`, replacing magic-name detection for
  merge/build/verify). Needs a migration path for existing templates whose
  stages are load-bearing by name.
- Gap 6 (`on_fail.then: continue`). Changes what "done" means for a task and
  interacts with the merge/gate machinery.
