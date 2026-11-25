import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";
import { isAbortError, type RouterClient } from "./client";
import { coalesce, type Coalescer } from "./coalesce";
import { eventsReady, subscribeToEvents } from "./events";
import { markFromTrigger, noteGitStateEvent, noteGitStateRead, onGitUpdatingFailsafe } from "./gitUpdating";
import type { Envelope, GitStateUpdatedPayload, RepoChangedPayload, SessionRepoState } from "./types";

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
 * The Router recomputes on these (docs/GIT-STATE-DESIGN.md), so they only
 * mark the repos "updating" (router/gitUpdating) and the read waits for
 * session.git_state_updated naming repos changed; against a Router that
 * computes per request (an older mesh peer) they refetch, as before.
 */
export const REPO_STATE_SESSION_EVENT_TYPES = ["session.updated", "commit.created", "commit.undone", "merge.completed"];
const SESSION_BUS_TYPES = ["session.git_state_updated", ...REPO_STATE_SESSION_EVENT_TYPES];
/** repo.changed has no session: one subscription on this device's bus matches it to sessions by git dir. */
const BUS_TYPES = ["repo.changed"];
/** After a cold answer (never computed), read again this long after, announced or not. */
export const REPO_STATE_COLD_RETRY_MS = 10_000;

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
  /** Aborts the fetch in flight: when the last reader leaves, or its timeout fires before a retry. */
  inFlight?: AbortController;
  lastJson: string;
  /** The session's own subscription (its bus: a remote session's host), while read. */
  unsubscribe?: () => void;
  coldRetry?: ReturnType<typeof setTimeout>;
}

// Branch facts several sessions share (docs/COMMIT-LAND-PUSH-DESIGN.md D10),
// read once per session for the whole window, however many surfaces show
// them (dock, path, Changes chip, push dialog). Like sessionsStore: events
// only ask a session's coalescer, which runs at most one fetch at a time,
// and a snapshot outlives its last reader so a remount shows it at once and
// revalidates behind it. Each read session has its own subscription
// (session.git_state_updated and its own triggers, on its host's bus); one
// more for every session takes repo.changed, which has no session, matched
// to sessions by their repos' git dirs.
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

/**
 * The read, aborted after REPO_STATE_FETCH_TIMEOUT_MS: the abort cancels the
 * request itself (and, server side, its git work), so the retry that
 * follows never puts a second request beside a first still running.
 */
function withTimeout<T>(run: (signal: AbortSignal) => Promise<T>, ctl: AbortController): Promise<T> {
  let timeout: ReturnType<typeof setTimeout> | undefined;
  const timedOut = new Promise<never>((_, reject) => {
    timeout = setTimeout(() => {
      // Rejected first, so the race settles as a timeout (retried), not as the abort (silent).
      reject(new Error("GET /sessions/{id}/repos timed out"));
      ctl.abort();
    }, REPO_STATE_FETCH_TIMEOUT_MS);
  });
  return Promise.race([run(ctl.signal), timedOut]).finally(() => clearTimeout(timeout));
}

async function fetchEntry(e: Entry): Promise<void> {
  const gen = e.generation;
  clearRetry(e);
  e.inFlight?.abort();
  const ctl = new AbortController();
  e.inFlight = ctl;
  try {
    const state = await withTimeout((signal) => e.client.getSessionReposState(e.sessionId, { signal }), ctl);
    const answer: unknown = state?.repos;
    // Only well-formed entries: a Router without this route, or a stand-in
    // answering every URL alike, mustn't take down a reader.
    const repos = Array.isArray(answer)
      ? (answer as SessionRepoState[]).filter((r) => typeof r?.root === "string" && typeof r.worktree?.git_dir === "string")
      : [];
    if (gen !== e.generation || entries.get(e.sessionId) !== e) return;
    e.retryAttempt = 0;
    const served = state?.gitState === true;
    noteGitStateRead(e.sessionId, "repos", served, served && state.updating);
    // Never computed yet: [] with X-Mads-Updating: 1 is "loading", not "no
    // repos" (Push isn't offered until a computed answer). The compute's
    // session.git_state_updated brings them; one more read a while later
    // makes sure a missed announcement can't leave them unknown.
    if (served && state.updating && !state.computedAt && repos.length === 0) {
      if (e.snapshot.error !== null) setSnapshot(e, { ...e.snapshot, error: null });
      if (!e.coldRetry) {
        e.coldRetry = setTimeout(() => {
          e.coldRetry = undefined;
          if (gen === e.generation && active(e)) e.coalescer.now();
        }, REPO_STATE_COLD_RETRY_MS);
      }
      return;
    }
    const json = JSON.stringify(repos);
    if (json === e.lastJson && e.snapshot.repos !== null && e.snapshot.error === null) return;
    const same = json === e.lastJson && e.snapshot.repos !== null;
    e.lastJson = json;
    setSnapshot(e, { repos: same ? e.snapshot.repos : repos, error: null });
  } catch (err) {
    if (gen !== e.generation || entries.get(e.sessionId) !== e) return;
    // Aborted by stopEntry, or by a newer fetch: whoever aborted owns what's next.
    if (isAbortError(err)) return;
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
  } finally {
    if (e.inFlight === ctl) e.inFlight = undefined;
  }
}

/** Does one of these repos live in that git dir? A linked worktree shares its main checkout's. */
function inRepos(repos: SessionRepoState[] | null, gitDir: string): boolean {
  return (repos ?? []).some((r) => r.worktree.git_dir === gitDir || r.base_worktree?.git_dir === gitDir);
}

function onBusEvent(env: Envelope): void {
  if (env.type !== "repo.changed") return;
  const gitDir = (env.payload as RepoChangedPayload | undefined)?.git_dir;
  if (!gitDir) return;
  for (const e of entries.values()) {
    if (!active(e) || !inRepos(e.snapshot.repos, gitDir)) continue;
    if (!markFromTrigger(e.sessionId, env)) e.coalescer.request();
    for (const listener of e.changeListeners) listener();
  }
}

function onSessionEvent(e: Entry, env: Envelope): void {
  if (!active(e)) return;
  if (env.type === "session.git_state_updated") {
    const p = env.payload as GitStateUpdatedPayload | undefined;
    noteGitStateEvent(p);
    if (Array.isArray(p?.changed) && p.changed.includes("repos")) e.coalescer.now();
    return;
  }
  // session.updated marks nothing (it isn't a trigger: the Router's own
  // counts write sends it); an older Router is asked again on it.
  if (!markFromTrigger(e.sessionId, env)) e.coalescer.request();
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
 * The entry's session subscription, then its load once the session's bus
 * is ready (router/events.ts eventsReady): the bus never replays, so a read
 * served before it fixes its cursor could miss a session.git_state_updated
 * landing in between. The session's own bus: a remote session's events
 * come from its host. Dropped if the entry stopped (or was replaced)
 * meanwhile.
 */
function startEntry(e: Entry): void {
  const gen = e.generation;
  e.unsubscribe?.();
  const offEvents = subscribeToEvents({ sessionId: e.sessionId, types: SESSION_BUS_TYPES }, (env) => onSessionEvent(e, env));
  // A lost announcement: the fail-safe re-reads (and a cold or stale part
  // revalidates on the Router).
  const offFailsafe = onGitUpdatingFailsafe(e.sessionId, "repos", () => e.coalescer.now());
  e.unsubscribe = () => {
    offEvents();
    offFailsafe();
  };
  void eventsReady(e.sessionId).then(() => {
    if (gen === e.generation && entries.get(e.sessionId) === e && active(e)) e.coalescer.now();
  });
}

function stopEntry(e: Entry): void {
  e.generation++;
  e.coalescer.cancel();
  clearRetry(e);
  e.unsubscribe?.();
  e.unsubscribe = undefined;
  if (e.coldRetry) clearTimeout(e.coldRetry);
  e.coldRetry = undefined;
  // Nobody reads it now: let the Router stop its git work.
  e.inFlight?.abort();
  e.inFlight = undefined;
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
  if (existing && active(e)) startEntry(e);
  return e;
}

function attach(e: Entry, set: Set<() => void>, listener: () => void): () => void {
  const wasActive = active(e);
  set.add(listener);
  if (!wasActive) {
    // First reader: load, or revalidate the snapshot kept from before —
    // subscribed first, the read behind the bus's ready barrier.
    syncBus();
    startEntry(e);
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
 * on first use and kept current by session.git_state_updated (the Router
 * recomputes them on repo.changed and the session's commits,
 * docs/GIT-STATE-DESIGN.md). `repos` is null until the first computed
 * answer: callers treat what they'd read from it as unknown.
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
