# HANDOFF: ConversationTab never renders blank (loading/error/empty states)

## Problem

`ConversationTab` (`ui/src/conversation/ConversationTab.tsx:36-55`) fetches
`getSession`/`getTranscript` in a `useEffect` with no `.catch` — a rejection
from either becomes an unhandled promise rejection, `session` never leaves
`null`, and `if (!session) return null` (line 55) renders a permanently blank
pane with zero feedback. There's also no distinction between "still loading"
and "failed" (both look identical: blank), and a loaded-but-empty transcript
renders `ChatRenderer` with zero children — a blank scroll area with no
indication whether that's correct or broken. This is the exact failure mode
`ConfigGateFailure`/`App.tsx:46-60,85` already fixed one level up (config
fetch); apply the same shape here, one level down.

## Decision

Keep the two fetches independent (as today — `getSession`/`getTranscript`
resolve at different times, and one succeeding shouldn't wait on the other),
but give each a `.catch` that routes to one shared `loadError` boolean. A
`retryToken` counter, bumped by the error card's Retry button, re-runs the
effect. Three render states, checked in this order ahead of the main return:
`loadError` (error card) → `!session` (loading placeholder) → ready. Inside
the ready render, add one more conditional: `messages.length === 0` in chat
mode swaps `ChatRenderer` for an inline "No conversation yet" note — everything
else in that branch (`SessionSummaryCard`, `Composer`) renders unchanged, so
the existing "inactive session shows its summary card" behavior and "composer
usable while empty" both fall out for free rather than needing new branches.

Reuse `ConfigGateFailure`'s visual language (`Card` + `--bad` left border +
`Button variant="primary"` Retry) but sized for an in-pane card, not a
full-viewport overlay — new styles live in `ConversationTab.module.css`, not
a shared component (this is the only place with this exact loading/error
combination; `ConfigGateFailure` itself is App-level and full-screen, not
reusable here as-is).

## Design

### `ui/src/conversation/ConversationTab.tsx`

```tsx
import { useEffect, useState } from "react";
import { Button } from "../components/Button/Button";
import { Card } from "../components/Card/Card";
import { Chip } from "../components/Chip/Chip";
import type { RouterClient } from "../router/client";
import type { Session, TranscriptMessage } from "../router/types";
import type { PtyBridge } from "./TerminalRenderer/ptyBridge";
import { ConversationHeader } from "./Header/ConversationHeader";
import { ChatRenderer } from "./ChatRenderer/ChatRenderer";
import { TerminalView } from "./TerminalRenderer/TerminalView";
import { SessionSummaryCard } from "./SummaryCard/SessionSummaryCard";
import { Composer } from "./Composer/Composer";
import { ShellDock } from "./ShellDock/ShellDock";
import styles from "./ConversationTab.module.css";

// ...ConversationTabProps / DisplayMode unchanged...

export function ConversationTab({ sessionId, routerClient, ptyBridge }: ConversationTabProps) {
  const [session, setSession] = useState<Session | null>(null);
  const [messages, setMessages] = useState<TranscriptMessage[]>([]);
  const [loadError, setLoadError] = useState(false);
  const [retryToken, setRetryToken] = useState(0);
  const [mode, setMode] = useState<DisplayMode>("chat");
  const [shellOpen, setShellOpen] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setLoadError(false);
    setSession(null);
    setMessages([]);
    void routerClient.getSession(sessionId).then((s) => {
      if (!cancelled) setSession(s);
    }).catch(() => {
      if (!cancelled) setLoadError(true);
    });
    void routerClient.getTranscript(sessionId).then((m) => {
      if (!cancelled) setMessages(m);
    }).catch(() => {
      if (!cancelled) setLoadError(true);
    });
    return () => {
      cancelled = true;
    };
  }, [routerClient, sessionId, retryToken]);

  if (loadError) {
    return (
      <div className={styles.stateWrap}>
        <Card className={styles.errorCard}>
          <div className={styles.errorTitle}>Couldn't load this conversation</div>
          <div className={styles.errorBody}>
            The session or its transcript failed to load.
          </div>
          <Button variant="primary" onClick={() => setRetryToken((t) => t + 1)}>
            Retry
          </Button>
        </Card>
      </div>
    );
  }

  if (!session) {
    return <div className={styles.stateWrap}>Loading session…</div>;
  }

  return (
    <div className={styles.tab}>
      <ConversationHeader
        session={session}
        shellOpen={shellOpen}
        onToggleShell={() => setShellOpen((x) => !x)}
        onRestartAgent={() => void routerClient.bounce(sessionId)}
        onUpdateSession={(patch) => setSession((s) => (s ? { ...s, ...patch } : s))}
      />

      <div className={styles.body}>
        <div className={styles.stream}>
          <div className={styles.modeSwitch}>
            {/* Chip mode switch unchanged */}
          </div>

          {mode === "chat" ? (
            <>
              <SessionSummaryCard session={session} />
              {messages.length === 0 ? (
                <div className={styles.emptyTranscript}>No conversation yet</div>
              ) : (
                <ChatRenderer messages={messages} />
              )}
              <Composer
                routerClient={routerClient}
                sessionId={sessionId}
                agentId={session.agent}
                onSent={(text) => setMessages((m) => [...m, { role: "user", text }])}
              />
            </>
          ) : ptyBridge ? (
            <TerminalView bridge={ptyBridge} className={styles.terminal} />
          ) : (
            <div className={styles.ptyUnavailable}>
              Terminal mode requires the Electron shell.
            </div>
          )}
        </div>

        {/* shellOpen block unchanged */}
      </div>
    </div>
  );
}
```

Everything not shown above (`ConversationTabProps`, `DisplayMode`, the mode
switch `Chip`s, the `shellOpen` dock block) is untouched — copy it through
verbatim from the current file. `retryToken` is intentionally unused inside
the effect body — it's a dependency-array-only trigger, same pattern as
`attempt`-style counters elsewhere in this codebase (e.g. `MergeBackFlow`'s
retry-by-remount patterns); don't destructure or reference it beyond the
dependency array or a linter will flag it as unused — the ESLint config here
does treat effect-dep-only variables as used, but double check `npm run lint`
stays clean.

### `ui/src/conversation/ConversationTab.module.css` — add

```css
.stateWrap {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 1;
  min-height: 0;
  padding: var(--space-6);
  color: var(--text3);
  font: var(--fs-body) var(--sans);
}

.errorCard {
  width: 360px;
  border-left: 3px solid var(--bad);
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.errorTitle {
  font: 650 15px var(--sans);
  color: var(--text);
}

.errorBody {
  font: 400 var(--fs-body) var(--sans);
  color: var(--text2);
}

.emptyTranscript {
  padding: var(--space-4);
  color: var(--text3);
  font: var(--fs-body) var(--sans);
}
```

`.stateWrap` is shared by both the loading text and the error card wrapper
(centers either); `.errorCard`/`.errorTitle`/`.errorBody` mirror
`ConfigGateFailure.module.css`'s `.card`/`.title`/`.body` almost verbatim —
same tokens, smaller width since this sits inside a pane, not the viewport.

## Test plan — `ui/src/conversation/ConversationTab.test.tsx`

Add four cases (existing four must keep passing unmodified — none of their
fixtures touch empty transcripts or rejections):

1. **`shows an error card when getSession rejects, and Retry refetches`**
   ```ts
   const client = new FakeRouterClient();
   vi.spyOn(client, "getSession").mockRejectedValueOnce(new Error("boom"));
   render(<ConversationTab sessionId="session-active" routerClient={client} />);
   await waitFor(() => expect(screen.getByText(/couldn't load/i)).toBeTruthy());
   fireEvent.click(screen.getByRole("button", { name: "Retry" }));
   await waitFor(() =>
     expect(screen.getByText("Add retry backoff to the ingest worker")).toBeTruthy(),
   );
   ```
   `mockRejectedValueOnce` only intercepts the first call; the Retry-triggered
   second call falls through to `FakeRouterClient`'s real `getSession` and
   succeeds, proving Retry actually refetches rather than just clearing the
   error flag.

2. **`shows an error card when getTranscript rejects`** — same shape,
   `vi.spyOn(client, "getTranscript").mockRejectedValueOnce(new Error("boom"))`.
   Only needs to assert the error text appears (covers the "both fetches get
   `.catch` handling" acceptance bullet) — no need to duplicate the full
   retry-succeeds assertion from case 1.

3. **`shows a loading placeholder before the fetches resolve`** — render,
   then assert on the loading text *before* `await waitFor(...)`, e.g.:
   ```ts
   const client = new FakeRouterClient();
   render(<ConversationTab sessionId="session-active" routerClient={client} />);
   expect(screen.getByText(/loading session/i)).toBeTruthy();
   ```
   This works because `FakeRouterClient`'s promises resolve on a microtask,
   not synchronously — the initial render (before React flushes that
   microtask) is guaranteed to hit the `!session` branch first.

4. **`shows an empty-state note when the transcript is empty, with the composer still usable`**
   ```ts
   const client = new FakeRouterClient();
   vi.spyOn(client, "getTranscript").mockResolvedValueOnce([]);
   render(<ConversationTab sessionId="session-active" routerClient={client} />);
   await waitFor(() => expect(screen.getByText("No conversation yet")).toBeTruthy());
   expect(screen.getByPlaceholderText(/message the agent/i)).toBeTruthy();
   ```
   Uses `session-active` (not a new fixture) so this doubles as the "composer
   still usable for active sessions" acceptance bullet — `session-active` has
   no `summary`, so `SessionSummaryCard` correctly renders nothing here,
   isolating the assertion to the new empty-transcript branch.

Don't add a fifth test for "inactive session summary card + empty transcript"
— `SessionSummaryCard`'s own test file already covers the summary-card
rendering logic in isolation; re-asserting it through `ConversationTab` would
be redundant coverage, not new signal.

## Risks / things the reviewer should scrutinize

- **Both fetches restart on every `retryToken` bump, not just the failed
  one.** If `getSession` failed but `getTranscript` had already succeeded,
  Retry re-fetches both. Simpler than tracking per-fetch failure and matches
  the scope note's singular "Retry button that refetches" (not "retries only
  the failed request") — but worth a second look if a future task wants
  finer-grained retry.
- **`loadError` is a single shared boolean**, not per-fetch. A reviewer might
  ask for two distinct error messages ("session failed" vs. "transcript
  failed"); scope only asks for "an in-pane error card naming what failed" —
  interpreted here as naming *that the load* failed, not which of the two
  requests. If the executor's read of the scope differs, splitting into
  `sessionError`/`transcriptError` booleans is a small, contained change to
  the same effect.
- **Resetting `session`/`messages` to `null`/`[]` at the top of the effect**
  means a Retry after a partial failure (e.g. session loaded, transcript
  failed) briefly re-shows the loading placeholder rather than preserving the
  already-good session data during refetch. Chosen for simplicity and
  consistency (every retry looks the same); flag if product wants to preserve
  known-good partial state across a retry.

## Verification

1. `make check` (root) — lint + Go tests + UI lint + UI test. UI test count
   must increase by 4 (see Test plan).
2. No Playwright — this task is component-test only per scope; don't add one.

## Executor notes

Implemented exactly as designed above — no deviations. `ConversationTab.tsx`
and `ConversationTab.module.css` match the Design section verbatim; the four
new tests match the Test plan section verbatim (same names, same assertions).

- All four new tests pass, plus the four pre-existing tests pass unmodified.
  `ui/src/conversation/ConversationTab.test.tsx` now has 8 tests (was 4).
- `make check` is green end to end: golangci-lint (0 issues), `go test -race
  ./...` (all packages ok/cached), `eslint .` (clean), `vitest run` (174 test
  files, 711 tests, all passing).
- Did not touch `sessionError`/`transcriptError` splitting or per-fetch retry
  scoping — both called out as reviewer-discretion items in Risks above, and
  neither is required by the scope's "an in-pane error card naming what
  failed" / "Retry button that refetches" language.
- Did not add a Playwright test, per scope.

Nothing else in the file (props, `DisplayMode`, mode-switch `Chip`s,
terminal/pty branches, `shellOpen`/`ShellDock` block) was touched.

**For the reviewer:** the two risks flagged above by the architect are worth
a second look — (1) Retry re-fetches both session and transcript even if only
one failed, and (2) `loadError` is a single shared boolean rather than
per-fetch. Both are intentional scope calls, not oversights.
