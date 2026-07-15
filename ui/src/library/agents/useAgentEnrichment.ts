import { useCallback } from "react";
import { routerPost } from "../../router/client";
import type { AgentCapabilities } from "../../router/types";

export interface UseAgentEnrichment {
  describe: (prompt: string) => Promise<string>;
  suggestCapabilities: (prompt: string) => Promise<AgentCapabilities>;
  refine: (prompt: string, instruction: string) => Promise<string>;
}

/** Synchronous request/response enrichment actions (spec §9.5) — no job/event plumbing. */
export function useAgentEnrichment(): UseAgentEnrichment {
  const describe = useCallback(async (prompt: string) => {
    const result = await routerPost<{ result: string }>("/agents/describe", { prompt });
    return result.result;
  }, []);

  const suggestCapabilities = useCallback(async (prompt: string) => {
    return routerPost<AgentCapabilities>("/agents/suggest-capabilities", { prompt });
  }, []);

  const refine = useCallback(async (prompt: string, instruction: string) => {
    const result = await routerPost<{ result: string }>("/agents/refine", { prompt, instruction });
    return result.result;
  }, []);

  return { describe, suggestCapabilities, refine };
}
