# HANDOFF — W6.3 Conversation tab

Architect stage output. Executor: read this fully before writing code — several
decisions here exist to route around real gaps in the current codebase, not
just style preference.

## 0. Coordination risk (read first)

W6.2 (Session list & Launchpad) is a **sibling work item, not yet merged** —
at design time its worktree (`tsk_6dcad2b35d549a76`) was still at baseline.
The task brief says to follow "UI conventions: as W6.2" (Router-client
pattern, one-line shell registration), but W6.3's own dependency line in
`docs/MADS-WORK-ITEMS.md` is `W6.1, W4.3, W4.2` — **not W6.2** — so this tab
is designed to be buildable and testable standalone, without waiting for
W6.2 to land.

Before writing code, check `main`/the target branch for whether W6.2 already
added:
- a shared Router API client module (e.g. `ui/src/router/`)
- a Session Workspace tab registry (the "one-line shell registration" point)

If it has, reuse those exactly and skip the equivalent scaffolding below. If
not, build the minimal versions described here, but keep them in their own
module (`ui/src/router/client.ts`) so a later merge of W6.2 can absorb/rename
without a rewrite. **Do not build a second, competing Router client** — that's
the one real merge-conflict risk in this task.

## 1. Backend reality check

Read `services/router/internal/api/api.go` before assuming any endpoint in
MADS-UX §3.3 exists. As of this design:

- **Exists on the Router:** `POST /sessions`, `POST /sessions/{id}/terminate`,
  `POST /sessions/{id}/bounce`, `GET /events` (control-plane), `GET
  /sessions/{id}/output` (raw data-plane passthrough).
- **Does NOT exist on the Router** (only on the per-session process,
  `services/session/internal/api/api.go`, unproxied): `POST /messages`,
  `POST /interrupt`, `GET /stream` (normalized SSE events), `POST /merge`.
- **Does NOT exist anywhere:** `GET /mentionables`, `GET
  /sessions/{id}/transcript`, a Router-proxied `GET /capabilities`.
  `GET /sessions` and `GET /sessions/{id}` exist only on the Data Service
  (`services/data/internal/api/sessions.go`), not proxied through the Router.

**This is expected, not a bug to fix in this task.** W6.1's README precedent
("UI work can start pre-wave-4 against mocked contracts") is the model to
follow: build the frontend against a documented `RouterClient` interface and
fixtures, and land it before backend proxying fully exists. Do not add Go
code to `services/router` as part of this task — if the executor believes a
proxy route is trivial and low-risk to add opportunistically, flag it for a
human decision rather than doing it silently, since it's cross-cutting
(W6.2, W6.8 need overlapping routes) and belongs to whoever owns the Router
surface next.

Acceptance criteria already anticipates this: "composer resolves
mentionables/commands against **a faked Router response set**" — the fake is
the deliverable's test boundary, not a bug.

## 2. Module layout

```
ui/src/router/
  client.ts            RouterClient interface + fetch-backed implementation
  client.test.ts
  types.ts              TS mirrors of Go contracts (see §3)
  fixtures.ts            in-memory fixture data + a FakeRouterClient for
                          dev/demo mounting and component tests

ui/src/conversation/
  ConversationTab.tsx           top-level: display-mode switch, composes
                                 everything below
  ConversationTab.module.css
  ConversationTab.test.tsx
  HANDOFF.md                    (this file)

  Header/
    ConversationHeader.tsx       name/status/pills/context meter, kebab
                                  (Shell, Restart Agent)
    ConversationHeader.module.css
    ConversationHeader.test.tsx
    ContextMeter.tsx              compact usage gauge (see §6 — data gap)
    ContextMeter.test.tsx

  SummaryCard/
    SessionSummaryCard.tsx        renders session.summary (already a
                                  rendered markdown string, see §3) via the
                                  shared markdown renderer
    SessionSummaryCard.module.css
    SessionSummaryCard.test.tsx

  ChatRenderer/
    ChatRenderer.tsx              maps provider.Message[] -> prose + ToolCard
    ChatRenderer.module.css
    ChatRenderer.test.tsx
    ToolCard.tsx                  collapsible tool-call card
    ToolCard.module.css
    ToolCard.test.tsx
    markdown.tsx                  shared renderer (see §7 — sanitization)
    markdown.test.tsx

  TerminalRenderer/
    TerminalView.tsx              xterm.js mounted over a PtyBridge (see §5)
    TerminalView.module.css
    TerminalView.test.tsx         uses an in-memory PtyBridge stub — no
                                   Electron, no node-pty import
    ptyBridge.ts                  PtyBridge interface + useElectronPtyBridge()
                                   hook (reads window.mads.pty)

  Composer/
    Composer.tsx                  textarea, ⌘E expand, send
    Composer.module.css
    Composer.test.tsx
    MentionMenu.tsx                @ / popover (reuses Panel)
    MentionMenu.test.tsx
    useMentionResolver.ts          trigger detection + debounced resolution
    useMentionResolver.test.ts
    InjectedContentPreview.tsx      resolved-content preview before send
    InjectedContentPreview.test.tsx

  ShellDock/
    ShellDock.tsx                  kit's ResizablePanel + a *separate*
                                   TerminalView bound to a general shell pty
                                   (not the agent's own session — see §5)
    ShellDock.module.css
    ShellDock.test.tsx

ui/electron/
  pty/
    ptyHost.ts                     main-process pty lifecycle; dynamic
                                    import("node-pty") in a try/catch
    ptyHost.realtest.ts             real-pty contract test — deliberately
                                    named *.realtest.ts, NOT *.test.ts, so
                                    it is never picked up by the default
                                    `src/**/*.test.{ts,tsx}` vitest include
                                    (see §5)
  preload.ts                       extend contextBridge: window.mads.pty
  main.ts                          register ipcMain.handle pty routes
```

## 3. TS contract types (`ui/src/router/types.ts`)

Mirror the Go shapes exactly — field names and optionality — so wiring a
real endpoint later is a rename, not a redesign:

```ts
// mirrors providers/provider/event.go Message
interface TranscriptMessage {
  role: "user" | "agent" | "tool";
  text?: string;
  tool?: ToolUse;
}
interface ToolUse {
  name: string;
  input?: unknown;   // json.RawMessage
  path?: string;
}

// mirrors providers/provider/event.go Event (data-plane stream)
type StreamEvent =
  | { kind: "text"; text: string }
  | { kind: "tool_use"; tool: ToolUse }
  | { kind: "turn_end"; usage?: Usage; interrupted?: boolean }
  | { kind: "error"; text: string };
interface Usage { input_tokens: number; output_tokens: number; model?: string }

// mirrors pkg/contracts/session.go Session (fields this tab reads)
interface Session {
  id: string;
  name: string;
  activity: "active" | "inactive";
  workspace?: string;
  provider?: string;
  model?: string;
  agent?: string;
  worktree_branch?: string;
  summary?: string;   // ALREADY a rendered markdown string (accomplished/
                       // decisions/remaining sections omitted when empty —
                       // pkg/sessionsummary renders this server-side, so
                       // SessionSummaryCard does no section-presence logic
                       // of its own, just "render if session.summary is
                       // non-empty and session.activity === 'inactive'")
}

// New contract this task introduces on the frontend only (no Go type yet
// — flag for whoever implements GET /mentionables to reconcile):
type MentionableKind = "skill_doc" | "session_summary" | "steering_file";
interface Mentionable { id: string; kind: MentionableKind; label: string; detail?: string }

// mirrors pkg/contracts/registry.go Capability (subset used by / commands)
interface CapabilitySummary {
  id: string;
  name: string;
  type: "skill" | "mcp";
  description?: string;
  healthy: boolean;
}
```

`RouterClient` interface (`ui/src/router/client.ts`):

```ts
interface RouterClient {
  getSession(id: string): Promise<Session>;
  getTranscript(sessionId: string, page?: string): Promise<TranscriptMessage[]>;
  streamSession(sessionId: string): EventSource; // or an async iterator wrapper
  sendMessage(sessionId: string, body: { text: string; mentions?: string[] }): Promise<void>;
  interrupt(sessionId: string): Promise<void>;
  bounce(sessionId: string): Promise<void>; // "Restart Agent" in the UI
  getMentionables(query: string): Promise<Mentionable[]>;
  getSkillCapabilities(agentId?: string): Promise<CapabilitySummary[]>;
}
```

Implement one real (`fetch`-backed) and one fake (`FakeRouterClient` in
`fixtures.ts`, backed by an in-memory array + simple substring filtering).
Component tests and the dev-mode demo mount (§8) use the fake; only
`client.test.ts` itself exercises the fetch-backed implementation (mock
`fetch` there, nowhere else — every other test should inject
`FakeRouterClient`, not mock the network).

## 4. Component design notes

**ConversationHeader** — per the task brief's narrowed scope (not full UX
§3.3): name (editable), status chip (`session.activity`), workspace/repo/
branch line, agent/model/trust pills (click opens a popover; wire the
`onUpdateSession` callback but the popover's own field editor can be a plain
form — the elaborate provider-grouped model picker is Launchpad's job in
W6.2, don't rebuild it here), `ContextMeter`, and a kebab menu with exactly
two items: **Shell** (toggles `ShellDock` open/closed) and **Restart Agent**
(calls `bounce()` — labelled per MADS-UX design rule 9, never "Bounce" in
UI text). Resume/Pause/Merge/Link/fork/delete from the fuller UX §3.3 header
are out of scope — other tabs' work items own them.

**ContextMeter** — data gap: `Usage` has `input_tokens`/`output_tokens` but
there's no context-window-size field anywhere (not on `Session`, no models
catalog endpoint yet). Build the component to accept `usedTokens` and
`contextWindow` as plain number props (undefined-safe, renders nothing if
either is missing) rather than inventing a lookup. Wiring real numbers in is
future work once a models catalog exists — leave a comment pointing at that
gap, don't stub a fake catalog.

**ChatRenderer / ToolCard** — `ChatRenderer` maps `TranscriptMessage[]`:
`role: "user"|"agent"` → prose bubble through the shared markdown renderer;
`role: "tool"` (or a message carrying `.tool`) → `ToolCard`. `ToolCard`
defaults collapsed (header row: tool name chip + one-line summary + chevron,
reuse `Card` + `Chip`); expanded state shows `input` as pretty-printed JSON
in mono and `path` if set. Expand state is local component state, not
persisted.

**TerminalRenderer (`TerminalView`)** — used in **two distinct places**,
easy to conflate, keep them separate in code:
1. The Conversation tab's *terminal display mode* — mirrors the agent's own
   PTY-transport session (spec §11: "PTY-only providers render in terminal
   mode only"). Bound to that session's pty.
2. `ShellDock` — the docked, resizable **general-purpose shell** (⌘\`),
   a separate process in the worktree, nothing to do with the agent.

Both use the same `TerminalView` component (xterm.js `Terminal` + `@xterm/
addon-fit`, mounted in a ref, wired to write/resize/dispose) but each gets
its own `PtyBridge` instance / pty id — never share one pty between the two.

**Composer** — plain textarea, ⌘E toggles an expanded height (no modal, per
UX §3.3). `useMentionResolver` watches cursor position for an active `@` or
`/` trigger (last unescaped trigger char before cursor with no whitespace
since), debounces ~150ms, and calls `getMentionables(query)` for `@` or
filters `getSkillCapabilities(session.agent)` + a static mads-command list
(`/task`, `/model`, `/agent`) for `/`, badging skills by `.healthy`. Selecting
an item splices a plain-text reference token into the composer value and
adds it to a staged "injected content" list shown by `InjectedContentPreview`
— resolved content is a preview only; nothing is fetched/expanded until
send, matching UX §2.4 ("resolves at send time... before anything reaches
the harness").

**SessionSummaryCard** — trivial by design: `session.activity === "inactive"
&& session.summary` → render `session.summary` through the shared markdown
renderer inside a `Card`. No section-presence logic needed client-side (see
§3) — don't rebuild what `pkg/sessionsummary` already renders.

## 5. node-pty / xterm isolation (the acceptance-critical part)

- `node-pty` is a **native addon** compiled against a specific Node ABI.
  Electron's main process runs its own bundled Node/V8, distinct from
  whatever Node runs `npm test`/vitest. Importing `node-pty` anywhere that
  vitest's module graph can reach (i.e. anything under `ui/src/`) risks
  breaking `make test`/`make check` on any machine where that native binary
  isn't present or doesn't match — which is exactly the "no display" CI box
  this criterion is guarding against.
- **Rule: `node-pty` is imported only inside `ui/electron/pty/ptyHost.ts`,
  and only via a dynamic `await import("node-pty")` wrapped in try/catch.**
  `ui/src/**` never imports it, directly or transitively. `TerminalView` and
  everything under `ui/src/conversation/` talk only to the `PtyBridge`
  interface (`spawn`, `write`, `resize`, `kill`, `onData`, `onExit`).
- Add `node-pty` to `ui/package.json` under **`optionalDependencies`**, not
  `dependencies` — mirrors the standard pattern for platform-native optional
  deps (e.g. `fsevents`) and keeps `npm ci` from hard-failing the whole
  install (and therefore `make build`/`make test` at the `ui-deps` target)
  on a box without build tools for it.
- `@xterm/xterm` + `@xterm/addon-fit` are pure JS/DOM — normal
  `dependencies`, safe under happy-dom for component tests.
- `preload.ts` extends `contextBridge.exposeInMainWorld("mads", {...})` with
  a `pty` namespace (`spawn`, `write`, `resize`, `kill` via
  `ipcRenderer.invoke`; `onData`/`onExit` via a wrapped `ipcRenderer.on` that
  returns an unsubscribe function — don't leak raw `ipcRenderer` into the
  renderer, contextIsolation is already on in `main.ts`).
- **Two-tier test split:**
  1. `TerminalView.test.tsx` (vitest, `src/**`, included in `make test`) —
     inject an in-memory `PtyBridge` stub (no Electron, no IPC, no
     node-pty). Assert: mounts, writes to the bridge on user input, calls
     `resize` on container resize, calls `kill`/dispose on unmount. This is
     "the renderer contract."
  2. `ui/electron/pty/ptyHost.realtest.ts` — a **real** pty via a real
     `node-pty` spawn (e.g. spawn `echo hi` or the user's shell, assert
     output round-trips). Name it `*.realtest.ts`, **not** `*.test.ts`, so
     it's excluded from the default `include: ["src/**/*.test.{ts,tsx}"]`
     in `vitest.config.ts` by construction — `npm run test` / `make test`
     never touches it. Give it its own script, e.g. `npm run test:pty`
     backed by a second vitest config (`vitest.electron.config.ts`,
     `environment: "node"`, `include: ["electron/**/*.realtest.ts"]`).
     Inside the test itself, still wrap the `node-pty` import/spawn in
     try/catch and call `ctx.skip()` (or vitest's `test.skip` at runtime)
     on failure — covers the case where someone *does* run `test:pty` on a
     box without a pty (containers, Windows without ConPTY support wired
     up). Document in `ui/README.md` that `test:pty` is optional/manual,
     not part of `make check`.
- Do not try to make the real-pty test part of `make check` "but skip
  gracefully" — that couples the default gate's reliability to a native
  dependency's install success across every dev/CI machine for no benefit;
  the contract test already covers the renderer's correctness.

## 6. Layout: full-width stream, ~92ch prose measure

No existing `--measure-*` token. Add one alongside the other static
(non-palette) exports in `ui/src/tokens/tokens.ts`'s `space`/`radius`
section:

```ts
export const measure = { prose: "92ch" };
```

and the matching `--measure-prose: 92ch;` near `--space-*` in
`tokens.css`. This is a static value, not a palette/theme color, so it is
**not** touched by `tokens.test.ts`'s hex/rgba pinning or byte-identical
check — safe to add without touching that guard. Apply
`max-width: var(--measure-prose)` only to the prose text wrapper inside
`ChatRenderer` (agent/user message text), never to the outer transcript
container, `ToolCard`, or code blocks — those stay full-width per UX §3.3.

## 7. Markdown rendering — security note

Transcript text can contain arbitrary markdown (agent output, and
potentially reflected file/tool content). Do not hand-roll HTML string
concatenation or use `dangerouslySetInnerHTML` on unsanitized parser output.
Recommended: a small well-known parser (`marked` or `micromark`) rendering
to a sanitized tree, or run parser output through `dompurify` before any
`dangerouslySetInnerHTML` use. Code blocks render in mono per design rule 1;
no raw script/style/event-handler attributes should ever reach the DOM.
Flag the chosen library in the PR description since it's a new runtime
dependency.

## 8. Prototype / review artifact

Per the task brief ("prototype first"): since no Session Workspace shell
exists yet to mount this tab inside (W6.2 not landed), don't wire
`ConversationTab` into `App.tsx`'s `RAIL_ITEMS`/tab switch. Instead add a
demo mount analogous to the existing Gallery pattern — e.g. a `conversation`
entry point wired to `FakeRouterClient` fixtures, reachable via `npm run
dev` the same way `npm run gallery` works today — so the tab is visually
reviewable in both display modes and both empty/active/inactive-summary
states without depending on any other work item. Export a plain
`ConversationTab` component (props: `sessionId`, `routerClient`,
`ptyBridge`) so that whenever W6.2's tab registry exists, wiring it in truly
is the promised one-line addition.

## 9. Test-to-acceptance-criteria map

- "Chat and terminal display modes render" → `ConversationTab.test.tsx`
  (mode switch renders `ChatRenderer` vs `TerminalView`).
- "tool cards collapse" → `ToolCard.test.tsx`.
- "composer resolves mentionables/commands against a faked Router response
  set" → `useMentionResolver.test.ts` + `Composer.test.tsx`, both against
  `FakeRouterClient`.
- "Terminal contract tested against a pty stub" → `TerminalView.test.tsx`.
- "real-pty test self-skips when absent" → `ptyHost.realtest.ts`, run via
  `npm run test:pty` (not part of `make check`; see §5 for why that's
  correct rather than a gap).
- "make check passes (including on machines without a display)" → holds by
  construction: `node-pty` is optionalDependency + dynamic-imported
  main-process-only, `*.realtest.ts` is excluded from the default vitest
  include, and nothing under `ui/src/conversation/` touches Electron or a
  real display/browser (happy-dom only, no Playwright).

## 10. Non-goals (explicitly out of scope, don't build)

- Any change to `services/router` Go code (endpoint additions) — flag, don't
  implement (§1).
- Full agent/model/trust pill editing UX (provider-grouped model popover) —
  that's W6.2 Launchpad's job; stub a plain editable popover here.
- Merge-back, fork/rewind, delete, Resume/Pause — other tabs' work items own
  these header actions per the task brief's narrowed kebab scope.
- A models catalog for context-window sizing (§4 `ContextMeter` gap).

## 11. Executor notes

Built per the module layout in §2, in this order: `router/` (types + client
+ fixtures), `ChatRenderer/` (markdown + `ToolCard`), `TerminalRenderer/`
(pty bridge contract + `TerminalView`) + `electron/pty/` isolation, `Composer/`
(mention resolver + menu + injected-content preview), `Header/`
(`ConversationHeader` + `ContextMeter`), `SummaryCard/`, `ShellDock/`, then
top-level `ConversationTab.tsx` + the `conversation.html` demo mount. All 23
test files / 188 tests pass; `npm run lint`, `tsc --noEmit -p tsconfig.json`,
`tsc -p electron/tsconfig.json --noEmit`, and root `make check` (Go tests +
ui lint + ui test) all pass clean.

**Deviations from this doc, flag for review:**

- **`SessionEventSource` instead of a literal `EventSource` return type** on
  `RouterClient.streamSession` (`router/client.ts`). Used a small structural
  interface (`addEventListener`/`removeEventListener`/`close`) instead of the
  DOM `EventSource` type so `FakeRouterClient` doesn't need a real
  `EventSource` polyfill under vitest. Nothing currently consumes the SSE
  stream (no component wires `onmessage` yet — out of scope this task per
  §1's endpoint-existence caveats), so this hasn't been exercised against a
  real stream; whoever wires live streaming in should double check the
  narrowed interface covers what they need.
- **Per-file jsdom environment override for markdown-rendering tests**
  (`markdown.test.tsx`, `SessionSummaryCard.test.tsx`, `ConversationTab.test.tsx`)
  via `// @vitest-environment jsdom`, diverging from this package's blanket
  happy-dom convention (ui/README.md). Found via a standalone probe that
  DOMPurify silently fails to strip `<script>` tags under happy-dom but
  sanitizes correctly under jsdom — a real test-environment gap, not
  something to route around by hand-rolling sanitization. Added `jsdom` as a
  devDependency for this. **Scrutinize this**: it's a security-relevant test
  behavior difference, confirmed real via a throwaway repro script, not
  assumed. Production (Electron's real Chromium DOM) is unaffected either
  way.
- **No `trust` pill in `ConversationHeader`.** MADS-UX §3.3 lists
  "agent/model/trust pills", and `PATCH /sessions/:id` accepts `trust`
  server-side, but `Session` (mirrored from `pkg/contracts/session.go` in
  `router/types.ts`) has no `trust` field for this tab to read — same kind
  of data gap as `ContextMeter`'s context-window size (§4). Rendered
  agent/model pills only, with a comment pointing at the gap, rather than
  inventing a frontend-only field that wouldn't match the Go contract.
- **`ConversationTab`/`ShellDock` share one `PtyBridge` instance** in
  practice (`ConversationDemo.tsx` and any real caller passing one
  `ptyBridge` prop through). This doesn't violate §5's "never share one pty
  between them" — each `TerminalView` mount calls `bridge.spawn()`
  independently and gets its own pty id — but it does mean the two
  terminals go through the same underlying transport object. If a future
  caller needs them on genuinely separate transports (e.g. different IPC
  channels), that's a call-site change, not a `PtyBridge`/`TerminalView`
  change.

**Not done / residual risk:**

- If `node-pty` fails to install as an optional dependency on some target
  machine, `tsc -p electron/tsconfig.json` (invoked only by `npm run build`,
  not by `make check`) could fail to resolve `import("node-pty")` for
  type-checking. Confirmed out of scope for "`make check` passes" since
  `make check` never calls `npm run build`; flagging rather than silently
  leaving unmentioned. `node-pty` did install and build successfully in this
  environment.
- No real browser/visual QA was performed for `conversation.html` — this
  sandbox has no way to render a headless browser and Playwright is
  explicitly out of scope. Verified instead that `npm run dev`-equivalent
  (`vite`) serves and transforms `conversation.html` and every module it
  imports without error, and that the exact same component tree (including
  `TerminalView` mounting real `@xterm/xterm`) is exercised by
  `ConversationTab.test.tsx` under jsdom with `matchMedia`/`ResizeObserver`
  stubs. A human should still eyeball `npm run conversation` before sign-off.
- `ConversationHeader`'s agent/model pill popovers are a plain text-input
  form per §4's explicit instruction not to rebuild W6.2's model picker —
  they don't validate against `getSkillCapabilities`/provider lists, they
  just PATCH whatever string is typed via `onUpdateSession`.
