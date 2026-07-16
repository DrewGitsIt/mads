import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import type { RegistryItemType, RegistryPublishItem } from "../../router/types";

export interface UseRegistryJob {
  pending: boolean;
  error: string | null;
  install: (type: RegistryItemType, name: string, version?: string) => Promise<void>;
  update: (type: RegistryItemType, name: string, version?: string) => Promise<void>;
  publish: (items: RegistryPublishItem[]) => Promise<void>;
}

/**
 * Tracks one in-flight registry install/update/publish job to a terminal
 * state over SSE — mirrors useAgentGeneration.ts's job_id matching, extended
 * to also resolve the returned promise on job.completed (not just surface
 * job.failed as an error).
 */
export function useRegistryJob(): UseRegistryJob {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const jobId = useRef<string | null>(null);
  const resolveRef = useRef<(() => void) | null>(null);
  const rejectRef = useRef<((err: Error) => void) | null>(null);

  useEffect(() => {
    const unsubscribe = subscribeToEvents(
      { types: ["job.completed", "job.failed"] },
      (envelope) => {
        const payload = envelope.payload as { job_id: string; error?: string };
        if (!jobId.current || payload.job_id !== jobId.current) return;
        jobId.current = null;
        setPending(false);
        if (envelope.type === "job.failed") {
          const message = payload.error ?? "job failed";
          setError(message);
          rejectRef.current?.(new Error(message));
        } else {
          resolveRef.current?.();
        }
        resolveRef.current = null;
        rejectRef.current = null;
      },
    );
    return unsubscribe;
  }, []);

  const run = useCallback((spawn: () => Promise<{ job_id: string }>): Promise<void> => {
    setPending(true);
    setError(null);
    return new Promise<void>((resolve, reject) => {
      resolveRef.current = resolve;
      rejectRef.current = reject;
      spawn()
        .then((result) => {
          jobId.current = result.job_id;
        })
        .catch((err: unknown) => {
          setPending(false);
          resolveRef.current = null;
          rejectRef.current = null;
          const wrapped = err instanceof Error ? err : new Error(String(err));
          setError(wrapped.message);
          reject(wrapped);
        });
    });
  }, []);

  const install = useCallback(
    (type: RegistryItemType, name: string, version?: string) =>
      run(() => router.installRegistryItem(type, name, version)),
    [run],
  );

  const update = useCallback(
    (type: RegistryItemType, name: string, version?: string) =>
      run(() => router.updateRegistryItem(type, name, version)),
    [run],
  );

  const publish = useCallback(
    (items: RegistryPublishItem[]) => run(() => router.publishRegistryItems(items)),
    [run],
  );

  return { pending, error, install, update, publish };
}
