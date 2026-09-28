import { useEffect, useState } from "react";
import { router } from "./client";
import type { Model, ModelCatalog } from "./types";

/**
 * Every registered provider's model catalog, grouped by provider name.
 * Extracted from ModelPill.tsx (W6.2) — the session launchpad's model
 * picker, the conversation header, the Library's Agents shelf and the
 * template stage list all need the same GET /providers + GET
 * /providers/{name}/models pair; this is the one place that fetch logic
 * lives.
 *
 * Five components mount this, and simultaneous mounts share one fan-out
 * rather than each issuing its own. Results are deliberately NOT held
 * here: the Router caches discovery, so a repeat GET costs a memory read,
 * and a client-side result cache would only add a second staleness window
 * on top of the server's — one the refresh endpoint could not clear.
 */

// RETRY_MS backs the one bounded re-fetch for a provider still discovering
// at first paint. The Router warms its catalog at boot, so this is the
// narrow case of the UI opening before a slow harness answered — one retry,
// deliberately not a poll loop.
const RETRY_MS = 2_000;

type Catalogs = Record<string, ModelCatalog>;

let inFlight: Promise<Catalogs> | null = null;

/** Test seam: drops any in-flight request so each case starts cold. */
export function resetProviderModelCache(): void {
  inFlight = null;
}

async function fetchCatalogs(): Promise<Catalogs> {
  const providers = await router.listProviderNames();
  const entries = await Promise.all(
    providers.map(async (name) => {
      try {
        return [name, await router.listModelCatalog(name)] as const;
      } catch {
        // One provider's failure must not blank the whole picker — the
        // others are still selectable.
        return [name, { models: [], state: "unavailable" as const }] as const;
      }
    }),
  );
  return Object.fromEntries(entries);
}

function loadCatalogs(): Promise<Catalogs> {
  if (inFlight) return inFlight;
  const p = fetchCatalogs().finally(() => {
    if (inFlight === p) inFlight = null;
  });
  inFlight = p;
  return p;
}

/**
 * Full catalogs including discovery state — for callers that render the
 * difference between "still discovering", "couldn't ask" and "no models".
 */
export function useProviderCatalogs(): Catalogs {
  const [catalogs, setCatalogs] = useState<Catalogs>({});

  useEffect(() => {
    let cancelled = false;
    let retry: ReturnType<typeof setTimeout> | undefined;

    const apply = (next: Catalogs) => {
      if (cancelled) return;
      setCatalogs(next);
      // Anything still mid-discovery gets exactly one more look.
      const pending = Object.values(next).some(
        (c) => c.state === "refreshing" || c.state === "pending",
      );
      if (pending && retry === undefined) {
        retry = setTimeout(() => {
          loadCatalogs()
            .then((again) => {
              if (!cancelled) setCatalogs(again);
            })
            .catch(() => {});
        }, RETRY_MS);
      }
    };

    loadCatalogs()
      .then(apply)
      .catch(() => {
        if (!cancelled) setCatalogs({});
      });

    return () => {
      cancelled = true;
      if (retry !== undefined) clearTimeout(retry);
    };
  }, []);

  return catalogs;
}

/**
 * Just the models, the shape the four callers that don't care about
 * discovery state already consume.
 */
export function useProviderModels(): Record<string, Model[]> {
  const catalogs = useProviderCatalogs();
  const out: Record<string, Model[]> = {};
  for (const [name, c] of Object.entries(catalogs)) out[name] = c.models;
  return out;
}
