import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import type { Agent, GenerateAgentBody, Similar } from "../../router/types";

export interface UseAgentGeneration {
  draft: Agent | null;
  similar: Similar | null;
  pending: boolean;
  error: string | null;
  generate: (body: GenerateAgentBody) => Promise<void>;
  clear: () => void;
}

/** POSTs /agents/generate, then waits for the async agent.draft_generated result over SSE. */
export function useAgentGeneration(): UseAgentGeneration {
  const [draft, setDraft] = useState<Agent | null>(null);
  const [similar, setSimilar] = useState<Similar | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const jobId = useRef<string | null>(null);

  useEffect(() => {
    const unsubscribe = subscribeToEvents(
      { types: ["agent.draft_generated", "job.failed"] },
      (envelope) => {
        if (envelope.type === "agent.draft_generated") {
          const payload = envelope.payload as { draft: Agent; similar?: Similar };
          setDraft(payload.draft);
          setSimilar(payload.similar ?? null);
          setPending(false);
        } else if (envelope.type === "job.failed") {
          const payload = envelope.payload as { job_id: string; kind: string; error: string };
          if (jobId.current && payload.job_id === jobId.current) {
            setError(payload.error);
            setPending(false);
          }
        }
      },
    );
    return unsubscribe;
  }, []);

  const generate = useCallback(async (body: GenerateAgentBody) => {
    setPending(true);
    setError(null);
    setDraft(null);
    setSimilar(null);
    try {
      const result = await router.generateAgent(body);
      jobId.current = result.job_id;
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setPending(false);
    }
  }, []);

  const clear = useCallback(() => {
    setDraft(null);
    setSimilar(null);
    setError(null);
  }, []);

  return { draft, similar, pending, error, generate, clear };
}
