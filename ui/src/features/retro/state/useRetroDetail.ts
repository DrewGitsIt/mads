import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import type { DPAStatus, Retro } from "../../../router/types";

export interface UseRetroDetail {
  retro: Retro | null;
  loading: boolean;
  toggleDpa: (dpaId: string) => void;
}

/**
 * Fetches one retro and offers an optimistic accept/reject toggle — shared
 * by the post-generation review and history click-through. No live event
 * subscription: single-user local desktop app, and the calling surface
 * already owns the optimistic state (HANDOFF §4).
 */
export function useRetroDetail(retroId: string | null): UseRetroDetail {
  const [retro, setRetro] = useState<Retro | null>(null);
  const [loading, setLoading] = useState(false);
  const retroRef = useRef<Retro | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    retroRef.current = retro;
  }, [retro]);

  useEffect(() => {
    mounted.current = true;
    if (!retroId) {
      setRetro(null);
      return;
    }
    setLoading(true);
    router
      .getRetro(retroId)
      .then((result) => {
        if (mounted.current) setRetro(result);
      })
      .finally(() => {
        if (mounted.current) setLoading(false);
      });
    return () => {
      mounted.current = false;
    };
  }, [retroId]);

  const toggleDpa = useCallback(
    (dpaId: string) => {
      if (!retroId) return;
      const current = retroRef.current;
      const dpa = current?.dpas.find((d) => d.id === dpaId);
      if (!dpa) return;
      const previousStatus = dpa.status;
      const nextStatus: DPAStatus = previousStatus === "rejected" ? "accepted" : "rejected";

      const applyStatus = (status: DPAStatus) => {
        setRetro((prev) =>
          prev
            ? { ...prev, dpas: prev.dpas.map((d) => (d.id === dpaId ? { ...d, status } : d)) }
            : prev,
        );
      };

      applyStatus(nextStatus);
      const request =
        nextStatus === "accepted"
          ? router.acceptDpa(retroId, dpaId)
          : router.rejectDpa(retroId, dpaId);
      request.catch(() => {
        applyStatus(previousStatus);
      });
    },
    [retroId],
  );

  return { retro, loading, toggleDpa };
}
