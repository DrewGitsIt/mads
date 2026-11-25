import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import { eventsReadyWithin, subscribeToEvents } from "../../../router/events";
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
      // Subscribed before the POST, and the POST sent only once the bus is
      // ready (router/events.ts eventsReady): the bus never replays, so the
      // job's first events could otherwise land before this hears anything.
      // Until the POST says which retro is ours, events wait here; then the
      // retro_id filter (handleEvent) picks ours out.
      unsubscribeRef.current?.();
      retroIdRef.current = null;
      const early: Envelope[] = [];
      let known = false;
      unsubscribeRef.current = subscribeToEvents({ types: RETRO_GENERATION_EVENT_TYPES }, (envelope) => {
        if (known) handleEvent(envelope);
        else early.push(envelope);
      });
      try {
        await eventsReadyWithin();
        const result = await router.startRetro(sessionIds, userObservations || undefined);
        jobIdRef.current = result.job_id;
        retroIdRef.current = result.retro_id;
        setRetroId(result.retro_id);
        setPhase("starting");
        known = true;
        for (const envelope of early.splice(0)) handleEvent(envelope);
      } catch (err) {
        unsubscribeRef.current?.();
        unsubscribeRef.current = null;
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
