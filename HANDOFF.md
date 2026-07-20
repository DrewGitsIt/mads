# Handoff: Task detail sheet click-to-edit

## What I did

The implementation from the previous attempt (commit d97039e) was already
complete and on this branch. I did not change any code. Instead I
investigated the reported failure:

```
command "make build && make test" failed: exit status 2
...
FAIL	github.com/drewkidwell/mads/services/pipeline/engine	20.139s
```

I could not reproduce this failure:
- `go test ./services/pipeline/engine/...` — pass
- `go test -race ./services/pipeline/engine/...` — pass
- `go test -race -count=3 ./services/pipeline/engine/...` — pass (3x, ~60s)
- `make build` — pass
- `make test` (full suite: `go test -race ./...` + `npm run lint` +
  `npm run test`) — pass, 1043/1043 UI tests, all Go packages ok

The failing package (`services/pipeline/engine`, land-queue/mergiraf merge
driver tests) has no relationship to the UI-only diff in d97039e
(`ui/src/tasks/DetailSheet/*`). This reads as a flaky/transient failure in
the previous pipeline run, not a regression caused by this change.

## What I verified against the acceptance criteria

Reviewed `TaskDetailSheet.tsx` and its CSS/tests directly:
- Resting state renders only `Markdown` (or an italic placeholder span)
  for description, acceptance_criteria, and scope — no textarea present
  until editing starts.
- Click, or Enter while the preview `div[role=button][tabIndex=0]` has
  focus, switches to an autofocused `<textarea>` seeded with the raw
  value (`EditableTextField`, TaskDetailSheet.tsx:62-157).
- Blur or Cmd/Ctrl+Enter calls `onCommit` → `set(field, value)` (existing
  draft plumbing, untouched). Escape reverts without calling `onCommit`.
- `.editableField` and `.editor` both set `min-height: 72px` so toggling
  doesn't jump; `.editIcon` opacity is 0 by default and 1 on
  `:hover`/`:focus-visible`.
- `TaskDetailSheet.test.tsx` has a dedicated `describe("click-to-edit
  long-text fields")` block covering: preview-only rest state, empty
  placeholder click, autofocus on click, Enter-to-edit, blur commit,
  Cmd/Ctrl+Enter commit, Escape cancel, and save-through-draft-plumbing.

## What I did not do

- No code changes — the diff already met the acceptance criteria and all
  gates pass.

## What the reviewer should scrutinize

- The downstream failure log's `services/pipeline/engine` output was not
  reproducible locally across 5 separate runs. If it recurs, it's likely
  either flakiness in that package's land-queue/mergiraf tests or an
  environment issue (e.g. `mergiraf` binary availability/state) unrelated
  to this task — worth a second opinion rather than re-attributing it to
  the UI change.
