import { useEffect, useState } from "react";
import type { RouterClient } from "./client";
import { useHostClient, useHostScope } from "./hostScope";
import { hostKey } from "./mesh";
import type { ProviderInfo } from "./types";

/**
 * Every registered provider's capability declaration (GET /providers), for
 * callers that need to know what a session's provider can do — the
 * session dock's trust picker marks Auto-review "(runs as Limited on
 * …)" from it. Like useProviderModels, simultaneous mounts share one request
 * and nothing is cached past it: the declarations are static per Router
 * process, and a refetch on mount is one cheap read. A failed fetch yields
 * an empty list — callers treat "unknown" as "no annotation", never as a
 * downgrade.
 */

// Per host ("" for this device): a remote session's capability table is its
// host's, not this one's (docs/MESH-MODE-DESIGN.md D3b).
const inFlight = new Map<string, Promise<ProviderInfo[]>>();

/** Test seam: drops any in-flight request so each case starts cold. */
export function resetProviderInfoCache(): void {
  inFlight.clear();
}

function loadProviderInfos(client: RouterClient, key: string): Promise<ProviderInfo[]> {
  const pending = inFlight.get(key);
  if (pending) return pending;
  const p = client.listProviders().finally(() => {
    if (inFlight.get(key) === p) inFlight.delete(key);
  });
  inFlight.set(key, p);
  return p;
}

export function useProviderInfos(): ProviderInfo[] {
  const [infos, setInfos] = useState<ProviderInfo[]>([]);
  const client = useHostClient();
  const key = hostKey(useHostScope());

  useEffect(() => {
    let cancelled = false;
    loadProviderInfos(client, key)
      .then((next) => {
        if (!cancelled && Array.isArray(next)) setInfos(next);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [client, key]);

  return infos;
}
