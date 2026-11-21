import { useCallback, useEffect, useRef, useState } from "react";
import type { RouterClient } from "../router/client";
import { eventsReady, subscribeToEvents } from "../router/events";
import { useRepoChanged } from "../router/repoStateStore";
import type { ChangeBaselineId, ChangeSet } from "../router/types";

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

/** Events after which a session's change set may differ. */
export const CHANGESET_REFRESH_EVENT_TYPES = [
  "file.changed",
  "commit.created",
  "commit.undone",
  "session.agent_state",
];

/** Bursts of file writes collapse into one refetch. */
export const CHANGESET_REFRESH_DEBOUNCE_MS = 1500;

export interface ChangeSetState {
  changeSet: ChangeSet | null;
  /** True until the first answer for the current baseline arrives. */
  loading: boolean;
  error?: string;
  refetch: () => void;
}

/**
 * The session's ChangeSet against one baseline, refetched (debounced) when
 * the session writes a file, commits, undoes a commit or changes agent
 * state, and when one of its repos' branches moved (repo.changed,
 * COMMIT-LAND-PUSH D10) — another session's commit takes files out of
 * "uncommitted" here too. A refetch keeps the previous set on screen until the new one
 * lands, so selection and scroll don't jump.
 */
export function useChangeSet(routerClient: RouterClient, sessionId: string, vs: ChangeBaselineId = "start"): ChangeSetState {
  const [changeSet, setChangeSet] = useState<ChangeSet | null>(null);
  const [loadedFor, setLoadedFor] = useState<string | null>(null);
  const [error, setError] = useState<string>();
  const seq = useRef(0);
  const target = `${sessionId}|${vs}`;

  const fetchNow = useCallback(() => {
    const mine = ++seq.current;
    routerClient
      .getChangeSet(sessionId, vs)
      .then((cs) => {
        if (mine !== seq.current) return;
        setChangeSet(normalizeChangeSet(cs));
        setError(undefined);
        setLoadedFor(`${sessionId}|${vs}`);
      })
      .catch((err: unknown) => {
        if (mine !== seq.current) return;
        setError(err instanceof Error ? err.message : String(err));
        setLoadedFor(`${sessionId}|${vs}`);
      });
  }, [routerClient, sessionId, vs]);

  // The mount read waits for the bus's ready barrier (router/events.ts
  // eventsReady): the bus never replays, so a read served before it fixes
  // its cursor could miss an event landing in between. The subscription is
  // a later effect of the same commit, registered before the barrier lets
  // this read start.
  useEffect(() => {
    let cancelled = false;
    void eventsReady(sessionId).then(() => {
      if (!cancelled) fetchNow();
    });
    return () => {
      cancelled = true;
    };
  }, [fetchNow, sessionId]);

  // A new session must not show the previous session's files.
  useEffect(() => {
    setChangeSet(null);
    setError(undefined);
  }, [sessionId]);

  // One debounce for every reason to refetch.
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const schedule = useCallback(() => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(fetchNow, CHANGESET_REFRESH_DEBOUNCE_MS);
  }, [fetchNow]);

  useEffect(() => {
    const unsubscribe = subscribeToEvents({ sessionId, types: CHANGESET_REFRESH_EVENT_TYPES }, schedule);
    return () => {
      if (timer.current) clearTimeout(timer.current);
      timer.current = undefined;
      unsubscribe();
    };
  }, [sessionId, schedule]);

  useRepoChanged(routerClient, sessionId, schedule);

  return { changeSet, loading: loadedFor !== target, error, refetch: fetchNow };
}
