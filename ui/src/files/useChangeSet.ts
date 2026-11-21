import { useCallback, useRef, useSyncExternalStore } from "react";
import { isAbortError, type RouterClient } from "../router/client";
import { coalesce, type Coalescer } from "../router/coalesce";
import { eventsReady, subscribeToEvents } from "../router/events";
import { subscribeRepoChanges } from "../router/repoStateStore";
import type { AgentState, ChangeBaselineId, ChangeSet, Envelope } from "../router/types";

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
 * Events after which a session's change set may differ. session.agent_state
 * counts only when it says idle (see onSessionEvent): a turn's end is when
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

/** Bursts of file writes collapse into one refetch, this long after the last. */
export const CHANGESET_REFRESH_DEBOUNCE_MS = 1500;
/** A steady stream of writes still refetches at least this often. */
export const CHANGESET_REFRESH_MAX_WAIT_MS = 5000;
/** Change sets kept for sessions nobody is showing, so switching back paints at once. */
export const CHANGESET_KEPT_INACTIVE = 16;

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
  if (env.type === "session.agent_state" && (env.payload as AgentState | undefined)?.state !== "idle") return;
  e.coalescer.request();
}

function start(e: Entry): void {
  const gen = e.generation;
  e.unsubscribe?.();
  const offEvents = subscribeToEvents({ sessionId: e.sessionId, types: CHANGESET_REFRESH_EVENT_TYPES }, (env) =>
    onSessionEvent(e, env),
  );
  // Another session's commit takes files out of "uncommitted" here too (COMMIT-LAND-PUSH D10).
  const offRepos = subscribeRepoChanges(e.client, e.sessionId, () => e.coalescer.request());
  e.unsubscribe = () => {
    offEvents();
    offRepos();
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

/** Fetch now, single-flight with anything in flight: Retry, or after the UI itself changed the set. */
export function refetchChangeSet(sessionId: string, vs: ChangeBaselineId): void {
  const e = entries.get(keyOf(sessionId, vs));
  if (e && active(e)) e.coalescer.now();
}

export function resetChangeSetStoreForTests(): void {
  for (const e of entries.values()) stop(e);
  entries.clear();
}

/**
 * The session's ChangeSet against one baseline, refetched (debounced) when
 * the session writes a file, commits, undoes a commit or its turn ends, and
 * when one of its repos' branches moved (repo.changed, COMMIT-LAND-PUSH
 * D10). Every reader of one session and baseline shares one request. A
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
