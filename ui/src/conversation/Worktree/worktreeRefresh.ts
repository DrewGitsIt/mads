import { useEffect } from "react";
import type { RouterClient } from "../../router/client";
import { markRefreshed } from "../../router/gitUpdating";

/**
 * A focus within this long of a finished recount for the same session is
 * skipped. Switching apps and back is often seconds apart, and each focus
 * used to run `git status` across every repo the session touched (3–17 s on
 * a real checkout) for counts that couldn't have moved unless someone
 * committed outside mads in between. 30 s keeps that case prompt enough (a
 * commit made in a terminal shows within half a minute of coming back, and
 * at once on the next turn end, which recounts anyway) while a burst of
 * alt-tabs costs one recount, not one each.
 */
export const WORKTREE_REFRESH_FOCUS_SKIP_MS = 30_000;

interface Entry {
  client: RouterClient;
  /** The recount in flight, shared by every mount showing the session. */
  inFlight?: Flight;
  /** When the last recount for this session finished (ok or not), by Date.now(). */
  finishedAt?: number;
}

interface Flight {
  ctl: AbortController;
  holders: number;
  /** Settles when the recount does (answered, failed or aborted). */
  settled: Promise<void>;
}

// Window-wide, per session: ConversationTab remounts on every switch, and a
// remount must join the recount already out rather than start another.
const entries = new Map<string, Entry>();

function entryFor(client: RouterClient, sessionId: string): Entry {
  let e = entries.get(sessionId);
  if (!e || e.client !== client) {
    e = { client };
    entries.set(sessionId, e);
  }
  return e;
}

/**
 * Asks the Router to recount the session's Commit/Land counts (it answers
 * 202 at once; they arrive as session.updated), joining one already in
 * flight. A "focus" ask is
 * skipped when one finished within WORKTREE_REFRESH_FOCUS_SKIP_MS. Returns
 * a release: when every asker has released, a recount still running is
 * aborted (the view went away; the next open recounts again), and
 * `settled`, for when releasing no longer matters.
 */
export function requestWorktreeRefresh(
  client: RouterClient,
  sessionId: string,
  reason: "open" | "focus",
): { release: () => void; settled: Promise<void> } {
  const e = entryFor(client, sessionId);
  if (!e.inFlight) {
    if (reason === "focus" && e.finishedAt !== undefined && Date.now() - e.finishedAt < WORKTREE_REFRESH_FOCUS_SKIP_MS) {
      return { release: () => {}, settled: Promise.resolve() };
    }
    const ctl = new AbortController();
    const flight: Flight = {
      ctl,
      holders: 0,
      settled: client
        .refreshWorktreeCounts(sessionId, { signal: ctl.signal })
        // Accepted (202): the Router recounts behind it, and says when
        // (session.git_state_updated) — until then the counts are "updating".
        .then(() => markRefreshed(sessionId))
        // Best-effort, an abort included: the next turn end recounts anyway.
        .catch(() => {})
        .finally(() => {
          if (e.inFlight !== flight) return;
          e.inFlight = undefined;
          if (!ctl.signal.aborted) e.finishedAt = Date.now();
        }),
    };
    e.inFlight = flight;
  }
  const flight = e.inFlight;
  flight.holders++;
  let released = false;
  const release = () => {
    if (released) return;
    released = true;
    flight.holders--;
    if (flight.holders > 0 || e.inFlight !== flight) return;
    e.inFlight = undefined;
    flight.ctl.abort();
  };
  return { release, settled: flight.settled };
}

// Releases a mounted view still holds, window-wide (tests check none leak).
let held = 0;

/**
 * Recounts the on-screen session's Commit/Land counts when it opens and when
 * the window regains focus: they are otherwise only recounted at turn ends
 * and by mads's own Commit/Land, so work committed or merged outside mads
 * would leave them offering what the dialog then finds nothing for.
 */
export function useWorktreeRefresh(client: RouterClient, sessionId: string): void {
  useEffect(() => {
    // Only the recounts still running are held: each is dropped once it settles.
    const releases = new Set<() => void>();
    const ask = (reason: "open" | "focus") => {
      const { release, settled } = requestWorktreeRefresh(client, sessionId, reason);
      releases.add(release);
      held++;
      void settled.then(() => {
        if (releases.delete(release)) held--;
      });
    };
    ask("open");
    const onFocus = () => ask("focus");
    window.addEventListener("focus", onFocus);
    return () => {
      window.removeEventListener("focus", onFocus);
      for (const release of releases) release();
      held -= releases.size;
      releases.clear();
    };
  }, [client, sessionId]);
}

/** How many releases mounted views still hold: only recounts still running. */
export function heldWorktreeRefreshesForTests(): number {
  return held;
}

export function resetWorktreeRefreshForTests(): void {
  held = 0;
  for (const e of entries.values()) e.inFlight?.ctl.abort();
  entries.clear();
}
