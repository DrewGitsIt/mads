import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { subscribeToEvents } from "../../../router/events";
import { RETRO_GENERATION_EVENT_TYPES } from "../events";
import type { Envelope } from "../../../router/types";

export type GenerationPhase =
  | "idle"
  | "starting"
  | "gathering"
  | "generating"
  | "completed"
  | "failed"
  | "cancelled";

export interface UseRetroGeneration {
  phase: GenerationPhase;
  error: string | null;
  retroId: string | null;
  start: (sessionIds: string[], userObservations: string) => Promise<void>;
  cancel: () => void;
  reset: () => void;
}

/** Owns the retro generation job lifecycle only — not the resulting DPA data, see useRetroDetail. */
export function useRetroGeneration(): UseRetroGeneration {
  const [phase, setPhase] = useState<GenerationPhase>("idle");
  const [error, setError] = useState<string | null>(null);
  const [retroId, setRetroId] = useState<string | null>(null);
  const jobIdRef = useRef<string | null>(null);
  const retroIdRef = useRef<string | null>(null);
  const unsubscribeRef = useRef<(() => void) | null>(null);

  useEffect(() => () => unsubscribeRef.current?.(), []);

  const handleEvent = useCallback((envelope: Envelope) => {
    const payload = envelope.payload as
      | { retro_id?: string; step?: string; error?: string }
      | undefined;
    if (!payload?.retro_id || payload.retro_id !== retroIdRef.current) return;
    switch (envelope.type) {
      case "retro.started":
        setPhase("starting");
        break;
      case "retro.progress":
        if (payload.step === "gathering") setPhase("gathering");
        else if (payload.step === "generating") setPhase("generating");
        break;
      case "retro.completed":
        setPhase("completed");
        break;
      case "retro.failed":
        setPhase("failed");
        setError(payload.error ?? "Retro generation failed");
        break;
      case "retro.cancelled":
        setPhase("cancelled");
        break;
      default:
        break;
    }
  }, []);

  const start = useCallback(
    async (sessionIds: string[], userObservations: string) => {
      setError(null);
      try {
        const result = await router.startRetro(sessionIds, userObservations || undefined);
        jobIdRef.current = result.job_id;
        retroIdRef.current = result.retro_id;
        setRetroId(result.retro_id);
        setPhase("starting");
        unsubscribeRef.current = subscribeToEvents(
          { types: RETRO_GENERATION_EVENT_TYPES },
          handleEvent,
        );
      } catch (err) {
        setPhase("failed");
        setError(err instanceof Error ? err.message : String(err));
      }
    },
    [handleEvent],
  );

  const cancel = useCallback(() => {
    if (!jobIdRef.current) return;
    router.cancelJob(jobIdRef.current).catch((err: unknown) => {
      setError(err instanceof Error ? err.message : String(err));
    });
  }, []);

  const reset = useCallback(() => {
    unsubscribeRef.current?.();
    unsubscribeRef.current = null;
    jobIdRef.current = null;
    retroIdRef.current = null;
    setPhase("idle");
    setError(null);
    setRetroId(null);
  }, []);

  return { phase, error, retroId, start, cancel, reset };
}
