import { useEffect, useState } from "react";
import { router } from "./client";
import type { ProviderInfo } from "./types";

/**
 * Every registered provider's capability declaration (GET /providers), for
 * callers that need to know what a session's provider can do — the
 * conversation header's trust picker marks Auto-review "(runs as Limited on
 * …)" from it. Like useProviderModels, simultaneous mounts share one request
 * and nothing is cached past it: the declarations are static per Router
 * process, and a refetch on mount is one cheap read. A failed fetch yields
 * an empty list — callers treat "unknown" as "no annotation", never as a
 * downgrade.
 */

let inFlight: Promise<ProviderInfo[]> | null = null;

/** Test seam: drops any in-flight request so each case starts cold. */
export function resetProviderInfoCache(): void {
  inFlight = null;
}

function loadProviderInfos(): Promise<ProviderInfo[]> {
  if (inFlight) return inFlight;
  const p = router.listProviders().finally(() => {
    if (inFlight === p) inFlight = null;
  });
  inFlight = p;
  return p;
}

export function useProviderInfos(): ProviderInfo[] {
  const [infos, setInfos] = useState<ProviderInfo[]>([]);

  useEffect(() => {
    let cancelled = false;
    loadProviderInfos()
      .then((next) => {
        if (!cancelled && Array.isArray(next)) setInfos(next);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  return infos;
}
