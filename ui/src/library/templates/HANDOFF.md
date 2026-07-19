# Template editor: mads-styled fields and honest picker affordances

## What changed

- **New shared primitives**: `ui/src/components/Select/Select.tsx` (closed-set
  themed picker — button trigger + listbox, replaces native `<select>`) and
  `ui/src/components/Combobox/Combobox.tsx` (free-text input with a themed
  suggestion list — replaces the bare `<input list>`/datalist). Both follow
  the ARIA combobox/listbox pattern, support ArrowUp/ArrowDown/Enter/Escape,
  and are exported from `ui/src/components/index.ts` for reuse elsewhere.
- **StageCard.tsx / StageCard.module.css** (not in the task's literal file
  list, but this is where Model, Loop back to, and the Agent/Command toggle
  actually live — VisualStageList just maps over StageCards): Agent/Command
  radios replaced with the existing `Chip` toggle-group idiom (same pattern
  as `AppearancePanel`'s Palette/Theme pickers); Model field is now a
  `Combobox` seeded with `knownModelIds`, still free text, still shows the
  inherited model as placeholder when blank; Loop back to and Then are now
  `Select`s over the closed option sets; the On-fail `<details>` disclosure
  got a chevron marker and consistent spacing; all field labels use the
  `.field`/`.fieldLabel` convention borrowed from `TaskDetailSheet`.
- **TemplateEditor.tsx/.module.css, VisualStageList.tsx/.module.css**: same
  `.field`/`.fieldLabel` treatment applied to Description and Template name;
  no structural changes otherwise (mode toggle, validate row, error list,
  Save/Delete/Cancel footer already used themed `Chip`/`Button`/`SideSheet*`).
- Tests: new `Select.test.tsx` (7 tests) and `Combobox.test.tsx` (8 tests);
  updated `StageCard.test.tsx` and `VisualStageList.test.tsx` wherever they
  queried the old native `<select>`/`<datalist>` DOM directly. All existing
  `TemplateEditor.test.tsx` round-trip tests pass unchanged.

## What I did not do

- Did not touch `YamlSourceView.tsx` (Monaco) — out of scope, not a native
  form control in the sense the task means.
- Did not add a search/filter affordance to the `Select` for Loop back to —
  the option set is small (stage names only) and the task only asked for a
  themed picker, not a searchable one; `Combobox` already covers the
  free-text+suggestions case for Model.
- Did not attempt to genericize `Select`/`Combobox` beyond what this task
  needed (e.g. no multi-select, no async options) — smallest change that
  satisfies the criteria.

## What to scrutinize

- **Scope expansion**: `StageCard.tsx`/`StageCard.module.css` were edited
  even though the task named only `TemplateEditor`/`VisualStageList`. This
  was necessary — Model and Loop back to are StageCard's fields — but
  flagging it explicitly since it's outside the literal file list.
- **New shared components**: `Select`/`Combobox` are now general-purpose
  exports in `ui/src/components/`. Check the API shape (`SelectOption[]`,
  plain `onChange(value)`) is one you're happy having other features build
  on, since this is their first consumer.
- **Verification**: automated (vitest 197 files/1017 tests, `tsc --noEmit`,
  `eslint`) all green. Additionally spun up the real stack (`vite build` +
  Go router/data/session binaries via a throwaway Playwright script, not
  committed) and visually confirmed both dark and light themes render
  correctly and the Model/Loop-back-to pickers open, filter, and keyboard-
  navigate as expected — screenshots were inspected but not saved to the
  repo.
