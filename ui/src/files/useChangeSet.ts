import { useCallback, useRef, useSyncExternalStore } from "react";
import { isAbortError, type RouterClient } from "../router/client";
import { coalesce, type Coalescer } from "../router/coalesce";
import { eventsReady, subscribeToEvents } from "../router/events";
import {
  gitStateServed,
  markFromTrigger,
  markRevalidating,
  noteGitStateEvent,
  noteGitStateRead,
  onGitUpdatingFailsafe,
} from "../router/gitUpdating";
import { subscribeRepoChanges } from "../router/repoStateStore";
import type { AgentState, ChangeBaselineId, ChangeSet, Envelope, GitStateUpdatedPayload } from "../router/types";

/**
 * Fills the slices Go may send as null (a nil slice marshals to null) so
 * every consumer can treat them as arrays.
 */
export function normalizeChangeSet(raw: ChangeSet): ChangeSet {
  const files = (raw.files ?? []).map((f) => ({ ...f, turns: f.turns ?? [], commits: f.commits ?? [] }));
  return {
    ...raw,
    baselines: raw.baselines ?? [],
    files,
    turns: raw.turns ?? [],
    commits: (raw.commits ?? []).map((c) => ({ ...c, paths: c.paths ?? [] })),
    totals: raw.totals ?? {
      files: files.length,
      added: files.reduce((n, f) => n + f.added, 0),
      deleted: files.reduce((n, f) => n + f.deleted, 0),
    },
  };
}

/**
 * Events after which a session's change set may differ. The Router
 * recomputes it on these (docs/GIT-STATE-DESIGN.md), so here they only mark
 * it "updating" (router/gitUpdating); the refetch waits for
 * session.git_state_updated naming it changed. Against a Router that
 * computes per request (an older mesh peer) they refetch, as before.
 * session.agent_state counts only when it says idle: a turn's end is when
 * writes no file.changed reported (a shell command's, say) have all landed;
 * the working states in between (one per tool label) change nothing on disk
 * that file.changed doesn't already cover.
 */
export const CHANGESET_REFRESH_EVENT_TYPES = [
  "file.changed",
  "commit.created",
  "commit.undone",
  "session.agent_state",
];
const CHANGESET_BUS_TYPES = ["session.git_state_updated", ...CHANGESET_REFRESH_EVENT_TYPES];

/** Against an older Router: bursts of file writes collapse into one refetch, this long after the last. */
export const CHANGESET_REFRESH_DEBOUNCE_MS = 1500;
/** A steady stream of writes still refetches at least this often. */
export const CHANGESET_REFRESH_MAX_WAIT_MS = 5000;
/** After a cold answer (never computed), read again this long after, announced or not. */
export const CHANGESET_COLD_RETRY_MS = 10_000;
/** Change sets kept for sessions nobody is showing, so switching back paints at once. */
export const CHANGESET_KEPT_INACTIVE = 16;
/**
 * A window opening a session's change set asks the Router to recompute it
 * when the kept one is older than this: edits made outside the agent send
 * no event, and the window shows what the Router last computed.
 */
export const CHANGESET_OPEN_MAX_AGE_MS = 30_000;

export interface ChangeSetState {
  changeSet: ChangeSet | null;
  /** True until the first answer for the current baseline arrives. */
  loading: boolean;
  error?: string;
  refetch: () => void;
}

interface ChangeSetSnapshot {
  changeSet: ChangeSet | null;
  /** An answer (or a failure) has arrived for this session and baseline. */
  loaded: boolean;
  error?: string;
}

const UNKNOWN: ChangeSetSnapshot = { changeSet: null, loaded: false };

/** One session's change set against one baseline: its snapshot, its readers, its one fetch. */
interface Entry {
  key: string;
  sessionId: string;
  vs: ChangeBaselineId;
  client: RouterClient;
  snapshot: ChangeSetSnapshot;
  listeners: Set<() => void>;
  /** Single-flight reads: request() for events (debounced), now() for mount reads and refetch(). */
  coalescer: Coalescer;
  // Bumped on stop: a fetch that settles after it is dropped.
  generation: number;
  inFlight?: AbortController;
  unsubscribe?: () => void;
  lastUsed: number;
  /** The re-read after a cold answer, in case its announcement never comes. */
  coldRetry?: ReturnType<typeof setTimeout>;
  /** The first read since the entry gained a reader hasn't answered yet: it asks for a revalidate if old. */
  opening: boolean;
}

// The rail (ContextRail) and the Changes tab both show a session's change
// set; each used to fetch its own. One entry per session and baseline for
// the whole window, like repoStateStore: readers share one snapshot and one
// request per trigger, a snapshot outlives its last reader so a remount
// paints at once and revalidates behind it, and the request in flight is
// aborted when the last reader leaves (the Router then stops its git work).
const entries = new Map<string, Entry>();
let clock = 0;

function keyOf(sessionId: string, vs: ChangeBaselineId): string {
  return `${sessionId}|${vs}`;
}

function active(e: Entry): boolean {
  return e.listeners.size > 0;
}

function setSnapshot(e: Entry, next: ChangeSetSnapshot): void {
  e.snapshot = next;
  for (const listener of e.listeners) listener();
}

async function fetchEntry(e: Entry): Promise<void> {
  const gen = e.generation;
  const ctl = new AbortController();
  e.inFlight = ctl;
  try {
    const cs = await e.client.getChangeSet(e.sessionId, e.vs, { signal: ctl.signal });
    if (gen !== e.generation || entries.get(e.key) !== e) return;
    const served = typeof cs.updating === "boolean";
    noteGitStateRead(e.sessionId, "changeset", served, cs.updating === true, e.vs);
    if (e.opening) {
      e.opening = false;
      // Opened: recompute behind what's shown if it is old (the Router
      // decides, by its own clock). Not while one already runs.
      if (served && !cs.updating) {
        e.client
          .revalidateGitState(e.sessionId, { parts: ["changeset"], vs: e.vs, max_age_ms: CHANGESET_OPEN_MAX_AGE_MS })
          .catch(() => {});
      }
    }
    // Never computed yet (docs/GIT-STATE-DESIGN.md): still loading; the
    // compute's session.git_state_updated brings the set, and one more read
    // a while later makes sure a missed announcement can't leave it loading.
    if (served && cs.updating && !cs.computed_at) {
      if (!e.coldRetry) {
        e.coldRetry = setTimeout(() => {
          e.coldRetry = undefined;
          if (gen === e.generation && active(e)) e.coalescer.now();
        }, CHANGESET_COLD_RETRY_MS);
      }
      return;
    }
    setSnapshot(e, { changeSet: normalizeChangeSet(cs), loaded: true });
  } catch (err) {
    if (gen !== e.generation || entries.get(e.key) !== e) return;
    // Aborted because nobody reads it any more: not the user's error.
    if (isAbortError(err)) return;
    setSnapshot(e, {
      changeSet: e.snapshot.changeSet,
      loaded: true,
      error: err instanceof Error ? err.message : String(err),
    });
  } finally {
    if (e.inFlight === ctl) e.inFlight = undefined;
  }
}

function onSessionEvent(e: Entry, env: Envelope): void {
  if (env.type === "session.git_state_updated") {
    const p = env.payload as GitStateUpdatedPayload | undefined;
    noteGitStateEvent(p);
    if (!Array.isArray(p?.parts) || !p.parts.includes("changeset")) return;
    const baseline = p.baseline && p.baseline !== "start" ? p.baseline : "start";
    if (baseline === e.vs) {
      // Only a changed set is worth a read: the kept one is what's on screen.
      if (Array.isArray(p.changed) && p.changed.includes("changeset")) e.coalescer.now();
    } else if (baseline === "start") {
      // Session start's recompute followed a trigger, which left the other
      // baselines stale on the Router: reading one (cached, cheap)
      // recomputes it behind the answer, and its own announcement follows.
      e.coalescer.now();
    }
    return;
  }
  if (env.type === "session.agent_state" && (env.payload as AgentState | undefined)?.state !== "idle") return;
  if (!markFromTrigger(e.sessionId, env)) e.coalescer.request();
}

function start(e: Entry): void {
  const gen = e.generation;
  e.unsubscribe?.();
  const offEvents = subscribeToEvents({ sessionId: e.sessionId, types: CHANGESET_BUS_TYPES }, (env) =>
    onSessionEvent(e, env),
  );
  // Another session's commit (or a branch switch, stash or reset) takes
  // files out of "uncommitted" here too (COMMIT-LAND-PUSH D10): the Router
  // recomputes the change set on its repo.changed and announces it
  // (repoStateStore marks it updating), so only an older Router, which
  // computes per request, is asked again here.
  const offRepos = subscribeRepoChanges(e.client, e.sessionId, () => {
    if (gitStateServed(e.sessionId) !== true) e.coalescer.request();
  });
  // A lost announcement: the fail-safe re-reads.
  const offFailsafe = onGitUpdatingFailsafe(e.sessionId, "changeset", () => e.coalescer.now(), e.vs);
  e.opening = true;
  e.unsubscribe = () => {
    offEvents();
    offRepos();
    offFailsafe();
  };
  // The read waits for the bus's ready barrier (router/events.ts
  // eventsReady), subscribed first: the bus never replays, so a read served
  // before it fixes its cursor could miss an event landing in between. The
  // session's own bus: a remote session's events come from its host.
  void eventsReady(e.sessionId).then(() => {
    if (gen === e.generation && entries.get(e.key) === e && active(e)) e.coalescer.now();
  });
}

function stop(e: Entry): void {
  e.generation++;
  e.coalescer.cancel();
  if (e.coldRetry) clearTimeout(e.coldRetry);
  e.coldRetry = undefined;
  e.unsubscribe?.();
  e.unsubscribe = undefined;
  e.inFlight?.abort();
  e.inFlight = undefined;
}

function evictInactive(): void {
  const idle = [...entries.values()].filter((e) => !active(e)).sort((a, b) => a.lastUsed - b.lastUsed);
  for (const e of idle.slice(0, Math.max(0, idle.length - CHANGESET_KEPT_INACTIVE))) entries.delete(e.key);
}

/**
 * The entry for this client. Another client for the same session (a test's
 * fresh fixture router) starts over: its answers aren't the old one's.
 */
function entryFor(client: RouterClient, sessionId: string, vs: ChangeBaselineId): Entry {
  const key = keyOf(sessionId, vs);
  const existing = entries.get(key);
  if (existing && existing.client === client) return existing;
  const e: Entry = {
    key,
    sessionId,
    vs,
    client,
    snapshot: UNKNOWN,
    listeners: existing?.listeners ?? new Set(),
    coalescer: coalesce(() => fetchEntry(e), {
      waitMs: CHANGESET_REFRESH_DEBOUNCE_MS,
      maxWaitMs: CHANGESET_REFRESH_MAX_WAIT_MS,
    }),
    generation: 0,
    lastUsed: ++clock,
    opening: false,
  };
  if (existing) {
    stop(existing);
    for (const listener of e.listeners) listener();
  }
  entries.set(key, e);
  if (existing && active(e)) start(e);
  return e;
}

export function subscribeChangeSet(
  client: RouterClient,
  sessionId: string,
  vs: ChangeBaselineId,
  listener: () => void,
): () => void {
  const e = entryFor(client, sessionId, vs);
  const wasActive = active(e);
  e.listeners.add(listener);
  e.lastUsed = ++clock;
  // First reader: load, or revalidate the snapshot kept from before.
  if (!wasActive) start(e);
  return () => {
    if (!e.listeners.delete(listener)) return;
    if (active(e)) return;
    stop(e);
    e.lastUsed = ++clock;
    evictInactive();
  };
}

export function getChangeSetSnapshot(client: RouterClient, sessionId: string, vs: ChangeBaselineId): ChangeSetSnapshot {
  const e = entries.get(keyOf(sessionId, vs));
  return e && e.client === client ? e.snapshot : UNKNOWN;
}

/**
 * Retry, or after the UI itself changed the set: against a Router that
 * keeps git state, ask it to recompute (POST /worktree/refresh naming the
 * change set: what it kept may predate an edit no event reported), then
 * read; otherwise just read. Single-flight with anything in flight.
 */
export function refetchChangeSet(sessionId: string, vs: ChangeBaselineId): void {
  const e = entries.get(keyOf(sessionId, vs));
  if (!e || !active(e)) return;
  if (gitStateServed(sessionId) !== true) {
    e.coalescer.now();
    return;
  }
  const gen = e.generation;
  void e.client
    .revalidateGitState(sessionId, { parts: ["changeset"], vs })
    .then(() => markRevalidating(sessionId, "changeset", vs))
    .catch(() => {})
    .finally(() => {
      if (gen === e.generation && entries.get(e.key) === e && active(e)) e.coalescer.now();
    });
}

export function resetChangeSetStoreForTests(): void {
  for (const e of entries.values()) stop(e);
  entries.clear();
}

/**
 * The session's ChangeSet against one baseline, as the Router keeps it:
 * refetched when session.git_state_updated says it changed (the Router
 * recomputes it when the session writes a file, commits, undoes a commit
 * or its turn ends, and when one of its repos' branches moved —
 * docs/GIT-STATE-DESIGN.md). `loading` until the first computed answer. Every reader of one session and baseline shares one request. A
 * refetch keeps the previous set on screen until the new one lands, and so
 * does a baseline switch within a session, so selection and scroll don't jump.
 */
export function useChangeSet(routerClient: RouterClient, sessionId: string, vs: ChangeBaselineId = "start"): ChangeSetState {
  const subscribe = useCallback(
    (listener: () => void) => subscribeChangeSet(routerClient, sessionId, vs, listener),
    [routerClient, sessionId, vs],
  );
  const get = useCallback(() => getChangeSetSnapshot(routerClient, sessionId, vs), [routerClient, sessionId, vs]);
  const snap = useSyncExternalStore(subscribe, get, get);
  const refetch = useCallback(() => refetchChangeSet(sessionId, vs), [sessionId, vs]);
  // A new session must not show the previous session's files; a new baseline may, until its own arrive.
  const last = useRef<{ sessionId: string; changeSet: ChangeSet } | null>(null);
  if (snap.changeSet) last.current = { sessionId, changeSet: snap.changeSet };
  const changeSet = snap.changeSet ?? (last.current?.sessionId === sessionId ? last.current.changeSet : null);
  return { changeSet, loading: !snap.loaded, error: snap.error, refetch };
}
