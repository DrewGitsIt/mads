import { useEffect, useState, useSyncExternalStore } from "react";
import { FLAGS, subscribeFlags } from "../../../featureFlags";
import type { RouterClient } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
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

/** The link that leads `sessionId`: one whose subject it is and that grants orchestrate (one per subject, server-enforced). */
export function leaderLink(links: Link[], sessionId: string): Link | undefined {
  return links.find(
    (l) => l.subject_session_id === sessionId && l.grants.includes("orchestrate"),
  );
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
  const leaderId = links ? leaderLink(links, sessionId)?.granting_session_id : undefined;
  const [leader, setLeader] = useState<{ id: string; name: string }>();
  const [pipelineRunning, setPipelineRunning] = useState(false);

  useEffect(() => {
    if (!linkedSessions || !leaderId) {
      setLeader(undefined);
      return;
    }
    let cancelled = false;
    // Named by id until the record arrives (or if it can't be read).
    setLeader({ id: leaderId, name: leaderId });
    routerClient
      .getSession(leaderId)
      .then((s) => {
        if (!cancelled) setLeader({ id: leaderId, name: s.name || leaderId });
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [routerClient, leaderId, linkedSessions]);

  useEffect(() => {
    setPipelineRunning(false);
    if (!linkedSessions) return;
    let cancelled = false;
    let heard = false;
    routerClient
      .metricsPipelineRuns(sessionId)
      .then((runs) => {
        if (!cancelled && !heard) setPipelineRunning(runs.some((r) => r.outcome === "running"));
      })
      .catch(() => {});
    const unsubscribe = subscribeToEvents({ sessionId, types: RUN_EVENT_TYPES }, (envelope) => {
      heard = true;
      setPipelineRunning(envelope.type === "pipeline.started");
    });
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [routerClient, sessionId, linkedSessions]);

  return { linkedSessions, leader, pipelineRunning };
}
