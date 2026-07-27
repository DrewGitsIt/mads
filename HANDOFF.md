# HANDOFF: tsk_6ee04e1f75c1d9ae — Redesign the agent editor to the app design system

## What was done

1. **`Select` (`ui/src/components/Select/Select.tsx`)** — added an optional
   `groupLabel?: string` to `SelectOption`. The listbox now renders a
   non-interactive group-header `<li>` before any option whose `groupLabel`
   differs from the previous option's, styled via a new `.groupLabel` class
   in `Select.module.css` (mirrors `pills.module.css`'s group-label look).
   Backward compatible — the only other consumer (`StageCard.tsx`) passes
   flat options with no `groupLabel` and is unaffected.

2. **`ModelField.tsx`** — replaced the native `<select>`/`<optgroup>` with
   the themed `Select`, flattening `useProviderModels()`'s per-provider
   arrays into `SelectOption[]` tagged with `groupLabel: provider`.
   `ModelField.test.tsx` updated to open the listbox and click the option
   instead of firing a native `change` event.

3. **`QuadrantField.tsx`** — replaced the native `<select>` + "?"-Popover
   with an always-visible 2×2 grid of `Card`s (one per quadrant), each
   holding a button with the quadrant's label and its description inline.
   This keeps the "quadrant help" capability (seeing the clarity/agency
   descriptions) while dropping the hidden-behind-a-tooltip mechanic in
   favor of an IA that shows the guidance up front. `QuadrantField.test.tsx`
   updated to click a card and to assert the description text is always
   present.

4. **New `ChipListField` (`ui/src/components/ChipListField/`)** — generic
   chip-input list editor, modeled directly on the existing
   `CredentialEnvVarsField` pattern (chips row + text input + Add button,
   Enter submits, dedupes). Used for **Tags**. Exported from the components
   barrel. Has its own test file.

5. **New `CapabilityPickerField` (`ui/src/components/CapabilityPickerField/`)**
   — registry-backed multi-select for **Skills** and **MCP servers**,
   modeled on `DependencyPicker`'s Popover + search + keyboard-navigable
   candidate list. Takes `capabilities: Capability[]` and a `type: "skill" |
   "mcp"` and filters the registry to that type; selection is constrained to
   `Capability.name` values, closing the free-text phantom-reference hole at
   the UI edge (mirrors what agentgen validation already closes server-side).
   When the registry has no capabilities of that type, it shows an empty
   state pointing at "Library → Capabilities" instead of an empty picker.
   Has its own test file covering the empty state, selection, removal, and
   type filtering.

6. **`NLGenerateBox.module.css`** — restyled only (no logic change) to read
   as the primary entry point: `Launchpad`'s hero-composer treatment
   (bg2 panel, border, radius, padding-5) instead of a bare label+textarea
   floating above the form.

7. **`AgentEditor.tsx`/`.module.css`** — restructured into sections
   (Identity: name/description/tags; Persona: prompt scaffold/prompt/
   enrichment actions; Behavior: quadrant/model; Capabilities: tools/skills/
   MCP servers; Import) using a small local `Section` heading component
   modeled on `SettingsSurface`'s section/heading pattern. CSV-string state
   for tags/mcp_servers/skills replaced with `string[]` state feeding the
   new field components directly; `csv()`/`parseCsv()` are kept only for
   **Tools**, which per the task's explicit requirement stays a plain
   (styled) list input rather than becoming chips or a registry picker.
   Name/Description/Prompt/Tools remain native `<input>`/`<textarea>`
   elements (implicit `<label>` wrapping, same DOM shape the existing test
   already queries), now fully styled with tokens
   (`--bg2`/`--line-soft`/`--r-sm`/`--text`) instead of unstyled browser
   defaults — matching `TaskDetailSheet`'s established styled-native-input
   convention for plain text entry. A one-line comment marks where a future
   Permissions section slots in; nothing was built for it.

8. **`ImportFromProviderDialog.tsx`** — swapped its native provider-fixture
   `<select>` for the themed `Select` (small, contained change: it's named
   in the task's file list as one of the editor's "friends", and the
   acceptance criterion is "no native/unstyled form controls remain in the
   agent editor"). No behavior change; its own tests pass unmodified.

`make check` (`go test -race ./...` + `npm run lint` + `npm run test`,
218 UI test files / 1268 tests) is green. `npx tsc --noEmit` also clean.

## What was deliberately not done

- **No Permissions section was built.** The task explicitly says a
  follow-up task adds it and to only "leave sensible room in the section
  layout." I left a comment marking the insertion point between Capabilities
  and Import; no component or state for `AgentPermissions` was added.
- **No dedicated shared `Input`/`TextField` component was introduced.**
  There isn't one in `ui/src/components` today, and every reference "quality
  bar" surface I checked (`TaskDetailSheet`, `CredentialEnvVarsField`) uses
  styled native `<input>`/`<textarea>` for plain text entry, only replacing
  *closed-set* controls (dropdowns) and *structured-list* free text (tags/
  skills/MCP servers) with themed components. I followed that same line:
  Name/Description/Prompt/Tools/the picker search boxes are native elements
  with token-based CSS, not raw browser defaults.
- **No browser/visual verification was performed.** The app requires the
  data/router/session backend services to render anything meaningful, and
  none were running in this session. Verification relied on the full
  Vitest suite (which exercises the real DOM via Testing Library — click,
  keyboard nav, ARIA roles) plus `tsc --noEmit` and `eslint`.
- **No changes to `EnrichmentActions.tsx` or `PromptScaffold.tsx` logic.**
  Their existing behavior (describe/suggest/refine, scaffold `<details>`)
  was left as-is; only the surrounding section layout changed. Their own
  input styling was already reasonably plain — I didn't restyle their
  `.instruction` input since the task's named-file list and the
  "horrific" complaint centered on AgentEditor's top-level fields and the
  select dropdowns, and this was a smallest-change judgment call.

## What a reviewer should scrutinize

- **QuadrantField's IA change (hidden Popover → always-visible card grid)**
  is more than a 1:1 visual reskin — it changes when the description text
  is visible (always, vs. on-demand behind "?"). I judged this an
  improvement consistent with "match the visual quality of the settings
  surface," and the task's requirement #4 allows updating tests for the new
  structure, but it's a bigger IA delta than the other three swaps and worth
  a second look.
- **Tools intentionally did NOT become a chip input or registry picker.**
  The task's requirement #3 draws an explicit distinction ("Tags become
  chip input... Tools stays a plain list input... but styled"), so Tools
  is still a single comma-separated text `<input>`, just styled — this
  looks inconsistent next to Tags/Skills/MCP servers unless you know that
  distinction was deliberate in the brief.
- **`ImportFromProviderDialog`'s `<select>` swap** — this dialog wasn't
  explicitly named as in-scope beyond "and friends" / preserving its
  behavior; I chose to swap it for acceptance-criteria literalism. If the
  reviewer considers this dialog out of scope, it's a one-hunk revert
  (`ImportFromProviderDialog.tsx`'s Select import + two lines).
  `ImportFromProviderDialog.module.css`'s `.select` class is still
  referenced (now just for margin-bottom on the Select wrapper) — dead
  code if reverted since native select styling isn't otherwise used.
- **`CapabilityPickerField`'s empty-state text** ("No {label} registered
  yet. Add one from Library → Capabilities...") is a judgment call on
  copy, not lifted from an existing string in the codebase — worth a
  wording pass if the product has a house style for empty states.
