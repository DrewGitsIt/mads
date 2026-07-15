import { useEffect, useState } from "react";
import { router } from "./client";
import type { Model } from "./types";

/**
 * Fetches every registered provider's model list, grouped by provider name.
 * Extracted from ModelPill.tsx (W6.2) — both the session launchpad's model
 * picker and the Library's Agents shelf validated model dropdown (W6.8)
 * need the same GET /providers + GET /providers/{name}/models pair; this is
 * the one place that fetch logic lives.
 */
export function useProviderModels(): Record<string, Model[]> {
  const [modelsByProvider, setModelsByProvider] = useState<Record<string, Model[]>>({});

  useEffect(() => {
    let cancelled = false;
    router
      .listProviderNames()
      .then(async (providers) => {
        const entries = await Promise.all(
          providers.map(async (p) => [p, await router.listModels(p)] as const),
        );
        if (!cancelled) setModelsByProvider(Object.fromEntries(entries));
      })
      .catch(() => {
        if (!cancelled) setModelsByProvider({});
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return modelsByProvider;
}
