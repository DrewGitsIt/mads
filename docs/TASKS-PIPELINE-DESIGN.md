# Pipeline runs inside the Tasks tab

Status: **shipped** (2026-10-19). Mockup: `docs/mockups/tasks-pipeline.html`
(the source of truth for layout and behavior; open it in a browser).

The Pipeline workspace tab goes away. The Tasks tab becomes the one place to
shape tasks, pick a template, run it, watch it, and zoom into any task.

## Agreed decisions

- **Two views of one run.** List = a metro line (the template drawn as
  stations; tasks are tokens at their current stage) above a grouped, editable
  task list. Graph = the dependency graph with Overview / Cards / Focus zoom.
- **One lens for zooming into a task**, shared by both views: stage strip,
  attempt timeline, that stage-attempt's output (live turns feed or log),
  files touched, acceptance criteria.
- **Run bar is minimal**: List|Graph toggle · `Run N ▾` (or `New run ▾`) ·
  template · status chip · elapsed · Stop (live) / Run N tasks (plan) /
  New run (finished). **Stop** (formerly Cancel) keeps all state and returns
  the leases: in-flight tasks become Interrupted. A stopped run reads
  `Run N · stopped`; a crashed or lost one, `Run N · interrupted`
  (PIPELINE-LEASES-DESIGN.md B1). No cost, no progress bar, no prose counts. Plan shows
  at most one warning line.
- **`Run N ▾` is the run history.** Picking a past run loads its final state;
  picking the live run follows it live; `New run` enters Plan.
- **Vocabulary**: Ready, Queued, Running, Interrupted, Blocked, Done, Not in
  this run. Blocked is never relabelled "Needs you". No invented metaphors.
  **Interrupted** is pipeline work that started and nobody is doing now
  (in_progress, moved there by the pipeline, no live lease;
  PIPELINE-LEASES-DESIGN.md). It is a pipeline-only word: never a task's
  status outside the Tasks tab's run views, and never Blocked's alarm tone.
  Live/past runs group it after Queued (it waits for a run, not a person);
  Plan lists it under Will run, included by default and excludable, with
  "Interrupted · resumes at <stage>" or "Interrupted · restarts under
  <template>, keeps its work" (plus "· shared directory" in
  current-directory mode), from `GET /sessions/{id}/pipeline/pickup`. A task
  someone else set in progress stays Not in this run.
- **Metro**: no per-task stage tracks in the list. A station stack shows at
  most 3 tokens; past that, 2 tokens + `+N more`, which opens a filterable
  list. Blocked tokens sit under the stage that blocked them.
- **Esc** closes one layer at a time: detail sheet → popover/stack list →
  lens/focus.
- **Leaving tasks out**: Plan has an include checkbox per task. Drafts can't
  be included (they must be confirmed). A task whose dependency is left out
  is shown as "won't run: depends on X".
- **Past attempts**: when a stage ran more than once, the output header shows
  `attempt N of M ▾`. Attempt numbers are stage.started's `attempt` and
  continue across runs. A picked-up task's timeline starts with a pickup
  marker ("Picked up from Run N · resumes at <stage>"), a resumed attempt
  says "resumed from Run N", a fallback "rerun after interruption", and the
  stages an earlier run completed show done, marked "Run N".
- **Task details** opens the existing `TaskDetailSheet`. Read-only while the
  live run is working on the task (it holds the lease). An Interrupted task,
  or one a person set in progress, is editable. `Edit & retry` on a
  blocked task opens it with the block reason pinned above the fields.
- **Files touched** rows open that file in the Changes tab (`onOpenFile`).
  A task-scoped Changes view is an open question in `CHANGES-DESIGN.md`.
- Pause stays disabled (no engine support).

## Contracts (backend ↔ UI)

1. `POST /pipelines` body gains optional `exclude_task_ids: string[]`. The
   engine never launches an excluded task. A task that can't run *only*
   because a dependency (at any depth) is excluded is left out too. Left-out
   tasks don't make the run incomplete: `pipeline.completed` reports them as
   `left_out` (a count, separate from `skipped`), and the outcome is
   `completed` when nothing is blocked or skipped. A task whose chain also
   hits an unsatisfiable dependency that wasn't excluded (blocked, draft,
   wont_do, needs_review, …) is skipped and the run reads `incomplete`, as
   before. Unknown ids are a 400. If every ready and Interrupted task is
   excluded, 400 "session has no ready or interrupted tasks".
2. `pipeline.started` payload gains `run_id` (the Router's run id, the one
   `POST /pipelines/{id}/cancel` takes) and `excluded_task_ids`. The UI's run
   fold reads `run_id` so Cancel works after a reload.
3. `GET /sessions/{id}/pipeline/tasks/{taskId}/output` and `.../turns` accept
   optional query params `run` (run_seq), `stage`, `attempt`. Omitted → today's
   behavior (latest run, follow the newest log). With `run` only → that run's
   newest log for the task. With `stage`+`attempt` → exactly
   `<stage>-attempt-<n>.log` (turns: that attempt's recorded ref). A file that
   is not the one currently being written streams its contents, then sends
   `end`. Missing file → zero chunks, then `end` (same honest-empty rule).

## Work breakdown

| # | Part | Depends on |
|---|------|-----------|
| B1 | Exclude tasks from a run (router body, engine, `excluded_task_ids` + `run_id` on `pipeline.started`) | — |
| B2 | Output/turns by run, stage, attempt | — |
| U1 | UI client + run fold: start with excludes, stream by run/stage/attempt, `runId` from the fold, Cancel after reload; pure plan-scope helpers (left-out closure, waves); template list + workspace default | contracts |
| U2 | Run bar component | U1 |
| U3 | Task lens component (+ attempt dropdown, Task details / Edit & retry wiring, read-only sheet while running) | U1 |
| U4 | Metro map + grouped task list (List view) | U1 |
| U5 | Graph view: plan waves / critical path / include box; Overview·Cards·Focus; focus lens | U1, U3 |
| U6 | Integration: Tasks tab composes it all; remove Pipeline tab, its dead components, and every route to it (palette, dashboard run history, workspace badge); MADS-UX.md | U2–U5 |
| U7 | E2E: migrate pipeline specs; new journey spec (plan → run → zoom → attempt → Esc → past run) | U6, B1, B2 |
