import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import type { PreflightCheck } from "../../../router/types";

export interface UsePreflight {
  checks: PreflightCheck[];
  loading: boolean;
  rerun: () => void;
}

/** GET /preflight — shared verbatim between Settings > General and the wizard's PreflightStep. */
export function usePreflight(): UsePreflight {
  const [checks, setChecks] = useState<PreflightCheck[]>([]);
  const [loading, setLoading] = useState(true);
  const mounted = useRef(true);

  const rerun = useCallback(() => {
    setLoading(true);
    router.getPreflightChecks().then((result) => {
      if (mounted.current) {
        setChecks(result);
        setLoading(false);
      }
    });
  }, []);

  useEffect(() => {
    mounted.current = true;
    rerun();
    return () => {
      mounted.current = false;
    };
  }, [rerun]);

  return { checks, loading, rerun };
}
