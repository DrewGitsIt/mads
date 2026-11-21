import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";
import type { RouterClient } from "./client";
import { coalesce, type Coalescer } from "./coalesce";
import { eventsReady, subscribeToEvents } from "./events";
import type { Envelope, RepoChangedPayload, SessionRepoState } from "./types";

export interface SessionReposSnapshot {
  /** Null until the first fetch succeeds: unknown, never "no repos". */
  repos: SessionRepoState[] | null;
  /** Set while the most recent fetch failed; cleared by the next success. */
  error: string | null;
}

/** As SESSIONS_FETCH_TIMEOUT_MS: a GET that never settles mustn't park the single-flight slot. */
export const REPO_STATE_FETCH_TIMEOUT_MS = 15_000;

/**
 * A session's own events after which its repo set or branches may have
 * moved: a record update (a repo joined its set), its Commit, an Undo, a
 * Land. Its repos' repo.changed usually follows too, but a second later.
 */
export const REPO_STATE_SESSION_EVENT_TYPES = ["session.updated", "commit.created", "commit.undone", "merge.completed"];
const BUS_TYPES = ["repo.changed", ...REPO_STATE_SESSION_EVENT_TYPES];

const RETRY_FLOOR_MS = 500;
const RETRY_BASE_MS = 2_000;
const RETRY_MAX_MS = 30_000;

const UNKNOWN: SessionReposSnapshot = { repos: null, error: null };

/** One session's repo state: its snapshot, who reads it, and its own coalesced fetch. */
interface Entry {
  sessionId: string;
  client: RouterClient;
  snapshot: SessionReposSnapshot;
  listeners: Set<() => void>;
  /** Told when one of the session's repos changed (the Changes tab refetches its files). */
  changeListeners: Set<() => void>;
  coalescer: Coalescer;
  // Bumped on stop: a fetch that resolves after it is dropped.
  generation: number;
  retryTimer?: ReturnType<typeof setTimeout>;
  retryAttempt: number;
  lastJson: string;
}

// Branch facts several sessions share (docs/COMMIT-LAND-PUSH-DESIGN.md D10),
// read once per session for the whole window, however many surfaces show
// them (dock, path, Changes chip, push dialog). Like sessionsStore: events
// only ask a session's coalescer, which runs at most one fetch at a time,
// and a snapshot outlives its last reader so a remount shows it at once and
// revalidates behind it. One bus subscription for every session: repo.changed
// has no session, so each is matched to sessions by their repos' git dirs.
const entries = new Map<string, Entry>();
let unsubscribeBus: (() => void) | null = null;

function active(e: Entry): boolean {
  return e.listeners.size + e.changeListeners.size > 0;
}

function setSnapshot(e: Entry, next: SessionReposSnapshot): void {
  e.snapshot = next;
  for (const listener of e.listeners) listener();
}

function clearRetry(e: Entry): void {
  if (e.retryTimer) clearTimeout(e.retryTimer);
  e.retryTimer = undefined;
}

function withTimeout<T>(p: Promise<T>): Promise<T> {
  let timeout: ReturnType<typeof setTimeout> | undefined;
  const timedOut = new Promise<never>((_, reject) => {
    timeout = setTimeout(
      () => reject(new Error("GET /sessions/{id}/repos timed out")),
      REPO_STATE_FETCH_TIMEOUT_MS,
    );
  });
  return Promise.race([p, timedOut]).finally(() => clearTimeout(timeout));
}

async function fetchEntry(e: Entry): Promise<void> {
  const gen = e.generation;
  clearRetry(e);
  try {
    const answer: unknown = await withTimeout(e.client.getSessionRepos(e.sessionId));
    // Only well-formed entries: a Router without this route, or a stand-in
    // answering every URL alike, mustn't take down a reader.
    const repos = Array.isArray(answer)
      ? (answer as SessionRepoState[]).filter((r) => typeof r?.root === "string" && typeof r.worktree?.git_dir === "string")
      : [];
    if (gen !== e.generation || entries.get(e.sessionId) !== e) return;
    e.retryAttempt = 0;
    const json = JSON.stringify(repos);
    if (json === e.lastJson && e.snapshot.repos !== null && e.snapshot.error === null) return;
    const same = json === e.lastJson && e.snapshot.repos !== null;
    e.lastJson = json;
    setSnapshot(e, { repos: same ? e.snapshot.repos : repos, error: null });
  } catch (err) {
    if (gen !== e.generation || entries.get(e.sessionId) !== e) return;
    const error = err instanceof Error ? err.message : String(err);
    // An event-driven refetch alone would leave a failed read standing
    // until the repo next changed.
    const delay = Math.max(
      RETRY_FLOOR_MS,
      Math.random() * Math.min(RETRY_MAX_MS, RETRY_BASE_MS * 2 ** e.retryAttempt),
    );
    e.retryAttempt++;
    e.retryTimer = setTimeout(() => {
      e.retryTimer = undefined;
      e.coalescer.now();
    }, delay);
    if (error !== e.snapshot.error) setSnapshot(e, { ...e.snapshot, error });
  }
}

/** Does one of these repos live in that git dir? A linked worktree shares its main checkout's. */
function inRepos(repos: SessionRepoState[] | null, gitDir: string): boolean {
  return (repos ?? []).some((r) => r.worktree.git_dir === gitDir || r.base_worktree?.git_dir === gitDir);
}

function onBusEvent(env: Envelope): void {
  if (env.type === "repo.changed") {
    const gitDir = (env.payload as RepoChangedPayload | undefined)?.git_dir;
    if (!gitDir) return;
    for (const e of entries.values()) {
      if (!active(e) || !inRepos(e.snapshot.repos, gitDir)) continue;
      e.coalescer.request();
      for (const listener of e.changeListeners) listener();
    }
    return;
  }
  const e = env.session_id ? entries.get(env.session_id) : undefined;
  if (e && active(e)) e.coalescer.request();
}

function syncBus(): void {
  const any = [...entries.values()].some(active);
  if (any && !unsubscribeBus) unsubscribeBus = subscribeToEvents({ types: BUS_TYPES }, onBusEvent);
  if (!any && unsubscribeBus) {
    unsubscribeBus();
    unsubscribeBus = null;
  }
}

/**
 * The entry's load, once the bus is ready (router/events.ts eventsReady):
 * the bus never replays, so a read served before it fixes its cursor could
 * miss a repo.changed landing in between. Dropped if the entry stopped (or
 * was replaced) meanwhile.
 */
function loadWhenReady(e: Entry): void {
  const gen = e.generation;
  void eventsReady().then(() => {
    if (gen === e.generation && entries.get(e.sessionId) === e && active(e)) e.coalescer.now();
  });
}

function stopEntry(e: Entry): void {
  e.generation++;
  e.coalescer.cancel();
  clearRetry(e);
}

/**
 * The session's entry for this client. Another client for the same session
 * (a test's fresh fixture router) starts over: its answers aren't the old one's.
 */
function entryFor(client: RouterClient, sessionId: string): Entry {
  const existing = entries.get(sessionId);
  if (existing && existing.client === client) return existing;
  const e: Entry = {
    sessionId,
    client,
    snapshot: UNKNOWN,
    listeners: existing?.listeners ?? new Set(),
    changeListeners: existing?.changeListeners ?? new Set(),
    coalescer: coalesce(() => fetchEntry(e)),
    generation: 0,
    retryAttempt: 0,
    lastJson: "",
  };
  if (existing) {
    stopEntry(existing);
    // Readers already attached see the new client's state from now on.
    for (const listener of e.listeners) listener();
  }
  entries.set(sessionId, e);
  if (existing && active(e)) loadWhenReady(e);
  return e;
}

function attach(e: Entry, set: Set<() => void>, listener: () => void): () => void {
  const wasActive = active(e);
  set.add(listener);
  if (!wasActive) {
    // First reader: load, or revalidate the snapshot kept from before —
    // subscribed first, the read behind the bus's ready barrier.
    syncBus();
    loadWhenReady(e);
  }
  return () => {
    if (!set.delete(listener)) return;
    if (!active(e)) {
      stopEntry(e);
      syncBus();
    }
  };
}

export function subscribeSessionRepos(client: RouterClient, sessionId: string, listener: () => void): () => void {
  const e = entryFor(client, sessionId);
  return attach(e, e.listeners, listener);
}

/** Calls `onChange` whenever a repo.changed names one of the session's repos (once its repos are known). */
export function subscribeRepoChanges(client: RouterClient, sessionId: string, onChange: () => void): () => void {
  const e = entryFor(client, sessionId);
  return attach(e, e.changeListeners, onChange);
}

export function getSessionReposSnapshot(sessionId: string): SessionReposSnapshot {
  return entries.get(sessionId)?.snapshot ?? UNKNOWN;
}

/** Fetch now, coalesced with anything in flight — after the UI itself moved a branch (a push). */
export function refreshSessionRepos(sessionId: string): void {
  const e = entries.get(sessionId);
  if (!e || !active(e)) return;
  e.retryAttempt = 0;
  e.coalescer.now();
}

/**
 * The session's repos with their branch facts (COMMIT-LAND-PUSH D10), loaded
 * on first use and kept current by repo.changed. `repos` is null until the
 * first answer: callers treat what they'd read from it as unknown.
 */
export function useSessionRepos(client: RouterClient, sessionId: string): SessionReposSnapshot {
  const subscribe = useCallback(
    (listener: () => void) => subscribeSessionRepos(client, sessionId, listener),
    [client, sessionId],
  );
  const get = useCallback(() => getSessionReposSnapshot(sessionId), [sessionId]);
  return useSyncExternalStore(subscribe, get, get);
}

/** Runs `onChange` when one of the session's repos changed (a commit, fetch, push or merge, by anyone). */
export function useRepoChanged(client: RouterClient, sessionId: string, onChange: () => void): void {
  const latest = useRef(onChange);
  latest.current = onChange;
  useEffect(
    () => subscribeRepoChanges(client, sessionId, () => latest.current()),
    [client, sessionId],
  );
}

export function resetRepoStateStoreForTests(): void {
  for (const e of entries.values()) stopEntry(e);
  entries.clear();
  unsubscribeBus?.();
  unsubscribeBus = null;
}
