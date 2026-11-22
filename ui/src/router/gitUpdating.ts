import { useCallback, useSyncExternalStore } from "react";
import type { AgentState, ChangeBaselineId, Envelope, GitStatePart, GitStateUpdatedPayload } from "./types";

/**
 * Which of a session's git-derived parts the Router is recomputing, as this
 * window knows it (docs/GIT-STATE-DESIGN.md, "The updating indicator"):
 * what the workspace's `git-updating` indicator shows.
 *
 * A part is marked when a GET says it is updating, or when the window sees
 * an event that sets a recompute off (markFromTrigger); it is cleared when
 * session.git_state_updated covers it at a newer generation than it was
 * marked at, changed or not (counts clear only this way: they have no GET),
 * when a GET says it is not updating, or GIT_UPDATING_FAILSAFE_MS after it
 * was marked. That fail-safe re-reads the part (onGitUpdatingFailsafe: the
 * store showing it GETs it again — cached and cheap, and a cold or stale
 * part revalidates on the Router), so a lost announcement can't leave old
 * data on screen looking fresh; the re-read marks it again if the Router
 * still says updating.
 *
 * The change set is marked per baseline: an announcement naming another
 * baseline (its `baseline`) clears only that one, never session start's.
 * The snapshot names "changeset" while any baseline is marked.
 *
 * The stores that read git state (repoStateStore, files/useChangeSet) feed
 * this from their own subscriptions and reads; nothing here subscribes.
 */

export const GIT_UPDATING_FAILSAFE_MS = 30_000;

const ALL_PARTS: GitStatePart[] = ["repos", "changeset", "counts"];

/** A mark's key: the part, or "changeset:<baseline>" for a baseline other than session start. */
type MarkKey = string;

function keyOf(part: GitStatePart, baseline?: ChangeBaselineId | string): MarkKey {
  return part === "changeset" && baseline && baseline !== "start" ? `changeset:${baseline}` : part;
}

function partOf(key: MarkKey): GitStatePart {
  return key.startsWith("changeset") ? "changeset" : (key as GitStatePart);
}

interface Mark {
  /** The newest generation this window had seen for the session when it marked. */
  generation: number;
  timer: ReturnType<typeof setTimeout>;
}

interface SessionMarks {
  generation: number;
  marks: Map<MarkKey, Mark>;
  /** Whether the session's Router serves cached git state; unknown until a read says. */
  served?: boolean;
  snapshot: string;
  listeners: Set<() => void>;
  /** Who re-reads a mark's part when its fail-safe fires. */
  rereaders: Map<MarkKey, Set<() => void>>;
}

const sessions = new Map<string, SessionMarks>();

function stateFor(sessionId: string): SessionMarks {
  let s = sessions.get(sessionId);
  if (!s) {
    s = { generation: 0, marks: new Map(), snapshot: "", listeners: new Set(), rereaders: new Map() };
    sessions.set(sessionId, s);
  }
  return s;
}

function changed(s: SessionMarks): void {
  const marked = new Set([...s.marks.keys()].map(partOf));
  const next = ALL_PARTS.filter((p) => marked.has(p)).join(" ");
  if (next === s.snapshot) return;
  s.snapshot = next;
  for (const listener of s.listeners) listener();
}

function clear(s: SessionMarks, key: MarkKey): void {
  const m = s.marks.get(key);
  if (!m) return;
  clearTimeout(m.timer);
  s.marks.delete(key);
}

function markKeys(sessionId: string, keys: MarkKey[]): void {
  const s = stateFor(sessionId);
  for (const key of keys) {
    clear(s, key);
    const timer = setTimeout(() => {
      if (s.marks.get(key)?.timer !== timer) return;
      s.marks.delete(key);
      changed(s);
      // Re-read, so what's on screen is what the Router has now.
      for (const reread of [...(s.rereaders.get(key) ?? [])]) reread();
    }, GIT_UPDATING_FAILSAFE_MS);
    s.marks.set(key, { generation: s.generation, timer });
  }
  changed(s);
}

/**
 * Whether the session's Router serves cached git state (it sent `updating`
 * or X-Mads-Updating). Undefined until a read said; false for an older
 * mesh peer that computes per request — its readers refetch on the
 * triggers themselves, as before, and nothing is marked for it.
 */
export function gitStateServed(sessionId: string): boolean | undefined {
  return sessions.get(sessionId)?.served;
}

/**
 * A GET of `part` answered (for the change set, against `baseline`):
 * whether the Router serves git state, and whether that part is updating.
 */
export function noteGitStateRead(
  sessionId: string,
  part: GitStatePart,
  served: boolean,
  updating: boolean,
  baseline?: ChangeBaselineId,
): void {
  const s = stateFor(sessionId);
  s.served = served;
  if (!served) return;
  const key = keyOf(part, baseline);
  if (updating) {
    markKeys(sessionId, [key]);
    return;
  }
  if (!s.marks.has(key)) return;
  clear(s, key);
  changed(s);
}

/**
 * session.git_state_updated: every part it covers that was marked before
 * this generation is done. One naming a baseline covers only that
 * baseline's change set.
 */
export function noteGitStateEvent(payload: GitStateUpdatedPayload | undefined): void {
  if (!payload?.session_id || !Array.isArray(payload.parts)) return;
  const s = stateFor(payload.session_id);
  const generation = typeof payload.generation === "number" ? payload.generation : 0;
  let moved = false;
  for (const part of payload.parts) {
    const key = keyOf(part, payload.baseline);
    const m = s.marks.get(key);
    if (m && generation > m.generation) {
      clear(s, key);
      moved = true;
    }
  }
  if (generation > s.generation) s.generation = generation;
  if (moved) changed(s);
}

/** The parts an event the window saw sets a recompute off for, per the design's triggers; none for anything else. */
export function triggerParts(env: Envelope): GitStatePart[] {
  switch (env.type) {
    case "file.changed":
      return ["changeset", "counts"];
    case "session.agent_state":
      return (env.payload as AgentState | undefined)?.state === "idle" ? ["changeset", "counts"] : [];
    case "commit.created":
    case "commit.undone":
    case "merge.completed":
      return ALL_PARTS;
    case "repo.changed":
      // Marked only for sessions this window shows (repoStateStore asks
      // for its read ones): the Router recomputes a shown session's change
      // set on it, and announces an unmoved background one's as done.
      return ALL_PARTS;
    default:
      return [];
  }
}

/**
 * Marks what `env` sets recomputing for the session, when its Router serves
 * git state. Returns whether it did: false means the caller should refetch
 * itself, as it did before the Router kept git state.
 */
export function markFromTrigger(sessionId: string, env: Envelope): boolean {
  if (gitStateServed(sessionId) !== true) return false;
  const parts = triggerParts(env);
  if (parts.length > 0) markKeys(sessionId, parts);
  return true;
}

/**
 * After a POST /worktree/refresh: its counts and repos are recomputing (a
 * bodiless refresh recomputes the change set only when it is old, so it
 * isn't marked: an announcement for it may never come).
 */
export function markRefreshed(sessionId: string): void {
  if (gitStateServed(sessionId) === true) markKeys(sessionId, ["counts", "repos"]);
}

/** After a revalidate POST that recomputes `part` regardless (Retry). */
export function markRevalidating(sessionId: string, part: GitStatePart, baseline?: ChangeBaselineId): void {
  if (gitStateServed(sessionId) === true) markKeys(sessionId, [keyOf(part, baseline)]);
}

/**
 * Runs `reread` when the fail-safe clears the session's mark on `part`
 * (for the change set, against `baseline`): the store showing it GETs it
 * again. Returns the unsubscribe.
 */
export function onGitUpdatingFailsafe(
  sessionId: string,
  part: GitStatePart,
  reread: () => void,
  baseline?: ChangeBaselineId,
): () => void {
  const s = stateFor(sessionId);
  const key = keyOf(part, baseline);
  let set = s.rereaders.get(key);
  if (!set) {
    set = new Set();
    s.rereaders.set(key, set);
  }
  set.add(reread);
  return () => {
    s.rereaders.get(key)?.delete(reread);
  };
}

export function subscribeGitUpdating(sessionId: string, listener: () => void): () => void {
  const s = stateFor(sessionId);
  s.listeners.add(listener);
  return () => {
    s.listeners.delete(listener);
  };
}

/** The marked parts, space-separated in a fixed order; "" when none. */
export function getGitUpdating(sessionId: string): string {
  return sessions.get(sessionId)?.snapshot ?? "";
}

/** The session's marked parts ("" when nothing is updating), live. */
export function useGitUpdating(sessionId: string): string {
  const subscribe = useCallback((listener: () => void) => subscribeGitUpdating(sessionId, listener), [sessionId]);
  const get = useCallback(() => getGitUpdating(sessionId), [sessionId]);
  return useSyncExternalStore(subscribe, get, get);
}

export function resetGitUpdatingForTests(): void {
  for (const s of sessions.values()) for (const m of s.marks.values()) clearTimeout(m.timer);
  sessions.clear();
}
