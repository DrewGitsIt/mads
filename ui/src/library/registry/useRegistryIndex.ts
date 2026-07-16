import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../router/client";
import type { RegistryEntry } from "../../router/types";

export interface UseRegistryIndex {
  entries: RegistryEntry[];
  loading: boolean;
  error: string | null;
  query: string;
  setQuery: (q: string) => void;
  refetch: () => void;
}

/**
 * Owns the Browse Registry list. No live subscription — there is no
 * `registry.*` SSE domain; the index only changes via this shelf's own
 * publish action, which calls refetch on completion.
 */
export function useRegistryIndex(): UseRegistryIndex {
  const [entries, setEntries] = useState<RegistryEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const mounted = useRef(true);

  const refetch = useCallback(() => {
    setLoading(true);
    void (async () => {
      try {
        const list = await router.registryIndex(query || undefined);
        if (mounted.current) setEntries(list);
      } catch (err) {
        if (mounted.current) setError(err instanceof Error ? err.message : String(err));
      } finally {
        if (mounted.current) setLoading(false);
      }
    })();
  }, [query]);

  useEffect(() => {
    mounted.current = true;
    refetch();
    return () => {
      mounted.current = false;
    };
  }, [refetch]);

  return { entries, loading, error, query, setQuery, refetch };
}
