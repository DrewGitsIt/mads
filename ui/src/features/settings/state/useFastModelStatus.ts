import { useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import type { FastModelStatus } from "../../../router/types";

/**
 * Fetch-on-mount GET /fast-model/status — the live signal for whether
 * "provider" mode is actually working, since that depends on the resolved
 * default provider's Completer support, which the UI can't derive from
 * Config alone.
 */
export function useFastModelStatus(): { status: FastModelStatus | null; loading: boolean } {
  const [status, setStatus] = useState<FastModelStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    setLoading(true);
    router
      .getFastModelStatus()
      .then((result) => {
        if (mounted.current) setStatus(result);
      })
      .finally(() => {
        if (mounted.current) setLoading(false);
      });
    return () => {
      mounted.current = false;
    };
  }, []);

  return { status, loading };
}
