import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../router/client";
import { eventsReady, subscribeToEvents } from "../../router/events";
import type { Agent } from "../../router/types";

const AGENT_EVENT_TYPES = ["agent.created", "agent.updated", "agent.deleted"];

export interface UseAgents {
  agents: Agent[];
  loading: boolean;
  error: string | null;
  createAgent: (agent: Partial<Agent>) => Promise<Agent>;
  updateAgent: (agent: Agent) => Promise<Agent>;
  deleteAgent: (id: string) => Promise<void>;
}

/** Owns the shared agent list — fetched once, kept live via the agent.* SSE domain. */
export function useAgents(): UseAgents {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    try {
      const list = await router.listAgents();
      if (mounted.current) setAgents(list);
    } catch (err) {
      if (mounted.current) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    let live = true;
    const unsubscribe = subscribeToEvents({ types: AGENT_EVENT_TYPES }, () => {
      void refetch();
    });
    // The mount read waits for the bus's ready barrier (router/events.ts
    // eventsReady), subscribed first: the bus never replays, so a read served
    // before it fixes its cursor could miss an event landing in between.
    void eventsReady().then(() => {
      if (live) void refetch();
    });
    return () => {
      live = false;
      mounted.current = false;
      unsubscribe();
    };
  }, [refetch]);

  const createAgent = useCallback(async (agent: Partial<Agent>) => {
    return router.createAgent(agent);
  }, []);

  const updateAgent = useCallback(async (agent: Agent) => {
    return router.updateAgent(agent);
  }, []);

  const deleteAgent = useCallback(async (id: string) => {
    await router.deleteAgent(id);
  }, []);

  return { agents, loading, error, createAgent, updateAgent, deleteAgent };
}
