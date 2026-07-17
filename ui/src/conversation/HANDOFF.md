# Shell dock keyboard shortcut (Ctrl+`)

## Task

Give the existing Shell dock toggle (kebab menu → "Shell" / "Hide Shell" in
`ConversationHeader.tsx`) the IDE-conventional `Ctrl+\`` keyboard shortcut,
registered the same way the ⌘K palette shortcut is registered
(`App.tsx`'s document-level `keydown` listener). Active only while a session
workspace is open; fires everywhere including the Composer (no
input-focus guard — see "Why no input guard" below). Advertise the shortcut
on the affordance(s), not just implement it silently.

## The plumbing problem (why this isn't a one-line change)

`shellOpen` is local `useState` inside `ConversationTab.tsx`
(`ui/src/conversation/ConversationTab.tsx:44`), four component layers below
`App.tsx`: `App` → `SessionsSurface` (owns `selectedId`) → `SessionWorkspace`
(owns `activeTab`/`visited`) → `tabRegistry`'s `ConversationTabView` →
`ConversationTab`. None of the intermediate layers know or care about the
shell-dock state today. Threading a callback prop down through all four to
satisfy "register the physical listener in App.tsx" would mean changing
every one of those components' prop signatures for a single boolean toggle —
out of proportion for this task and it touches components three other
work items own.

**Decision: a tiny cross-tree signal, not prop drilling.** `App.tsx` owns the
physical `keydown` listener (matching the ⌘K pattern exactly) and, on
`Ctrl+\``, fires a plain `window` `Event` rather than reaching into state it
doesn't own. `ConversationTab` subscribes to that event in a `useEffect` and
flips its own `shellOpen`. This is the same `addEventListener`/
`dispatchEvent` idiom already used elsewhere in this codebase for
loosely-coupled signals (`GraphCanvas.tsx`, `Popover.tsx`,
`router/events.ts`) — not a new abstraction, just applied across the
App/ConversationTab boundary instead of within one component.

This also gives "active only when a session workspace is open" for free:
nothing is listening unless `ConversationTab` is mounted, which only happens
once a session is selected in `SessionsSurface` (`selectedId` set) — no
separate "is a workspace open" flag needs threading up to `App.tsx` either.

**Known scope gap, explicitly not fixed here:** `SessionWorkspace` keeps
every *visited* tab mounted (hidden) when you switch tabs
(`SessionWorkspace.tsx:100`, the `visited` Set), so once Conversation has
been opened once in this session, the signal listener stays registered even
while looking at Tasks/Files/Pipeline — matches VS Code (terminal toggles
regardless of active editor). But if a workspace opens *directly* into a
non-Conversation tab and Conversation is never visited (e.g. the palette's
"Start pipeline" command lands on the Pipeline tab,
`commandSource.ts` / `App.tsx:102-109`), the shortcut is a no-op until the
user visits Conversation at least once, because that's the only place
`ShellDock` is rendered at all. Fixing that for real means moving
`ShellDock`'s render location out of `ConversationTab` up to
`SessionWorkspace` (workspace-level panel, not tab-level) — a real
improvement, but a bigger structural change than "add a shortcut to an
existing toggle," and not what was asked. Flag it, don't build it here.

## Why no input-focus guard

Read `Composer.tsx`'s `handleKeyDown` (`ui/src/conversation/Composer/
Composer.tsx:51`) — it has no `stopPropagation`, so `App.tsx`'s
document-level listener already fires while the Composer's textarea has
focus, today, for ⌘K. There's no existing "skip when a text input is
focused" guard anywhere in this codebase's global shortcuts to begin with.
So: add none for Ctrl+` either. This isn't a gap to close, it's already the
behavior — don't add `e.target` checks.

## Files to touch

1. **New: `ui/src/conversation/shellToggleSignal.ts`**
   ```ts
   export const SHELL_TOGGLE_EVENT = "mads:toggle-shell";

   export function requestShellToggle() {
     window.dispatchEvent(new Event(SHELL_TOGGLE_EVENT));
   }
   ```
   No payload needed (it's a pure toggle signal), so a plain `Event`, not
   `CustomEvent`. Keep this file tiny — it's a signal name plus one
   dispatcher, not a hook or a store. `ConversationTab` subscribes inline
   with its own `useEffect` (see below) rather than a bespoke
   `useShellToggleSignal` hook — it has exactly one consumer, a wrapper
   hook would be pure indirection.

2. **`ui/src/App.tsx`**
   - Extend the existing keydown handler (`App.tsx:73-82`) with an `else if`
     branch: `e.ctrlKey && e.key === "\`"` → `e.preventDefault()` +
     `requestShellToggle()`. Keep it in the *same* listener/effect as ⌘K,
     don't add a second `useEffect` — that is the "same place" Drew asked
     for.
   - In `handleDispatch` (`App.tsx:94-121`), split the `"toggle-shell"` case
     out of the current no-op group and call `requestShellToggle()`. It's
     currently bundled with `"open-settings"` under the comment `// no-op:
     no shell-toggle or settings surface exists yet` — that comment is
     already stale (the kebab Shell toggle has existed since W6.3); fix the
     comment to read `// no-op: no settings surface exists yet` once
     `toggle-shell` is no longer part of that group. Wiring this costs
     nothing extra (same `requestShellToggle()` call) and turns the
     palette's existing "Toggle shell" entry from a dead affordance into a
     working one — leaving it a no-op while advertising a shortcut on it
     would be the same "affordance promises something untrue" problem this
     codebase has explicitly corrected before (see the trust-pill work:
     `ui/src/features/sessions/launchpad/pills/HANDOFF.md` in git history,
     `daea80e`).

3. **`ui/src/conversation/ConversationTab.tsx`**
   - Import `SHELL_TOGGLE_EVENT` from `./shellToggleSignal`.
   - Add a `useEffect` (near the other effects, `useEffect`/`useCallback`
     are already imported) that adds/removes a `window` listener on
     `SHELL_TOGGLE_EVENT` calling `setShellOpen((x) => !x)`. Empty deps
     array — `setShellOpen` is stable.

4. **`ui/src/conversation/Header/ConversationHeader.tsx`**
   - The kebab menu's Shell button (`ConversationHeader.tsx:134-143`) needs
     the shortcut on `title` and `aria-keyshortcuts`, e.g.
     `title="Toggle Shell (Ctrl+\`)"` `aria-keyshortcuts="Control+\`"`.
   - **Do not change the visible button text** (`{shellOpen ? "Hide Shell" :
     "Shell"}`) — `ConversationHeader.test.tsx:101` asserts
     `screen.getByText("Shell")` exactly; appending `" (Ctrl+\`)"` to the
     label breaks that assertion for no acceptance-criteria benefit ("title/
     aria", not label text, is what was asked for).

5. **`ui/src/conversation/ShellDock/ShellDock.tsx`**
   - The doc comment already says `(⌘\`)` (`ShellDock.tsx:12`) — that's
     stale/wrong (wrong modifier key, and it wasn't wired to anything until
     now). Correct it to `(Ctrl+\`)` now that it's real. One-line fix, but
     leaving an incorrect shortcut symbol in a doc comment that's now
     literally true for the wrong key is worth catching.

6. **`ui/src/features/command-palette/commandSource.ts`**
   - Add `detail: "Ctrl+\`"` to the `action:toggle-shell` entry in
     `STATIC_COMMANDS` (`commandSource.ts:29`). `CommandPalette.tsx` already
     renders `command.detail` as a second line
     (`CommandPalette.tsx:98`) — this is the palette's existing mechanism
     for exactly this kind of hint, no new UI needed.
   - `commandSource.test.ts:59,67` only asserts the `kind` sequence, not
     `detail`/`label` content — adding `detail` won't break it.

## Test plan

Primary test (the one the acceptance criteria wants — dispatches the real
keydown, exercises the actual `App.tsx` registration + the "session open"
gate): add to **`ui/src/App.test.tsx`**, alongside the existing "App command
palette (W6.11)" describe block. Reuse that block's established
`stubRouterFetch()` + jump-to-session flow (`App.test.tsx:163-182` is the
template — open the palette with `fireEvent.keyDown(document, { key: "k",
metaKey: true })`, click `screen.getByTestId("jump:session-active")`, wait
for the Conversation tab to be selected). Then:

```ts
fireEvent.keyDown(document, { key: "`", ctrlKey: true });
```

`useElectronPtyBridge()` resolves to `undefined` in this test environment
(no `window.mads`), so a real `ShellDock`/`TerminalView` won't mount — that's
fine and not worth faking. Assert on the `ptyBridge`-less branch that's
already in `ConversationTab.tsx:170-173`: `screen.getByText(/Shell requires
the Electron shell/)` appearing after the keydown is sufficient proof
`shellOpen` flipped. Press it again and assert that text is gone (round
trip). Add a second case for "no effect elsewhere": fire the same keydown
before any session is selected (still on the Launchpad) and assert that text
never appears — nothing is listening yet, so this should already just pass,
but assert it explicitly since it's a named acceptance criterion.

Optional/supplementary, not required for acceptance: a
`ConversationTab.test.tsx` case mirroring the existing "toggles the Shell
dock from the header kebab menu" test (`ConversationTab.test.tsx:79-97`),
but dispatching `window.dispatchEvent(new Event(SHELL_TOGGLE_EVENT))`
directly instead of clicking the kebab button, using the real
`makeStubBridge()` already in that file — this gets you the
`[data-testid="resize-handle"]` assertion the App-level test can't reach
without also stubbing `window.mads`. Nice-to-have coverage of the signal
wiring in isolation, but it doesn't exercise the actual keydown, so it can't
substitute for the App.test.tsx case above.

`make check` must stay green (Go tests untouched by this task, but the full
suite runs regardless; UI: `eslint`, `vitest`, `tsc --noEmit`).

## Non-goals

- Not moving `ShellDock` to render at the `SessionWorkspace` level (see
  "Known scope gap" above) — flagged, not built.
- Not adding any `Cmd+\`` / macOS-native binding. Drew asked specifically for
  `Ctrl+\`` cross-platform (IDE convention — VS Code binds `Ctrl+\`` on every
  platform, not `Cmd+\`` on Mac), so `e.ctrlKey` only, no `e.metaKey` branch
  for this one (unlike ⌘K, which does check both).
- Not adding an input-focus guard (see "Why no input-focus guard" above).

## Executor outcome

Built exactly per the plan above — all six files, in the order listed,
with no deviations:

1. `shellToggleSignal.ts` — new, `SHELL_TOGGLE_EVENT` + `requestShellToggle()`
   as specified.
2. `App.tsx` — `else if` branch in the existing keydown effect;
   `"toggle-shell"` split out of the no-op group in `handleDispatch` and now
   calls `requestShellToggle()`; stale comment corrected.
3. `ConversationTab.tsx` — `useEffect` subscribing to `SHELL_TOGGLE_EVENT`,
   flips `shellOpen`.
4. `ConversationHeader.tsx` — `title`/`aria-keyshortcuts` added to the Shell
   button; visible label left untouched (`ConversationHeader.test.tsx:101`
   still passes unmodified).
5. `ShellDock.tsx` — doc comment `⌘\`` → `Ctrl+\``.
6. `commandSource.ts` — `detail: "Ctrl+\`"` added to the `toggle-shell`
   static command; `CommandPalette.tsx` already renders `command.detail`
   unconditionally, no template change needed.

Added the two `App.test.tsx` cases from the "Primary test" section
verbatim in intent (jump to `session-active` via the palette, then
Ctrl+\` twice to assert the round trip via the `ptyBridge`-less "Shell
requires the Electron shell" branch; a second case asserts no effect
pre-session-select). Did not add the optional `ConversationTab.test.tsx`
supplementary case — the App-level test already exercises the real
keydown path end to end, and the plan marked it non-required.

`make check` is green: Go tests + `golangci-lint run ./...` (root),
`eslint .`, `tsc --noEmit`, and the full `vitest` suite (177 files / 734
tests, up from 732).

**Reviewer, please scrutinize:** the `handleDispatch` change makes the
palette's "Toggle shell" entry functional for the first time (previously
grouped under a no-op with `open-settings`). That's a small behavior
change beyond "add a shortcut," justified in the plan above (an
advertised-but-dead affordance is worse than not advertising it), but
flagging it explicitly since it wasn't the literal ask.
