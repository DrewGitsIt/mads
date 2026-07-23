import { useEffect, useState } from "react";
import type { RouterClient } from "../router/client";
import { subscribeToEvents } from "../router/events";
import type { Proposal } from "../router/types";

const PROPOSAL_STORE_EVENT_TYPES = [
  "synthesis.proposal_created",
  "synthesis.proposal_approved",
  "synthesis.proposal_dismissed",
];

/**
 * Fetches GET /proposals then subscribes to the shared event bus, refetching
 * the whole session's proposal list on any synthesis.proposal_* envelope —
 * proposal state always comes from the fetched record (spec §4's audit
 * trail), never reconstructed from the sparse envelope payload, so a card's
 * state survives a remount/refetch by construction.
 */
export function useProposalStore(routerClient: RouterClient, sessionId: string): Proposal[] {
  const [proposals, setProposals] = useState<Proposal[]>([]);

  useEffect(() => {
    let cancelled = false;
    routerClient.listProposals(sessionId).then((p) => {
      if (!cancelled) setProposals(p);
    });

    const unsubscribe = subscribeToEvents(
      { sessionId, types: PROPOSAL_STORE_EVENT_TYPES },
      () => {
        routerClient.listProposals(sessionId).then((p) => {
          if (!cancelled) setProposals(p);
        });
      },
    );

    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [routerClient, sessionId]);

  return proposals;
}
