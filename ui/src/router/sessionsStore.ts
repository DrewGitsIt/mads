import { router } from "./client";
import { coalesce } from "./coalesce";
import { subscribeToEvents } from "./events";
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
// bus connects with no `since`, so a fresh window replays the entire event
// log — thousands of session.* events in a few seconds. Fetching per event,
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
  return Promise.race([router.listSessions(), timedOut]).finally(() => clearTimeout(timeout));
}

async function fetchSessions(): Promise<void> {
  const gen = generation;
  clearRetry();
  try {
    const sessions = await listSessionsWithTimeout();
    if (gen !== generation) return;
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

function start(): void {
  coalescer.now();
  unsubscribeBus = subscribeToEvents({ types: SESSION_EVENT_TYPES }, () => coalescer.request());
}

function stop(): void {
  generation++;
  unsubscribeBus?.();
  unsubscribeBus = null;
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
}
