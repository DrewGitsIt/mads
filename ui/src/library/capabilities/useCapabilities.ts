import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import type { Capability } from "../../router/types";

const CAPABILITY_EVENT_TYPES = [
  "capability.created",
  "capability.updated",
  "capability.deleted",
  "capability.health_changed",
];

export interface UseCapabilities {
  capabilities: Capability[];
  loading: boolean;
  error: string | null;
  createCapability: (capability: Partial<Capability>) => Promise<Capability>;
  updateCapability: (capability: Capability) => Promise<Capability>;
  deleteCapability: (id: string) => Promise<void>;
}

/** Owns the shared capability list — fetched once, kept live via the capability.* SSE domain. */
export function useCapabilities(): UseCapabilities {
  const [capabilities, setCapabilities] = useState<Capability[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    try {
      const list = await router.listCapabilities();
      if (mounted.current) setCapabilities(list);
    } catch (err) {
      if (mounted.current) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void refetch();
    const unsubscribe = subscribeToEvents({ types: CAPABILITY_EVENT_TYPES }, () => {
      void refetch();
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [refetch]);

  const createCapability = useCallback(async (capability: Partial<Capability>) => {
    return router.createCapability(capability);
  }, []);

  const updateCapability = useCallback(async (capability: Capability) => {
    return router.updateCapability(capability);
  }, []);

  const deleteCapability = useCallback(async (id: string) => {
    await router.deleteCapability(id);
  }, []);

  return { capabilities, loading, error, createCapability, updateCapability, deleteCapability };
}
