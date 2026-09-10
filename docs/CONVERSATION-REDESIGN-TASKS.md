# Conversation-pane redesign — actionable work breakdown

Combines the two reviewed forks: **mockup v3** (`docs/conversation-redesign-mockup.html`,
LGTM 2026-09-09) and the **shadow-git ADR** (`docs/SHADOW-GIT-DIFFS-ADR.md`, Accepted).
Locked design decisions: always-grouped activity, hover-peek on every row, bottom-dock
detail surface (tight-wrapped, outside-click close), V2 working indicator (title
ticker), plan widget, MCP/skill/interrupted vocabulary, live-present subagents
(in-stream card + pinned strip), pinned bottom cluster (strip + indicator + composer
never scroll off screen), historically-accurate diffs via shadow git.

Ordering rule: P0 fixes a live transcript-corruption bug and every later phase reads
its fields — land it first. P1/P2 are independent of each other; P3 needs both.

## P0 · Wire correctness (fixes a live bug)

- [ ] **T1 — Parse subagent tagging.** `providers/claudecode/wire.go`: read
  `parent_tool_use_id`, `subagent_type`, `task_description` on assistant/user lines;
  carry them on `provider.Event`. Events with a parent must no longer emit as the
  main agent's text/tool stream (today they interleave — the bug). Fixture:
  `providers/claudecode/testdata/stream-task-subagent.jsonl`.
- [ ] **T2 — Task lifecycle events.** Normalize `system/task_started·task_progress·
  task_updated·task_notification` into a new `EventTask` (phase, task_id,
  tool_use_id, description, subagent_type, counters {tokens, tool_uses,
  duration_ms}, last_tool_name, final status + summary). Tolerate the
  background-subagent shape: immediate Agent tool_result, second `system/init`, and
  a **second `result` line** per invocation (double turn_end must not corrupt runner
  state or usage records).
- [ ] **T3 — Thinking on the stream.** Emit `thinking` blocks (new event kind or
  flagged text) and surface `system/thinking_tokens` counts for the indicator's
  "Thinking" title.

## P1 · Provider contract (ACP-informed, defensive)

- [ ] **T4 — ToolUse enrichment.** Add `Kind`, `Title`, `Status`, `Locations` to
  `provider.ToolUse` → `pkg/contracts` → TS types (all additive). claudecode adapter
  maps names→kinds (Bash→execute, Grep/WebSearch→search, Read/Glob→read,
  Write/Edit/MultiEdit/NotebookEdit→edit, WebFetch→fetch, Agent→agent,
  `mcp__server__tool`→mcp + server field, Skill→skill, TodoWrite→plan) and builds
  titles server-side (retire UI `summarize()`). Unknown tools → other, never
  `Mutates`.
- [ ] **T5 — Defensive status synthesis.** Happy path accepts provider-reported
  statuses (ACP conformance); defensively synthesize everywhere else:
  in_progress on tool_use arrival, completed/failed on tool_result, **interrupted**
  when the turn ends with no result, pending while permission-parked. Rules live in
  one place (runner or shared pkg), covered by table tests incl. Hermes-shaped
  input (no initial status, completed-only updates, orphaned calls — see capture
  /tmp/wireprobe/hermes-acp.jsonl; copy into testdata if used).
- [ ] **T6 — Subagent persistence.** Runner persists parent-tagged events and
  EventTask into the event log (contracts additions) so subagent streams replay
  after the fact — claude-code's own transcript omits sidechains, so our log is the
  only historical source. Probe & document whether spawn_depth ≥ 2 lines stream
  (unverified).
- [ ] **T7 — goosecli mapping.** When the goose adapter lands, map ACP
  kind/status/title/locations/diff-content through the enriched contract with the
  same defensive layer (relative-path locations, missing statuses).

## P2 · Shadow store (per ADR)

- [ ] **T8 — shadow package.** Session service: per-(session, root) shadow git dirs
  at `$XDG_DATA_HOME/mads/shadow/<session_id>/<root_key>/`; lazy root discovery
  (rev-parse, cached); primary-root baseline backgrounded at provisioning,
  first-touch baselines for other roots; per-mutating-tool `hash-object -w`
  pre-image capture with ~10 MB guard ("too large" marker); turn-end commits in
  dirtied roots; `gc --auto`; `shadow_dir` on the session record.
- [ ] **T9 — Retention sweep.** Delete shadow dirs with their session; 30-day
  post-archive retention (config-overridable). No global cap.
- [ ] **T10 — Diff endpoints.** Router: `GET /sessions/{id}/tool-diff?tool_use_id=`
  (blob pre-image vs tool input / next capture); switch `working_diff.go` original
  to the shadow baseline (replacing the `sessionBaseRev` commit-parent heuristic);
  response caps + caching.

## P3 · UI rebuild (mockup v3 as spec)

- [ ] **T11 — Vocabulary primitives.** KindIcon (22px tiles, per-kind colors),
  status glyphs (✓ ✕ ◔ ⊘ spinner), diffstat; MCP server badge.
- [ ] **T12 — ActivityGroup + Row + Peek.** Always-group consecutive tool events;
  collapsed one-line header (kind set, counts, duration, status); rows with
  start-truncated titles; live turn grows the open group in place and collapses on
  turn_end (replay motion in mockup); hover-peek on every row (input/result
  preview, capped).
- [ ] **T13 — Bottom dock.** Tight-wrapped centered drawer (~980px, rounded
  shoulders) hosting full tool detail (input, output, diff via T10); outside-click +
  Esc close, row-click retarget; replaces inline expansion.
- [ ] **T14 — Plan widget.** TodoWrite renders as an in-place-updating checklist
  (done/current/pending), not rows.
- [ ] **T15 — Subagent presence.** Compact in-stream Task card (live task_progress
  counters) + completed variant with task_notification summary inline; click opens
  the subagent's own grouped stream in the dock; **pinned substrip** while any
  subagent runs (name, current-op ticker, counters).
- [ ] **T16 — Pinned bottom cluster.** Substrip + V2 working indicator (generic
  "Working" + streamed current-title ticker + elapsed) + **composer with Stop/Send**
  in one sticky cluster that can never scroll off screen.
- [ ] **T17 — History parity.** Transcript refetch merges enriched persisted events
  by tool_use ID (kinds/titles/statuses/subagent streams/shadow diff pointers) so
  historical cards render identically to live ones; ToolCard/ChatRenderer retire or
  wrap accordingly.

## P4 · Verification

- [ ] **T18 — Suites + live pass.** RTL tests per new component, Go table tests for
  T2/T5/T8, `make check` green; restart services and validate against a real
  session (incl. a Task-subagent run and a dormant-session reload).

## Deferred (pinned)

- Checkpoint-restore on the shadow store — own design + mockups after this ships.
- Deeper per-harness ACP fidelity work (beyond defensive rules) — explicitly out.
