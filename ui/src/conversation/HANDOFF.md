# Compose expansion: Monaco editor on ⌘E (EXECUTOR HANDOFF)

Implemented per the architect's plan (this file replaced that draft — see git
history for the original if needed). Task: tsk_6505333961b6260e.

## What was done

**Chat mode** (`Composer.tsx`, new `ExpandedComposer.tsx`):
- ⌘E/Ctrl+E and a new visible icon button (⤢/⤡, `title`/`aria-keyshortcuts`
  naming the hotkey) toggle between the plain textarea and a lazy-loaded
  Monaco editor (markdown, word wrap, no minimap/line numbers).
- A single `onKeyDownCapture` on the wrapping `.composer` div owns ⌘E in both
  states — it fires before Monaco's own internal keybinding service ever sees
  the native event, so there's one toggle owner, not two.
- Cursor position round-trips between textarea and Monaco via
  `initialCursor`/`onCursorChange`; focus returns to the textarea at the same
  offset on collapse.
- ⌘Enter/Ctrl+Enter sends from inside Monaco; Escape collapses.
- @-mentions and `/`-commands remain textarea-only, as scoped.
- New `ui/src/utils/createMonacoTheme.ts` defines a `mads` Monaco theme read
  live from `getComputedStyle` against tokens.css custom properties, so it
  tracks whichever `[data-theme]`/`[data-palette]` combination is active.

**Terminal mode** (`TerminalView.tsx`, new `TerminalComposer.tsx`):
- `TerminalView` now takes an optional `onComposeOpen` prop and a
  `forwardRef`-exposed `{ focus() }` handle. ⌘E/Ctrl+E is captured via
  `term.attachCustomKeyEventHandler`, returning `false` so xterm never
  processes it (no stray bytes reach the pty).
- `TerminalComposer` is a fixed-position overlay (ConfirmDialog-style
  backdrop+panel) hosting the same lazy Monaco/markdown setup. Send calls
  `onSend(text)` with the raw text — the caller (`ConversationTab`) owns
  bracketed-paste formatting and the actual `bridge.write`. Escape (via a
  capture-phase handler on the overlay, same mechanism as chat mode) and the
  Cancel button both close and return focus to the terminal via the new
  `TerminalViewHandle.focus()`. Body scroll is pinned while open.
- `formatBracketedPaste(text)` (`\x1b[200~${text}\x1b[201~`) is exported from
  `TerminalComposer.tsx` and used by `ConversationTab`'s `onSend` wiring.
- `ConversationTab.tsx`: added `terminalComposerOpen` state (reset whenever
  the display mode isn't terminal, so a stale open state can't reappear after
  switching back) and a `terminalViewRef`; wired `onComposeOpen`/`ref` into
  `TerminalView` and rendered `TerminalComposer` alongside it.

## What was NOT done

- No dedicated "keybindings surface" UI exists in the codebase; followed the
  existing `title`/`aria-keyshortcuts` convention from `ConversationHeader`
  instead of building one.
- No @-mention/`/`-command resolution in either Monaco surface — explicitly
  out of scope per the brief.
- No per-palette Monarch tokenizer rules for markdown syntax colors — the
  `mads` theme only overrides editor chrome (background/foreground/cursor/
  selection/line-highlight), inheriting `vs`/`vs-dark` defaults for syntax
  highlighting itself.
- No live theme re-sync while an editor is already open (switching light/dark
  mid-session won't repaint an already-mounted Monaco instance) — same
  limitation as the codebase's existing Monaco integrations.

## Bugs fixed along the way (not part of the original brief, but load-bearing for the acceptance criteria)

- **Stale ⌘Enter/Escape handlers**: `@monaco-editor/react`'s `onMount` fires
  exactly once per mount, not on every prop change. The first draft of
  `ExpandedComposer`/`TerminalComposer` registered `addCommand` handlers that
  closed directly over the `onSend`/`onCollapse` props from that first
  render — in `ExpandedComposer`'s case this meant ⌘Enter would have sent
  whatever text existed at the moment Monaco mounted, not what was actually
  typed. Fixed with a ref-mirroring pattern (`onSendRef.current = onSend` on
  every render, dereferenced inside the registered handler) — same idea
  already established in this task's own `TerminalView.tsx` for
  `onComposeOpen`.
- **Double-fired Escape in `TerminalComposer`**: registering Escape both as a
  Monaco `addCommand` and as a `document`-level listener (the `ConfirmDialog`
  pattern) fired `onClose` twice when focus was inside Monaco, since the
  native keydown still bubbles to `document` after Monaco's internal handling
  regardless of the `addCommand` firing. Fixed by dropping both in favor of a
  single capture-phase handler on the overlay's backdrop div, which pre-empts
  Monaco's own keybinding service the same way Composer's outer ⌘E handler
  does.
- **Fragile Monaco tests**: `Composer.test.tsx`'s pre-existing Monaco mock
  invokes `onMount` synchronously inside the mock component's render body
  (not the real component's async-behind-Suspense behavior), which had
  quietly masked both bugs above and made several tests pass only because an
  earlier test in the same file happened to run first and warm the lazy
  import's module cache. Converted the first `getByTestId` after each expand
  to `findByTestId` (matching the established `YamlSourceView.test.tsx`
  convention) so tests aren't order-dependent.

## What the reviewer should scrutinize

- The capture-phase `stopPropagation()` mechanism (both `Composer.tsx`'s ⌘E
  handler and `TerminalComposer.tsx`'s Escape handler) relies on Monaco's
  internal keybinding service attaching its own native listeners rather than
  exposing a React `onKeyDown` prop — this is inferred from
  `@monaco-editor/react`'s public API surface, not verified against a real
  mounted Monaco instance (happy-dom/jsdom can't mount it; the whole suite
  tests against a mock). Worth a manual check in a real browser.
- `ConversationTab.tsx` resets `terminalComposerOpen` via a plain conditional
  `setState` call during render (not inside a `useEffect`) — this is the
  React-sanctioned "adjust state during rendering" pattern for derived state,
  but is easy to mistake for a bug on a quick read.
- The new integration test in `ConversationTab.test.tsx` (⌘E → overlay → send
  → `client.terminalWrite` bracketed-paste assertion) mocks
  `@monaco-editor/react` the same way `TerminalComposer.test.tsx` does —
  duplicated rather than shared, matching the codebase's existing per-file
  mock convention (no shared test-utils module for this exists yet).
- `TerminalComposer`'s props are `{ open, onClose, onSend }` only — no
  `bridge`/`ptyId` props as an earlier draft of the architect's handoff
  sketched, since the concrete `ConversationTab` wiring example in that same
  document has the caller own formatting+write via `onSend`. Chose the
  simpler shape; flagging in case the reviewer expected the redundant props.

## Testing

- `Composer.test.tsx`: 18 tests (6 new/migrated for the Monaco expansion).
- `ExpandedComposer`: covered via `Composer.test.tsx`'s integration-style tests (no separate file).
- `TerminalView.test.tsx`: 7 tests (3 new, covering ⌘E capture and the imperative `focus()`).
- `TerminalComposer.test.tsx`: 10 tests (open/close, ⌘Enter send, Cancel/Escape close exactly once, no mention/command resolution, text clears between opens, `formatBracketedPaste` byte-exact assertions).
- `ConversationTab.test.tsx`: +1 integration test exercising the full terminal-mode ⌘E → overlay → send → `client.terminalWrite` (bracketed-paste-formatted) → focus-return path against the existing `FakeRouterClient`/`FakeEventSource` fixtures.
- Full suite: `npx tsc --noEmit`, `npx eslint .`, and `npx vitest run` (201 files / 1089 tests) all pass clean.
