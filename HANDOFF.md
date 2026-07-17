# HANDOFF — Model pills: show full model identity with version

Architect stage output. Executor: read this fully before writing code.

## 0. Root cause — correct Drew's framing before you start

Drew's report frames this as "the UI is flattening [real model IDs] into
ambiguous friendly names." That's not what's happening. I checked every
layer and there is **no client-side name-flattening logic anywhere** — grep
for `friendlyName`/`displayName`/family-name string mapping in `ui/src`
turns up nothing.

The ambiguity is baked into the wire data at its source:
`providers/claudecode/claudecode.go`'s `DefaultModels` (lines 46-50) is a
hardcoded, non-versioned pinned list:

```go
var DefaultModels = []provider.Model{
	{ID: "sonnet", Name: "Claude Sonnet", Default: true},
	{ID: "opus", Name: "Claude Opus"},
	{ID: "haiku", Name: "Claude Haiku"},
}
```

`claude-code` has no real discovery call (`BehaviorModelDiscovery:
Emulated` in its `Matrix()`) — this pinned list *is* what `GET
/providers/claude-code/models` returns, verbatim
(`services/router/internal/api/reads.go`'s `handleListProviderModels`,
lines 74-99, is a pure passthrough: `contracts.Model{ID: m.ID, Name: m.Name,
Default: m.Default}`, no enrichment). So this is case (a) from the
project's usual UI-vs-backend gap triage: cheap to close at the real
source, not a client-side workaround. Fix the pinned list, not the UI.

Compare: `providers/fake/fake.go`'s `DefaultModels` (`fake-fast`/`Fake
Fast`, `fake-smart`/`Fake Smart`) has the same bare-name shape but isn't
ambiguous — there's no version to disambiguate for a fake model. Leave it
alone. `providers/kirocli/kirocli.go`'s `Models()` shells out to `kiro-cli
chat --list-models --format json` and passes through whatever that CLI
reports — also out of scope (Drew's report is claude-code-specific, and
kiro-cli's names are that provider's own problem to fix if ambiguous).

## 1. Server-side fix (do this first — it's what actually closes the gap)

`providers/claudecode/claudecode.go`, replace `DefaultModels` with
version-qualified IDs and names. Use the current real Claude model line-up
(Sonnet 5, Opus 4.8, Haiku 4.5 as of this writing — cross-check against
whatever's current when you implement, this drifts):

```go
var DefaultModels = []provider.Model{
	{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Default: true},
	{ID: "claude-opus-4-8", Name: "Claude Opus 4.8"},
	{ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5"},
}
```

These IDs aren't invented for this task — `claude-sonnet-5` already appears
in `ui/src/router/fixtures.ts` (session fixtures) and
`pkg/contracts/metrics_test.go`; `claude-haiku-4-5-20251001` already
appears in `providers/claudecode/claudecode_test.go` (a scripted fake
`claude` binary's one-shot `Usage.Model`). Use those exact strings so the
whole repo is consistent about what claude-code's real model identifiers
look like.

No Go test currently pins `DefaultModels`' exact values (grepped
`claudecode_test.go` — none), and `providers/conformance/conformance.go`'s
model-discovery check only asserts the list is non-empty, not its
contents, so this change is safe to make without touching other Go tests.
`pkg/contracts/model.go`'s `Model`/`ModelsResponse` wire shape needs no
change — `{id, name, default}` is already sufficient, it was just being fed
ambiguous data.

## 2. Client-side: one shared helper, no duplicated parsing

Add `ui/src/router/modelDisplay.ts`:

```ts
import type { Model } from "./types";

// Unambiguous model identity for display: the discovered display name
// (already version-qualified server-side, e.g. "Claude Sonnet 5"), or the
// raw model ID if it isn't in the discovered list (retired/unknown model,
// or a provider whose Models() call failed). Never returns a bare family
// name — every caller that shows a model MUST go through this.
export function modelDisplayName(modelId: string | undefined, models: Model[]): string | undefined {
  if (!modelId) return undefined;
  return models.find((m) => m.id === modelId)?.name ?? modelId;
}
```

Write `modelDisplay.test.ts` covering: known ID resolves to `name`, unknown
ID falls back to the raw ID unchanged, `undefined` input returns
`undefined`, empty `models` array falls back to raw ID.

This is the **only** place display-name resolution logic lives. Every
component below calls it — do not re-implement matching/fallback logic
per-component.

## 3. Launchpad `ModelPill` (`ui/src/features/sessions/launchpad/pills/ModelPill.tsx`)

Bug: the pill's trigger label is built from the raw `model` prop (a bare
ID string) directly — `` `${model} · ${provider}` `` — never looked up
against `modelsByProvider` at all. The dropdown options are already
correct (`{m.name}`, sourced straight from the fetched `Model[]`) — once
§1 lands those become version-qualified for free. Only the closed-pill
label needs fixing:

```tsx
const models = modelsByProvider[provider ?? ""] ?? [];
const label = model ? `${modelDisplayName(model, models) ?? model} · ${provider}` : "Select model";
```

`ModelPill.test.tsx` already fixtures a non-ambiguous ID (`claude-opus-4.6`
as both `id` and `name`) — add a second case where `model`/`provider` props
are set to something *not* in the fetched list and assert the pill falls
back to rendering the raw ID (proves the fallback path, not just the
happy path).

## 4. `ConversationHeader` (`ui/src/conversation/Header/ConversationHeader.tsx`)

Bug: the model pill renders `session.model` (a bare ID, e.g.
`"claude-sonnet-5"`) directly as the `PillPopover`'s `label`, with no
lookup at all. This component doesn't currently call `useProviderModels()`
— it needs to.

Careful point: `PillPopover` is a *freeform editable* pill (its own
comment: "the provider-grouped model picker is W6.2 Launchpad's job, not
rebuilt here") — typing a new value and hitting Enter calls
`onCommit(value)` → `onUpdateSession({ model: value })`, i.e. the edited
text becomes the literal new `session.model` wire value. Today
`PillPopover`'s edit `value` state is seeded from the same `label` string
it displays closed (`useState(label)`), so if you swap `label` for the
resolved display name, opening the pill to edit would seed the input with
"Claude Sonnet 5" instead of the real ID `"claude-sonnet-5"` — committing
without changes would silently corrupt `session.model` into the display
string. **Split display from edit value.** Give `PillPopoverProps` a
`value: string` (the raw value, used to seed editing and to commit) and an
optional `label?: string` (what the closed `Chip` shows; defaults to
`value`). Wire it:

```tsx
{session.model && (
  <PillPopover
    value={session.model}
    label={modelDisplayName(session.model, modelsByProvider[session.provider ?? ""] ?? [])}
    ...
  />
)}
```

The `agent` pill usage of `PillPopover` is unaffected (agent names aren't
ambiguous the same way — no `label` override needed there, it'll default
to `value`).

Add `useProviderModels()` to this component (same hook `ModelPill.tsx` and
`ModelField.tsx` already use — no new fetch logic). Update
`ConversationHeader.test.tsx`: assert the chip shows a resolved display
name when the fetched models list contains a match, and the raw ID
unchanged when it doesn't (e.g. an unknown/retired model, or the
`e2esmoke-fixture` string `ui/e2e/lib/fastModelFixture.ts` already uses —
that fixture's session model won't match any real provider's discovered
list, which is exactly the fallback path; don't change that fixture, it's
already a good test of the fallback).

## 5. Settings/agent surfaces — `ModelField.tsx`, no code change expected

`ui/src/library/agents/ModelField.tsx` (used by `AgentEditor.tsx`) already
renders `{m.name}` per `<option>`, sourced from the same `useProviderModels`
fetch — once §1 lands server-side, this becomes version-qualified for
free. Confirm with a quick test/manual check but don't expect a diff here.

Two other "model" text fields are explicitly **out of scope** — flag, don't
touch:
- `ui/src/library/workspaces/WorkspaceEditor.tsx` (`~line 100`): a raw
  `<input>` for the workspace's default model, not a validated dropdown or
  a rendered pill — there's no discovered identity being displayed, just a
  free-text default the user typed. Nothing to disambiguate.
- `ui/src/features/settings/panels/PipelinePanel.tsx`'s "Fast model"
  field (`~line 125`): also a free-text input, not sourced from
  `useProviderModels`. Same reasoning.

If you think either should actually become a validated `ModelField`-style
dropdown, that's a real scope expansion — flag it for a human decision
rather than doing it silently, per repo convention.

## 6. FakeRouterClient / fixtures

`ui/src/router/fixtures.ts`'s `FakeRouterClient.listModels()` currently
throws `"not fixtured: listModels"` unconditionally — every test that
needs models overrides it per-test (see `ModelPill.test.tsx`,
`useProviderModels.test.ts`, which both stub `fetch` directly rather than
using `FakeRouterClient` at all — those two files don't need touching
beyond what §3 adds). Check whether any `ConversationHeader.test.tsx` /
`ConversationTab.test.tsx` cases construct `FakeRouterClient` and rely on
`listModels` — if so, give it a realistic default fixture returning the
`claude-sonnet-5`/`claude-opus-4-8`/`claude-haiku-4-5-20251001` triad
(matching §1's new `DefaultModels`) instead of throwing, so header tests
don't all need a bespoke override. Don't change the throw-by-default
behavior for other methods.

## 7. Test plan / acceptance

- `providers/claudecode/claudecode_test.go`: no existing assertions on
  `DefaultModels` values to update, but consider adding one that asserts
  `Models()` returns version-qualified names (guards against this
  regressing back to bare family names later).
- `modelDisplay.test.ts` (new, §2).
- `ModelPill.test.tsx`: add the unknown-model fallback case (§3).
- `ConversationHeader.test.tsx`: add resolved-name and fallback cases (§4).
- `make check` (Go tests+vet+race, UI lint, UI vitest) must pass.
- `make ui-smoke` must pass — it exercises the Launchpad and Conversation
  header live against the real Router/claude-code fake binary stack, so
  it's the real end-to-end proof this works, not just unit tests.

## What NOT to do

- Don't add per-component string parsing of model IDs (e.g. regexing a
  version number out of `"claude-sonnet-5"`) — the display name comes from
  the wire `name` field via the one shared helper, full stop. If a future
  provider genuinely only ever has IDs and no name, that's the trigger to
  revisit `modelDisplayName` itself (still one place), not to special-case
  a component.
- Don't touch `kirocli.go` or `fake.go`'s model lists — out of scope, not
  ambiguous in the same way.
- Don't build a new validated-dropdown for `WorkspaceEditor`/`PipelinePanel`
  free-text fields (§5) — flag instead.

Remove this file before merge (pipeline convention).

## Executor notes

Implemented exactly as specified, sections 1–7, no deviations:

- §1: `providers/claudecode/claudecode.go` `DefaultModels` now
  `claude-sonnet-5`/`claude-opus-4-8`/`claude-haiku-4-5-20251001` with
  version-qualified `Name`s. Added `TestDefaultModelsAreVersionQualified` to
  `claudecode_test.go` (regex-asserts every `Name` contains a digit) as a
  regression guard, per §7's suggestion.
- §2: added `ui/src/router/modelDisplay.ts` (`modelDisplayName`) verbatim as
  specced, plus `modelDisplay.test.ts` covering all four cases named in the
  handoff.
- §3: `ModelPill.tsx`'s closed-pill label now resolves through
  `modelDisplayName`. Added two `ModelPill.test.tsx` cases (resolved name
  and unknown-ID fallback) — the handoff asked for one fallback case; added
  a matching positive case too since the bug was specifically in the closed
  label and the acceptance criteria calls for both.
- §4: `ConversationHeader.tsx` now calls `useProviderModels()` and passes
  `modelDisplayName(...)` as the model pill's `label`. Split `PillPopoverProps`
  into `value` (raw, seeds editing/commit) + optional `label` (closed-chip
  display only) exactly as specced — verified by tracing `commitPill` still
  receives the raw edited text unchanged. Added resolved-name and fallback
  cases to `ConversationHeader.test.tsx`.
- §5: confirmed `ModelField.tsx` needs no change (renders `{m.name}` off
  `useProviderModels` already) and that `WorkspaceEditor.tsx` /
  `PipelinePanel.tsx`'s model fields are free-text, not dropdowns — left
  both untouched, not flagging further since the handoff already flagged
  them for a human decision.
- §6: checked — `useProviderModels()` calls the `router` singleton from
  `router/client.ts` directly, not any client injected via props/context, so
  `FakeRouterClient.listModels()` is never invoked by
  `ConversationHeader.test.tsx` / `ConversationTab.test.tsx` (they inject a
  `FakeRouterClient` only for `routerClient`-prop-threaded calls). No
  fixture change was needed or made; verified by running both suites.
- §7: `make check` (Go tests+vet+lint+race, UI eslint, 719 UI vitest tests)
  and `make ui-smoke` (8 passed / 3 pre-existing skipped) both green.

Nothing flagged beyond what the handoff already flagged. No scope was
added or cut.
