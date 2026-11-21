import { useEffect, useState } from "react";
import type { RouterClient } from "./client";
import { eventsReady, subscribeToEvents } from "./events";
import { agentIndex } from "./agentRef";
import type { AgentIndex } from "./agentRef";

const AGENT_EVENT_TYPES = ["agent.created", "agent.updated", "agent.deleted"];

/**
 * The persona list as an id-or-name index (router/agentRef.ts), kept live via
 * the agent.* SSE domain. Undefined until the first answer — a caller can
 * tell "not loaded yet" from "no such persona" and avoid flashing "Unknown
 * agent" at a reference that is about to resolve. A failed fetch settles to
 * an empty index: the reference then reads as unresolved, never as a hash.
 */
export function useAgentIndex(routerClient: RouterClient): AgentIndex | undefined {
  const [index, setIndex] = useState<AgentIndex>();

  useEffect(() => {
    let cancelled = false;
    function load() {
      routerClient.listAgents().then(
        (agents) => !cancelled && setIndex(agentIndex(agents)),
        () => !cancelled && setIndex((cur) => cur ?? agentIndex([])),
      );
    }
    const unsubscribe = subscribeToEvents({ types: AGENT_EVENT_TYPES }, load);
    // The mount read waits for the bus's ready barrier (router/events.ts
    // eventsReady), subscribed first: the bus never replays, so a read served
    // before it fixes its cursor could miss an event landing in between.
    void eventsReady().then(() => {
      if (!cancelled) load();
    });
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [routerClient]);

  return index;
}
