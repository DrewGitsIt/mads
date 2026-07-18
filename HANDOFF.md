# Executor handoff: Template editor: per-stage model selection for agent stages

## What I did

Implemented exactly what the architect's handoff specified, no deviations
in design:

**Schema** (`pkg/template/template.go`)
- `Stage` gains `Model string \`yaml:"model,omitempty"\`
- `Validate` rejects `model:` on command stages (`"model is only valid on
  agent stages"`), added to the existing per-stage `switch`.

**Engine** (`services/pipeline/engine/engine.go`)
- `runAgent`'s precedence chain now reads `cfg.Model` → `st.Model` →
  `agent.Preferences.Model`, in that order. One `if` inserted between the
  two existing checks. Nothing else changed — `openOutputLog` and
  `recordUsage` already receive the resolved `model` variable.

**Fake provider fixture gap** (`providers/fake/fake.go`)
- `Fake.OneShot` now reflects `req.Model` into `Usage.Model` before
  applying any scripted `UsageOverride`, so tests can observe which model
  the engine actually resolved and handed to the provider without needing
  to script it every time. Scripted overrides still win when set.

**UI** (`ui/src/router/types.ts`, `StageCard.tsx`, `VisualStageList.tsx`)
- `TemplateStage.model?: string`.
- `StageCard` gets a Model field, agent-mode only: freeform text input
  with a `<datalist>` of known model ids, trimmed-to-`undefined` on every
  keystroke (matches `agent`/`command` toggle semantics), placeholder
  shows `"<inherited> (inherited)"` when blank.
- `VisualStageList` wires `useAgents` + `useProviderModels` (called
  unconditionally, before the parse-error early return), computes the
  known model id set and each stage's inherited model (mirroring the
  engine's `agent: auto` → `executor` name resolution), passes both down.
- No changes to `TemplateEditor.tsx` or `templateSync.ts` — the new field
  round-trips through the generic YAML parse/stringify same as `on_fail`.

## Tests

- `pkg/template/template_test.go`: extended `goodSource` with a
  `model:` on the architect stage, asserts it round-trips and that an
  unset stage's `Model` stays empty; added a `model:` on a command stage
  case to `TestValidateReportsEverythingAtOnce`.
- `services/pipeline/engine/model_test.go` (new): `TestModelPrecedence`
  is a 4-case table (CLI > stage > agent pref > provider default) that
  reads `usage.recorded`'s `model` field, which the fake now reflects
  from `OneShotRequest.Model`. `TestModelOverridesOnlyTargetedStage`
  covers a 2-stage template where only one stage carries a `model:`
  override and asserts the other stage's resolution is untouched.
- `ui/src/library/templates/StageCard.test.tsx`: 4 existing tests updated
  for the new required `knownModelIds` prop; 4 new tests — field visible
  only in agent mode, trim-and-undefined-on-blank, inherited placeholder,
  datalist options.
- `ui/src/library/templates/VisualStageList.test.tsx`: added
  `EventSource`/`fetch` stubs (via the repo's `FakeEventSource` +
  `vi.stubGlobal` pattern already used in `AgentsShelf.test.tsx`) to the
  3 existing tests, since `useAgents`/`useProviderModels` now fire real
  requests on render; added a new test asserting `knownModelIds` and
  `inheritedModel` reach the rendered `StageCard`.

## What I did not do (and why it's not needed)

- **`TemplateEditor.test.tsx`**: not named in the architect's handoff,
  but it renders `VisualStageList` and broke for the same reason as
  `VisualStageList.test.tsx` (real `fetch`/`EventSource` calls from the
  newly-added hooks racing against React's test cleanup — `"Should not
  already be working"` from `act()`). Fixed by adding the same
  `beforeEach`/`afterEach` stub pattern. This is a mechanical consequence
  of the spec'd change, not scope creep.
- No `cmd/mads/run.go`, model allow-listing, DB schema, or docs changes —
  all explicitly out of scope per the handoff, and none were needed.
- No Data Service API-layer round-trip test: templates persist as raw
  YAML strings (`Template.source`), so the save/load path is a string
  pass-through with no per-field handling to test independently of
  `pkg/template.Check`, which is already covered.

## For the reviewer to scrutinize

- The validation error message and field path
  (`stages[N].model`, `"model is only valid on agent stages"`) match the
  handoff's exact wording — worth confirming that's the wording actually
  wanted for user-facing display, since I didn't second-guess it.
- `TestModelOverridesOnlyTargetedStage`'s expected executor-stage model is
  `"sonnet"`, not empty/`"fake-fast"` — this surprised me initially. It's
  because migration `0008_agent_model_preference` pins all system agents'
  `Preferences.Model` to `"sonnet"` by default, so an unscripted stage
  with no override still resolves through the agent-preference tier, not
  straight to the provider default. Worth a second look to confirm this
  is the fixture behavior you'd expect a reviewer to hit.
- I did not add a allow-list or fuzzy-match warning for unknown model ids
  anywhere (editor or engine) per the "provider is the authority" stance
  in the spec — flagging in case the economics motivation (steering
  people toward Haiku) was expected to come with any nudge/hint beyond
  the datalist itself.

## Full test runs

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./...` — full repo, all packages pass.
- UI: `npx tsc --noEmit -p tsconfig.json` — clean.
- UI: `npx vitest run` — 195 files / 983 tests pass.
- UI: `npx eslint` on all touched files — clean.
