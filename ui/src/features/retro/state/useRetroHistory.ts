import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import { RETRO_LIST_EVENT_TYPES } from "../events";
import type { Retro } from "../../../router/types";

export interface UseRetroHistory {
  retros: Retro[];
  loading: boolean;
  error: string | null;
}

/** Fetch-once + live retro history list, kept fresh via RETRO_LIST_EVENT_TYPES. */
export function useRetroHistory(): UseRetroHistory {
  const [retros, setRetros] = useState<Retro[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    try {
      const list = await router.listRetros();
      if (mounted.current) setRetros(list);
    } catch (err) {
      if (mounted.current) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void refetch();
    const unsubscribe = subscribeToEvents({ types: RETRO_LIST_EVENT_TYPES }, () => {
      void refetch();
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [refetch]);

  return { retros, loading, error };
}
