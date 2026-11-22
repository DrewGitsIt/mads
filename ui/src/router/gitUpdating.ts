import { useCallback, useSyncExternalStore } from "react";
import type { AgentState, Envelope, GitStatePart, GitStateUpdatedPayload } from "./types";

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
 * was marked, so it can never stick.
 *
 * The stores that read git state (repoStateStore, files/useChangeSet) feed
 * this from their own subscriptions and reads; nothing here subscribes.
 */

export const GIT_UPDATING_FAILSAFE_MS = 30_000;

const ALL_PARTS: GitStatePart[] = ["repos", "changeset", "counts"];

interface Mark {
  /** The newest generation this window had seen for the session when it marked. */
  generation: number;
  timer: ReturnType<typeof setTimeout>;
}

interface SessionMarks {
  generation: number;
  marks: Map<GitStatePart, Mark>;
  /** Whether the session's Router serves cached git state; unknown until a read says. */
  served?: boolean;
  snapshot: string;
  listeners: Set<() => void>;
}

const sessions = new Map<string, SessionMarks>();

function stateFor(sessionId: string): SessionMarks {
  let s = sessions.get(sessionId);
  if (!s) {
    s = { generation: 0, marks: new Map(), snapshot: "", listeners: new Set() };
    sessions.set(sessionId, s);
  }
  return s;
}

function changed(s: SessionMarks): void {
  const next = ALL_PARTS.filter((p) => s.marks.has(p)).join(" ");
  if (next === s.snapshot) return;
  s.snapshot = next;
  for (const listener of s.listeners) listener();
}

function clear(s: SessionMarks, part: GitStatePart): void {
  const m = s.marks.get(part);
  if (!m) return;
  clearTimeout(m.timer);
  s.marks.delete(part);
}

function mark(sessionId: string, parts: GitStatePart[]): void {
  const s = stateFor(sessionId);
  for (const part of parts) {
    clear(s, part);
    const timer = setTimeout(() => {
      if (s.marks.get(part)?.timer !== timer) return;
      s.marks.delete(part);
      changed(s);
    }, GIT_UPDATING_FAILSAFE_MS);
    s.marks.set(part, { generation: s.generation, timer });
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

/** A GET of `part` answered: whether the Router serves git state, and whether that part is updating. */
export function noteGitStateRead(sessionId: string, part: GitStatePart, served: boolean, updating: boolean): void {
  const s = stateFor(sessionId);
  s.served = served;
  if (!served) return;
  if (updating) {
    mark(sessionId, [part]);
    return;
  }
  if (!s.marks.has(part)) return;
  clear(s, part);
  changed(s);
}

/** session.git_state_updated: every part it covers that was marked before this generation is done. */
export function noteGitStateEvent(payload: GitStateUpdatedPayload | undefined): void {
  if (!payload?.session_id || !Array.isArray(payload.parts)) return;
  const s = stateFor(payload.session_id);
  const generation = typeof payload.generation === "number" ? payload.generation : 0;
  let moved = false;
  for (const part of payload.parts) {
    const m = s.marks.get(part);
    if (m && generation > m.generation) {
      clear(s, part);
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
      return ["repos", "counts"];
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
  if (parts.length > 0) mark(sessionId, parts);
  return true;
}

/** After a POST /worktree/refresh: its counts and repos are recomputing. */
export function markRefreshed(sessionId: string): void {
  if (gitStateServed(sessionId) === true) mark(sessionId, ["counts", "repos"]);
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
