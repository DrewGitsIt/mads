import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { eventsReady, subscribeToEvents } from "../../../router/events";
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
    let live = true;
    const unsubscribe = subscribeToEvents({ types: RETRO_LIST_EVENT_TYPES }, () => {
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

  return { retros, loading, error };
}
