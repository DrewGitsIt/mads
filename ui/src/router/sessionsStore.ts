import { router } from "./client";
import { coalesce } from "./coalesce";
import { eventsReady, subscribeToEvents } from "./events";
import { effectiveScope, getMeshSnapshot, meshShowsAll, recordSessionHosts, scopePeer, watchMesh } from "./mesh";
import { SESSION_EVENT_TYPES } from "./sessionEventTypes";
import type { Session } from "./types";

export interface SessionsSnapshot {
  sessions: Session[];
  /** True only until the first fetch ever settles (success or failure). */
  loading: boolean;
  /** Set while the most recent fetch failed; cleared by the next success. */
  error: string | null;
}

/**
 * routerGet has no timeout, and the coalescer is single-flight: a GET that
 * never settles would park every later refetch behind it. Past this, the
 * fetch counts as failed (the normal error + retry path) and frees the slot.
 */
export const SESSIONS_FETCH_TIMEOUT_MS = 15_000;

const RETRY_FLOOR_MS = 500;
const RETRY_BASE_MS = 2_000;
const RETRY_MAX_MS = 30_000;

const INITIAL: SessionsSnapshot = { sessions: [], loading: true, error: null };

// One GET /sessions for the whole window, however many hooks read it. The
// bus once connected with no `since`, so a fresh window replayed the entire
// event log — thousands of session.* events in a few seconds; a reconnect
// after a long gap still brings everything since its cursor. Fetching per event,
// per mounted hook, once fired ~9,000 GETs on load and starved the browser
// of sockets; here every event only asks the coalescer, which runs at most
// one fetch at a time. The snapshot outlives the last listener so a remount
// shows the last list at once and revalidates behind it.
let snapshot: SessionsSnapshot = INITIAL;
const listeners = new Set<() => void>();
let unsubscribeBus: (() => void) | null = null;
// Bumped on stop and reset: a fetch that resolves after either is dropped.
let generation = 0;
let retryTimer: ReturnType<typeof setTimeout> | undefined;
let retryAttempt = 0;
// The last list's wire form, so an unchanged refetch keeps the snapshot (and
// every reader's render) as is.
let lastJson = "";

function setSnapshot(next: SessionsSnapshot): void {
  snapshot = next;
  for (const listener of listeners) listener();
}

function clearRetry(): void {
  if (retryTimer) clearTimeout(retryTimer);
  retryTimer = undefined;
}

function listSessionsWithTimeout(): Promise<Session[]> {
  let timeout: ReturnType<typeof setTimeout> | undefined;
  const timedOut = new Promise<never>((_, reject) => {
    timeout = setTimeout(
      () => reject(new Error("GET /sessions timed out")),
      SESSIONS_FETCH_TIMEOUT_MS,
    );
  });
  // All devices (mesh mode): the Router merges every visible peer's list,
  // each row tagged with its host.
  const opts = meshShowsAll(getMeshSnapshot()) ? ({ scope: "all" } as const) : undefined;
  return Promise.race([router.listSessions(opts), timedOut]).finally(() => clearTimeout(timeout));
}

async function fetchSessions(): Promise<void> {
  const gen = generation;
  clearRetry();
  try {
    const listed = await listSessionsWithTimeout();
    if (gen !== generation) return;
    recordSessionHosts(listed);
    // One other device (mesh mode): the merged list, narrowed to its host.
    const onlyPeer = scopePeer(effectiveScope(getMeshSnapshot()));
    const sessions = onlyPeer ? listed.filter((s) => s.host?.name === onlyPeer) : listed;
    retryAttempt = 0;
    const json = JSON.stringify(sessions);
    if (json === lastJson && !snapshot.loading && snapshot.error === null) return;
    const same = json === lastJson;
    lastJson = json;
    setSnapshot({ sessions: same ? snapshot.sessions : sessions, loading: false, error: null });
  } catch (err) {
    if (gen !== generation) return;
    const error = err instanceof Error ? err.message : String(err);
    // Event-driven refetches alone would leave a blip (a router restart
    // mid-refresh) standing until some unrelated session event came along.
    const delay = Math.max(
      RETRY_FLOOR_MS,
      Math.random() * Math.min(RETRY_MAX_MS, RETRY_BASE_MS * 2 ** retryAttempt),
    );
    retryAttempt++;
    retryTimer = setTimeout(() => {
      retryTimer = undefined;
      coalescer.now();
    }, delay);
    if (error !== snapshot.error || snapshot.loading) {
      setSnapshot({ ...snapshot, loading: false, error });
    }
  }
}

let coalescer = coalesce(fetchSessions);

/**
 * How often the All devices list re-reads while it's showing. Peers' session
 * events don't reach this window's bus: streaming every peer's whole
 * session.* history over a relayed tailnet link on each load would cost far
 * more than a periodic merged GET (the Router re-reads peers itself, too).
 */
export const MESH_POLL_MS = 10_000;

let unsubscribeMesh: (() => void) | null = null;
let pollTimer: ReturnType<typeof setInterval> | undefined;
// What the last fetch was for, so a mesh change that doesn't change the
// list's shape (a peer's rtt) doesn't refetch.
let lastScope: string = "self";

function syncMesh(): void {
  const showsAll = meshShowsAll(getMeshSnapshot());
  const scope = effectiveScope(getMeshSnapshot());
  if (showsAll && pollTimer === undefined) {
    pollTimer = setInterval(() => coalescer.request(), MESH_POLL_MS);
  } else if (!showsAll && pollTimer !== undefined) {
    clearInterval(pollTimer);
    pollTimer = undefined;
  }
  if (scope !== lastScope) {
    lastScope = scope;
    readWhenReady();
  }
}

/**
 * Reads the list now, behind the bus's ready barrier (THE CONTRACT in
 * router/events.ts). The list's local rows are ordered against this
 * window's LOCAL bus, which is what session-agnostic eventsReady() is;
 * peers' rows aren't on this window's bus at all (MESH_POLL_MS polls them).
 */
function readWhenReady(): void {
  const gen = generation;
  void eventsReady().then(() => {
    if (gen === generation) coalescer.now();
  });
}

function start(): void {
  lastScope = effectiveScope(getMeshSnapshot());
  unsubscribeBus = subscribeToEvents({ types: SESSION_EVENT_TYPES }, () => coalescer.request());
  // The mount read waits for the bus's ready barrier (router/events.ts
  // eventsReady), subscribed first: the bus never replays, so a read served
  // before it fixes its cursor could miss an event landing in between.
  readWhenReady();
  unsubscribeMesh = watchMesh(syncMesh);
  syncMesh();
}

function stop(): void {
  generation++;
  unsubscribeBus?.();
  unsubscribeBus = null;
  unsubscribeMesh?.();
  unsubscribeMesh = null;
  if (pollTimer !== undefined) clearInterval(pollTimer);
  pollTimer = undefined;
  coalescer.cancel();
  clearRetry();
}

export function subscribeSessions(listener: () => void): () => void {
  listeners.add(listener);
  if (listeners.size === 1) start();
  return () => {
    if (!listeners.delete(listener)) return;
    if (listeners.size === 0) stop();
  };
}

export function getSessionsSnapshot(): SessionsSnapshot {
  return snapshot;
}

/** Fetch now with the backoff reset — the toast's Retry. */
export function refreshSessions(): void {
  if (listeners.size === 0) return;
  retryAttempt = 0;
  coalescer.now();
}

export function resetSessionsStoreForTests(): void {
  stop();
  // A fresh coalescer too: a test whose fetch never settles would otherwise
  // leave the old one in flight forever, parking every later fetch behind it.
  coalescer = coalesce(fetchSessions);
  listeners.clear();
  snapshot = INITIAL;
  lastJson = "";
  retryAttempt = 0;
  lastScope = "self";
}
