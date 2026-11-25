import { useEffect, useState, useSyncExternalStore } from "react";
import { FLAGS, subscribeFlags } from "../../../featureFlags";
import { RouterHTTPError, type RouterClient } from "../../../router/client";
import { eventsReady, subscribeToEvents } from "../../../router/events";
import type { Link } from "../../../router/types";
import { RUN_EVENT_TYPES } from "../../../features/sessions/workspace/usePipelineRunning";

/** What the /new-* link commands need to know about this session's links. */
export interface LinkCommandState {
  /** feature_flags.linkedSessions — the link variants are hidden without it. */
  linkedSessions: boolean;
  /** The session holding orchestrate over this one, if any — "already led by <name>". */
  leader?: { id: string; name: string };
  /** A pipeline run is live here — pipeline runs can be monitored, not led. */
  pipelineRunning: boolean;
}

/**
 * The links that lead `sessionId` — those whose subject it is and that grant
 * orchestrate — newest first. The server allows one live holder per subject,
 * but a deleted leader's link stays behind (deletion leaves links dangling),
 * so a re-led session has more than one; the live one is almost always the newest.
 */
export function leaderLinks(links: Link[], sessionId: string): Link[] {
  return links
    .filter((l) => l.subject_session_id === sessionId && l.grants.includes("orchestrate"))
    .sort((a, b) => (a.created_at < b.created_at ? 1 : a.created_at > b.created_at ? -1 : 0));
}

/** The link that most likely leads `sessionId` (leaderLinks' newest), without checking its holder still exists. */
export function leaderLink(links: Link[], sessionId: string): Link | undefined {
  return leaderLinks(links, sessionId)[0];
}

/**
 * Feeds the composer's CommandContext for /new-leader's availability. The
 * links come from the caller (ConversationTab's useSessionLinks, which
 * already refetches on link.* events); this resolves the leader's name and
 * tracks whether a pipeline run is live — GET /metrics/pipelines once, then
 * the pipeline.* events. Both are fetched only while the flag is on. The
 * server stays the authority: the /new card's dry-run validation refuses
 * whatever this misses.
 */
export function useLinkCommandState(
  routerClient: RouterClient,
  sessionId: string,
  links: Link[] | undefined,
): LinkCommandState {
  const linkedSessions = useSyncExternalStore(subscribeFlags, () => FLAGS.linkedSessions);
  // A string, so the effect re-runs only when the candidates change.
  const candidateIds = links ? leaderLinks(links, sessionId).map((l) => l.granting_session_id).join(" ") : "";
  const [leader, setLeader] = useState<{ id: string; name: string }>();
  const [pipelineRunning, setPipelineRunning] = useState(false);

  useEffect(() => {
    const ids = candidateIds ? candidateIds.split(" ") : [];
    if (!linkedSessions || ids.length === 0) {
      setLeader(undefined);
      return;
    }
    let cancelled = false;
    // Named by id until the record arrives (or if it can't be read). A 404
    // means that leader was deleted: try the next, and a session whose
    // leaders are all gone is unled (the server's holder check agrees).
    setLeader({ id: ids[0], name: ids[0] });
    void (async () => {
      for (const id of ids) {
        try {
          const s = await routerClient.getSession(id);
          if (!cancelled) setLeader({ id, name: s.name || id });
          return;
        } catch (err) {
          if (!(err instanceof RouterHTTPError && err.status === 404)) return;
          if (cancelled) return;
        }
      }
      if (!cancelled) setLeader(undefined);
    })();
    return () => {
      cancelled = true;
    };
  }, [routerClient, candidateIds, linkedSessions]);

  useEffect(() => {
    setPipelineRunning(false);
    if (!linkedSessions) return;
    let cancelled = false;
    let heard = false;
    // Subscribed first, the run history read behind the bus's ready barrier
    // (router/events.ts eventsReady); a run event heard first wins.
    const unsubscribe = subscribeToEvents({ sessionId, types: RUN_EVENT_TYPES }, (envelope) => {
      heard = true;
      setPipelineRunning(envelope.type === "pipeline.started");
    });
    eventsReady(sessionId)
      .then(() => (cancelled ? [] : routerClient.metricsPipelineRuns(sessionId)))
      .then((runs) => {
        if (!cancelled && !heard) setPipelineRunning(runs.some((r) => r.outcome === "running"));
      })
      .catch(() => {});
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [routerClient, sessionId, linkedSessions]);

  return { linkedSessions, leader, pipelineRunning };
}
