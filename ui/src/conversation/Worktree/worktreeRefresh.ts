import { useEffect } from "react";
import type { RouterClient } from "../../router/client";

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
  inFlight?: { ctl: AbortController; holders: number };
  /** When the last recount for this session finished (ok or not), by Date.now(). */
  finishedAt?: number;
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
 * Asks the Router to recount the session's Commit/Land counts (they arrive
 * as session.updated), joining one already in flight. A "focus" ask is
 * skipped when one finished within WORKTREE_REFRESH_FOCUS_SKIP_MS. Returns
 * a release: when every asker has released, a recount still running is
 * aborted (the view went away; the next open recounts again).
 */
export function requestWorktreeRefresh(
  client: RouterClient,
  sessionId: string,
  reason: "open" | "focus",
): () => void {
  const e = entryFor(client, sessionId);
  if (!e.inFlight) {
    if (reason === "focus" && e.finishedAt !== undefined && Date.now() - e.finishedAt < WORKTREE_REFRESH_FOCUS_SKIP_MS) {
      return () => {};
    }
    const flight = { ctl: new AbortController(), holders: 0 };
    e.inFlight = flight;
    client
      .refreshWorktreeCounts(sessionId, { signal: flight.ctl.signal })
      // Best-effort, an abort included: the next turn end recounts anyway.
      .catch(() => {})
      .finally(() => {
        if (e.inFlight !== flight) return;
        e.inFlight = undefined;
        if (!flight.ctl.signal.aborted) e.finishedAt = Date.now();
      });
  }
  const flight = e.inFlight;
  flight.holders++;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    flight.holders--;
    if (flight.holders > 0 || e.inFlight !== flight) return;
    e.inFlight = undefined;
    flight.ctl.abort();
  };
}

/**
 * Recounts the on-screen session's Commit/Land counts when it opens and when
 * the window regains focus: they are otherwise only recounted at turn ends
 * and by mads's own Commit/Land, so work committed or merged outside mads
 * would leave them offering what the dialog then finds nothing for.
 */
export function useWorktreeRefresh(client: RouterClient, sessionId: string): void {
  useEffect(() => {
    const releases: (() => void)[] = [requestWorktreeRefresh(client, sessionId, "open")];
    const onFocus = () => releases.push(requestWorktreeRefresh(client, sessionId, "focus"));
    window.addEventListener("focus", onFocus);
    return () => {
      window.removeEventListener("focus", onFocus);
      for (const release of releases) release();
    };
  }, [client, sessionId]);
}

export function resetWorktreeRefreshForTests(): void {
  for (const e of entries.values()) e.inFlight?.ctl.abort();
  entries.clear();
}
