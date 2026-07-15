# HANDOFF — W6.5 Files tab & merge-back flow

Architect stage output. Executor: read this fully before writing code. Several
decisions here exist to route around real gaps in the current backend, not
style preference — this task has more backend gaps than W6.3 did, read §1 and
§11 carefully before assuming any acceptance criterion maps to a live endpoint.

## 0. Coordination risk (read first)

Same situation W6.3 flagged, still true as of this design: **W6.2 (Session
list & Launchpad)** is a sibling work item on `mads/tsk_6dcad2b35d549a76`,
**not merged into `main`**. Its `App.tsx` `SURFACES` registry (the "one-line
shell registration" the task brief references) and its Router client
scaffolding do not exist on `main`/HEAD today — I checked directly. **W6.3
(Conversation tab) is merged** (`ui/src/conversation/`, `ui/src/router/`) —
reuse its `RouterClient`/`FakeRouterClient`/`fixtures.ts` module exactly, do
not build a second Router client. There is also still **no Session Workspace
tab registry** (Conversation/Tasks/Files/Pipeline switcher within one
session) anywhere in the tree — W6.3 hit this same gap and worked around it
with a standalone demo mount. Follow the identical pattern here (§8).

Before writing code, re-check `main` for whether W6.2's `SURFACES` map or a
session-tab registry has landed since this was written. If it has, wire
`FilesTab` in for real; if not, use the demo-mount workaround and export a
plain component so wiring in later is a one-line add.

## 1. Backend reality check — read this before trusting any endpoint

I read the actual Go handlers (not just MADS-UX) before writing this. Status
as of this design:

**Router (`services/router/internal/api/`) — proxied, real:**
- `GET /events?session_id=&types=` — **exists, and this is the important
  one**: it's the control-plane SSE stream (`services/router/internal/api/
  events.go`), server-side filterable by `session_id` and a comma-separated
  `types` list. `merge.attempted` / `merge.conflicted` / `merge.completed`
  are appended to this same log by the Session Service's runner
  (`services/session/internal/runner/runner.go` `Merge()`, via
  `r.data.Append(...)`) — so **this is where merge status actually flows**,
  not through the per-session data-plane `stream` W6.3 built
  (`RouterClient.streamSession`, which only ever carries provider
  text/tool_use/turn_end/error events). Don't conflate the two streams — see
  §4.

**Does NOT exist on the Router (only on other services, unproxied):**
- `GET /sessions/{id}/files`, `GET /sessions/{id}/commits` — exist only on
  the **Data Service** (`services/data/internal/api/sessions.go` lines
  68–125), not proxied through the Router.
- `POST /merge` — exists only on the **per-session process**
  (`services/session/internal/api/api.go`, backed by `Runner.Merge`), not
  proxied through the Router. Same class of gap W6.3 hit for `/messages` and
  `/interrupt`.

**Does NOT exist anywhere in the codebase:**
- `POST /system/open-editor {path}` (MADS-UX §3.3's "open in editor" action)
  — grepped, zero hits.
- Any combined `GET /sessions/:id/changes` endpoint, and **no diff-content
  endpoint at all** — `contracts.FileChange` is metadata only (path, change
  type, origin, attributed, actor, timestamp); nothing anywhere generates or
  serves unified-diff text or before/after file content (`git diff`,
  `DiffContent`, etc. — grepped the whole Go tree, zero hits). The Monaco
  diff viewer has no real content source to point at yet.

**This is expected, not a bug to fix here** — same precedent as W6.1/W6.3:
"UI work can start pre-wave against mocked contracts." Build the frontend
against a documented `RouterClient` interface + fixtures, flag the gaps for
whoever owns the Router/Data Service surface next, and **do not add Go code
to `services/router` or `services/data` silently** — flag it in the PR
description instead (it's cross-cutting; other tabs need overlapping
routes).

**The one genuinely bigger gap, read carefully (drives §5/§7 below):** the
merge implementation (`services/session/internal/worktree/worktree.go`
`Manager.Merge`) runs `git merge --no-ff`, and **on conflict it immediately
runs `git merge --abort`** before returning `MergeResult{Conflicted: true,
Branch, Files}`. That means **there is never a real conflicted working tree
to point an editor at** — embedded or external — only the list of paths that
would have conflicted and the (still-existing) branch name. The acceptance
criteria's "embedded merge editor or external-editor handoff on conflict"
cannot be wired to real conflict content today. See §5 for how to handle
this without silently under-delivering.

## 2. Module layout

```
ui/src/router/                    (existing, W6.3 — extend, don't duplicate)
  types.ts                         add FileChange, Commit, MergeResult,
                                    ControlEvent, FileDiff (see §3)
  client.ts                        extend RouterClient + FetchRouterClient
                                    with the methods in §3
  client.test.ts                   add cases for the new fetch methods
  fixtures.ts                      add fixture data + FakeRouterClient
                                    methods, incl. a FakeControlEventSource
                                    that tests can drive (see §7)

ui/src/files/
  FilesTab.tsx / .module.css / .test.tsx
                                    top level: session-vs-task scope switch,
                                    composes ChangeList + CommitList +
                                    DiffViewer (selected-file panel) +
                                    MergeBackFlow entry point (button +
                                    status chip, also shown in a session
                                    header once one exists — out of scope
                                    here, see Non-goals)
  HANDOFF.md                       (this file — keep post-merge as living
                                    doc, per W6.3's precedent, not W6.1's
                                    delete-when-done convention)

  ChangeList/
    ChangeList.tsx / .module.css / .test.tsx
                                    FileChange[] rows: path (AA-contrast per
                                    §13), A/M/D badge, AttributionChip; row
                                    click selects the file for DiffViewer
    CommitList.tsx / .module.css / .test.tsx
                                    Commit[] rows: short hash, message,
                                    AttributionChip (trailer/session/
                                    heuristic/orphan) — see §4, this is a
                                    separate list from ChangeList, not a join
    AttributionChip.tsx / .test.tsx
                                    dumb presentational: AttributionKind ->
                                    Chip tone + label
    attribution.ts / attribution.test.ts
                                    pure functions: attributionForChange(fc),
                                    attributionForCommit(c) -> AttributionKind
                                    (see §4 for the exact mapping)

  DiffViewer/
    DiffViewer.tsx / .module.css / .test.tsx
                                    side-by-side/inline toggle, lazy-loaded
                                    Monaco DiffEditor, "Open in editor" button
                                    (always visible per MADS-UX §3.3 even
                                    when the diff itself can't be fetched)

  MergeBack/
    MergeBackFlow.tsx / .module.css / .test.tsx
                                    target-branch input -> mergeSession() ->
                                    subscribes streamControlEvents filtered
                                    to merge.* for this session -> renders
                                    attempting/conflicted/completed status
    ConflictResolution.tsx / .test.tsx
                                    conflicted-files list + "open worktree in
                                    external editor" handoff + a seed-only
                                    embedded-editor stub (§5 — flagged, not
                                    wired to real content)

  FilesDemo.tsx, files-main.tsx     standalone demo mount, mirrors
                                    ConversationDemo.tsx / conversation-main.tsx
ui/files.html                      new Vite entry, mirrors conversation.html
```

Add `files: resolve(__dirname, "files.html")` to `vite.config.ts`'s
`rollupOptions.input`, and `"files": "vite --open /files.html"` to
`package.json` scripts, exactly mirroring the existing `conversation` entry.

## 3. Contract types (`ui/src/router/types.ts`)

Mirror the Go shapes exactly, same discipline as the existing file's header
comment:

```ts
// mirrors pkg/contracts/session.go FileChange
export type ChangeKind = "added" | "modified" | "deleted";
export type ChangeOrigin = "tool_use" | "commit" | "observed";

export interface FileChange {
  id: number;
  session_id: string;
  task_id?: string;
  repo?: string;
  path: string;
  change: ChangeKind;
  origin: ChangeOrigin;
  attributed: boolean;
  actor?: string;
  timestamp: string; // ISO
}

// mirrors pkg/contracts/session.go Commit
export type LinkedVia = "trailer" | "session_context" | "heuristic";

export interface Commit {
  hash: string;
  session_id: string;
  task_id?: string;
  repo?: string;
  message: string;
  author?: string;
  linked_via?: LinkedVia;
  orphan?: boolean;
  timestamp: string;
}

// mirrors pkg/contracts/session.go MergeResult
export interface MergeResult {
  conflicted: boolean;
  branch?: string;
  files?: string[];
}

// mirrors pkg/contracts/event.go Envelope (subset fields this tab reads).
// `type` is intentionally `string`, not a closed union — the vocabulary
// grows as services land (see event.go's own comment) and this tab only
// ever branches on the three merge.* values it cares about.
export interface ControlEvent {
  seq: number;
  timestamp: string;
  type: string;
  session_id?: string;
  task_id?: string;
  payload?: unknown;
}

// Frontend-only — NO Go equivalent exists anywhere (§1: no diff-content
// endpoint at all). Flag for whoever builds GET .../diff to reconcile field
// names. `language` is a Monaco language id, guessed client-side from the
// path extension — fine to get wrong, it only affects syntax highlighting.
export interface FileDiff {
  path: string;
  language?: string;
  original: string;
  modified: string;
}
```

`RouterClient` additions (`ui/src/router/client.ts`) — extend the existing
interface, do not create a second one:

```ts
export interface RouterClient {
  // ...existing W6.3 methods...
  getFileChanges(sessionId: string, taskId?: string): Promise<FileChange[]>;
  getCommits(sessionId: string): Promise<Commit[]>;
  getFileDiff(sessionId: string, path: string, taskId?: string): Promise<FileDiff>;
  openInEditor(path: string): Promise<void>;
  mergeSession(sessionId: string, targetBranch?: string): Promise<MergeResult>;
  streamControlEvents(filter: { sessionId?: string; types?: string[] }): SessionEventSource;
}
```

`FetchRouterClient` implementations: `getFileChanges`/`getCommits` hit the
Data Service paths from MADS-UX §3.3 verbatim (`GET /sessions/:id/files` /
`/commits`) even though they're not Router-proxied yet — same reasoning as
W6.3's `/mentionables` (§1: "build against the documented contract, land
ahead of proxying"). `getFileDiff` and `openInEditor` hit the MADS-UX-named
paths (`GET /sessions/:id/changes` for diff content — reconcile if a real
shape differs later; `POST /system/open-editor`) even though nothing serves
them today. `mergeSession` posts `POST /sessions/:id/merge` (documented in
MADS-UX §3.3's own "Actions" line) even though only the per-session
unproxied `POST /merge` exists today. `streamControlEvents` is the one
**real** one — wire it to the actual, already-proxied `GET /events?
session_id=&types=merge.attempted,merge.conflicted,merge.completed`, reusing
`SessionEventSource`'s shape but note it needs named-event listening
(`es.addEventListener("merge.completed", ...)`, per the SSE frame format
Router already emits: `event: <type>\ndata: <json>`), not just `"message"`.

## 4. Attribution taxonomy — the acceptance-critical mapping

**Important: `FileChange` and `Commit` are two separate lists with no join
key** (`FileChange` has no commit hash field). Don't invent a join — render
them as two separate lists (`ChangeList` for files, `CommitList` for
commits), both real data shapes, both satisfying "attribution ... from real
observation data shapes" literally:

```ts
export type AttributionKind =
  | "actor"          // FileChange.attributed === true
  | "trailer"        // Commit.linked_via === "trailer"
  | "session"        // Commit.linked_via === "session_context"
  | "heuristic"       // Commit.linked_via === "heuristic"
  | "orphan"          // Commit.orphan === true
  | "unattributed";   // FileChange.attributed === false

export function attributionForChange(fc: FileChange): AttributionKind {
  return fc.attributed ? "actor" : "unattributed";
}

export function attributionForCommit(c: Commit): AttributionKind {
  if (c.orphan) return "orphan";
  if (c.linked_via === "trailer") return "trailer";
  if (c.linked_via === "session_context") return "session";
  if (c.linked_via === "heuristic") return "heuristic";
  return "unattributed"; // defensive fallback; shouldn't occur if the
                          // server sets orphan correctly whenever
                          // linked_via is empty
}
```

**Don't reimplement `contracts.AttributedChange`'s isolation-aware logic
client-side** — `FileChange.attributed` is already computed server-side
(`services/data/internal/store/sessions.go` `RecordFileChange`) with that
rule baked in (worktree isolation ⇒ everything attributed, even
`origin: "observed"`). The frontend only ever branches on the boolean it's
given.

**Chip tones** (`ChipTone` = `neutral|ok|warn|bad|info|draft`, existing kit):
`added` → `ok`, `modified` → `info`, `deleted` → `bad`; `actor` → `neutral`
(label: `fc.actor` if set, else "agent"); `trailer`/`session` → `info`;
`heuristic` → `warn` (lower-confidence guess, spec's own word); `orphan` →
`warn` (REBUILD-SPEC §5: "flagged for user attention," not an error state);
`unattributed` → **`draft` tone** (the kit's existing "uncertain provenance"
treatment) but with the label **"Unattributed"**, not the literal component
`DraftChip`/`Draft` from `ui/src/components/Draft/Draft.tsx` — that
component hardcodes the text "Draft" and its semantic is "proposed content
awaiting acceptance" (synthesis cards, generated agents), which isn't quite
the same claim as "this file changed on disk but wasn't confirmed as session
work." Reuse the `draft` **tone** (plain `<Chip tone="draft">`), not the
`DraftChip` wrapper.

**Data gap worth knowing about, not blocking:** heuristic-candidate
computation (file-overlap analysis, promised by W4.4) and the observed-file
watcher (FSEvents/inotify, also W4.4/REBUILD-SPEC §5/§17) don't appear
implemented anywhere in the current tree — I grepped for `fsnotify`/
`inotify` and found nothing, and `sessions.go`'s own comment says heuristic
candidates "come from file-overlap analysis (Session Service, W4.4)" without
such analysis existing. Don't block on this — the acceptance criteria only
requires rendering the taxonomy correctly against the documented shapes;
seed fixtures with at least one of each `AttributionKind` (including a
`heuristic`-linked commit and an `unattributed` observed file change) so the
component tests exercise all six even though nothing currently produces
`heuristic`/`observed` data for real. Re-check whether W4.4 has landed by
the time this is built — if it has, fixtures can graduate to closer-to-real
shapes, but the taxonomy/rendering logic doesn't change either way.

## 5. Diff viewer & merge conflict resolution — the two Monaco surfaces

These are **different Monaco use cases** built from **different content
sources**; don't conflate them.

**DiffViewer** (real content source exists in principle, just not yet
served — §1): renders `FileDiff.original`/`.modified` via Monaco's diff
editor, side-by-side/inline toggle (a boolean prop passed straight to
Monaco, e.g. `@monaco-editor/react`'s `DiffEditor` `original`/`modified`/
`options.renderSideBySide`). "Open in editor" (`openInEditor(path)`) is
**always visible** per MADS-UX §3.3, independent of whether a diff loaded
successfully.

**ConflictResolution** (no real content source exists at all — §1): the
merge API aborts the merge and never leaves conflict-marker content in the
tree, so there is nothing for an embedded 3-way editor to open against
today, and monaco-editor's OSS npm package doesn't ship VS Code's built-in
merge-editor UI anyway (that's a VS Code-specific feature, not part of the
standalone `monaco-editor` distribution) — even with real content, "embedded
merge editor" would mean an editable Monaco instance over raw
conflict-marker text, not a purpose-built 3-way UI. Given the abort-on-
conflict backend behavior, build:
- The real, usable part: render `MergeResult.files` (paths) and `.branch`,
  with an "Open worktree in external editor" action (`openInEditor` against
  the session's `worktree_dir` — the user resolves manually in their own
  editor/terminal, since nothing here can drive an in-app resolution against
  real data today) and a "Retry merge" action.
- The seed-only part: a `MergeEditor`-shaped stub component that accepts a
  conflict-marker string prop and an `onResolve(text)` callback, wired up
  and tested against a **fixture** string only (satisfies "seeded conflict
  surfaces the resolution flow" — the acceptance criterion is explicitly
  scoped to "component level against faked Router streams," not real
  content). Flag in the PR description, loudly: wiring this to real content
  requires a W4.5 follow-up (either leave conflict markers in the worktree
  instead of aborting, or add a `GET .../conflicts/:path` content endpoint)
  — this is a backend decision, not something to solve by adding Go code
  here silently.

**Lazy-loading Monaco** (shell-startup + test-speed requirement): wrap the
actual editor import in `React.lazy`, not a static top-level import —

```ts
const MonacoDiffEditor = lazy(() =>
  import("@monaco-editor/react").then((m) => ({ default: m.DiffEditor })),
);
```

wrapped in `<Suspense fallback={...}>` in both `DiffViewer` and the seed-only
`MergeEditor` stub. This gets real code-splitting (Vite emits a separate
chunk for the dynamic import, so it's excluded from the initial app bundle)
and testability: `vi.mock("@monaco-editor/react", () => ({ DiffEditor: (props) =>
<div data-testid="fake-diff-editor" ... /> }))` at the top of
`DiffViewer.test.tsx` intercepts the import regardless of static/dynamic
form, so tests assert the right `original`/`modified`/`renderSideBySide`
props reached the (fake) editor without ever mounting real Monaco under
happy-dom — Monaco needs Worker/canvas-measurement APIs happy-dom/jsdom
don't reliably provide, and even a partial mount would be slow and
non-deterministic. This mirrors the `PtyBridge`-stub isolation pattern from
W6.3 (mock the heavy/native boundary, test the wrapper's own logic) — same
spirit, though Monaco is pure JS/WASM with no `node-pty`-style ABI risk, so
it's a plain `dependencies` entry, not `optionalDependencies`.

Recommend `@monaco-editor/react` over raw `monaco-editor` — it has its own
loader/lazy-load machinery and a `DiffEditor` component ready-made, so no
hand-rolled Monaco lifecycle code is needed. Try it without extra Vite
worker config first (the diff view degrades gracefully without dedicated
language-service workers — it's not a full code editor); only add
`vite-plugin-monaco-editor`-style worker wiring if something genuinely
breaks in manual/demo-mount testing. Either way, **flag the new runtime
dependency in the PR description**, per W6.1/W6.3 precedent.

## 6. Target branch default — another real data gap

REBUILD-SPEC §17 says target branch "defaults to the session's base
branch," but `contracts.Session` has no such field (`worktree_branch` is the
session's *own* isolated branch, not what it branched *from*). Build the
target-branch field as a plain, undefined-safe text input with no
pre-filled default (or a static `"main"` placeholder, clearly labeled as a
placeholder, not a real default) — same treatment as W6.3's `ContextMeter`/
trust-pill gaps: don't invent a frontend-only field to paper over a missing
Go one. Flag it; a real default needs a Go-side field addition later.

## 7. Testing the merge-back flow against faked Router streams

`FakeRouterClient.streamControlEvents` needs to be more than the existing
`FakeSessionEventSource` no-op — component tests must be able to **drive**
merge status transitions. Add a small fake that both implements
`SessionEventSource` and exposes a test-only `emit(type, envelope)` method
(or a factory the test imports directly, e.g.
`createFakeControlEventSource()` returning `{ source, emit }`), so
`MergeBackFlow.test.tsx` can:
1. Render with a `FakeRouterClient` whose `mergeSession` resolves with a
   clean `MergeResult` and whose `streamControlEvents` returns a fake
   source; trigger the merge button; call `emit("merge.attempted", {...})`
   then `emit("merge.completed", {...})`; assert the UI reaches a
   "Merged"/completed state.
2. Same shape but `mergeSession` resolves `{conflicted: true, branch,
   files}` and the fake stream emits `merge.attempted` then
   `merge.conflicted` with a fixture `files` list; assert
   `ConflictResolution` renders with those paths.

Both scenarios belong in `fixtures.ts` as named exports (e.g.
`fixtureCleanMerge`, `fixtureConflictedMerge`) so `MergeBackFlow.test.tsx`
and the demo mount (§8) share one source of truth, matching the existing
`fixtureSessions`/`fixtureTranscripts` pattern.

## 8. Demo mount

Same reasoning as W6.3 §8 — no Session Workspace shell exists to mount this
tab inside yet (§0). Add `files.html` / `FilesDemo.tsx` / `files-main.tsx`
wired to `FakeRouterClient` fixtures, with a picker to switch between a
clean-changes session, an unattributed-observation session, and a
conflicted-merge session, reachable via `npm run files`. Export a plain
`FilesTab` component (props: `sessionId`, `taskId?`, `routerClient`) so
wiring into a future tab registry is the promised one-line addition.

## 9. Test-to-acceptance-criteria map

- "Change lists render attribution (trailer/session/heuristic/orphan) from
  real observation data shapes" → `attribution.test.ts` (pure-function unit
  tests against literal `FileChange`/`Commit` fixtures covering all six
  `AttributionKind` values) + `ChangeList.test.tsx`/`CommitList.test.tsx`
  (render against `FakeRouterClient` fixtures, assert the right chip/tone
  per row, including an unattributed-observation row and an orphan-commit
  row).
- "diff viewer renders a seeded diff both ways" → `DiffViewer.test.tsx`,
  mocking `@monaco-editor/react`, asserting `original`/`modified`/language
  reach the mock and that toggling side-by-side/inline flips the prop passed
  down, using a fixture `FileDiff`.
- "A seeded conflict surfaces the resolution flow ... at component level
  against faked Router streams" → `MergeBackFlow.test.tsx` scenario 2 (§7).
- "A clean merge reflects merge.completed ... at component level against
  faked Router streams" → `MergeBackFlow.test.tsx` scenario 1 (§7).
- "make check passes" → holds by construction if Monaco stays lazy-loaded
  and mocked in tests (§5) — nothing under `ui/src/files/` needs a real
  browser, display, or Playwright; `npm run lint`/`tsc --noEmit`/`npm run
  test` all run under the existing happy-dom/jsdom setup.

## 10. Non-goals (explicitly out of scope, don't build)

- Any Go code in `services/router`/`services/data`/`services/session` —
  flag proxy-route and conflict-content gaps (§1, §5), don't implement.
- A real embedded 3-way merge editor wired to actual conflict content — not
  possible against the current abort-on-conflict backend (§5); build the
  seed-only stub and flag loudly.
- File-overlap heuristic computation and the observed-file watcher — W4.4's
  job, not this task's, regardless of whether it's landed yet (§4).
- Multi-repo change lists — `FileChange.repo`/`Commit.repo` exist as hooks
  but multi-repo isolation is feature-flagged/later work (REBUILD-SPEC §17).
- Wiring `FilesTab` into `App.tsx`'s `SURFACES` or a session-tab registry —
  neither exists yet on `main` (§0); re-check at build time.
- The session-header "Merge back" button/entry point from MADS-UX §3.3's
  fuller header spec (Resume/Pause/Merge/Link/fork/delete) — that's the
  Session Workspace header's job (a different, not-yet-built surface); this
  task only owns the Files-tab-local merge-back entry point.

## 11. Risks / judgment calls for human review

1. **Conflict resolution is real-plumbing-plus-fixture-stub, not fully real**
   (§5) — the single biggest scope judgment call in this doc. The
   acceptance criteria's own wording ("at component level against faked
   Router streams") supports this reading, but flag it explicitly in the PR
   description so a human confirms that's the intended v1, not an
   oversight.
2. **Target branch has no real default to read** (§6) — confirm the plain
   text input (no smart default) is acceptable for v1.
3. **Five of six new `RouterClient` methods hit endpoints that don't exist
   yet** (`getFileChanges`, `getCommits`, `getFileDiff`, `openInEditor`,
   `mergeSession` — only `streamControlEvents` is real today). This is a lot
   of speculative surface for one task; if it turns out too much of the
   Router/Data Service work has landed differently by build time, reconcile
   against the real routes rather than the MADS-UX-documented ones.
4. **Monaco is a new, fairly heavy runtime dependency**, first introduction
   in this codebase — confirm the `@monaco-editor/react` + `React.lazy`
   approach (§5) rather than raw `monaco-editor` is acceptable, and that
   skipping custom Vite worker config for v1 doesn't visibly break the demo
   mount.

## 12. README

Per the task brief's UI conventions (as W6.2/W6.3): add a "Files tab"
section to `ui/README.md`, mirroring the existing "Conversation tab"
section's shape (what `npm run files` opens, what it's mounted against,
what's real vs. stubbed, pointer to this HANDOFF for the conflict-resolution
caveat). Update the package's opening paragraph's "does not yet own" list
accordingly (it currently calls out the Session list/Launchpad surface and
tab registry as not-yet-owned — the Files tab should be added to what *is*
owned, with the same "not wired into `App.tsx` yet" caveat).
