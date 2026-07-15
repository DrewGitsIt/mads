import { useEffect, useRef, useState } from "react";
import { routerGet } from "../../../router/client";
import type { BehaviorInfo, ProviderInfo } from "../../../router/types";

export interface UseProviderMatrix {
  providers: ProviderInfo[];
  behaviors: BehaviorInfo[];
  loading: boolean;
}

/** Two parallel fetches joined client-side into the Providers panel's table. */
export function useProviderMatrix(): UseProviderMatrix {
  const [providers, setProviders] = useState<ProviderInfo[]>([]);
  const [behaviors, setBehaviors] = useState<BehaviorInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    Promise.all([
      routerGet<ProviderInfo[]>("/providers"),
      routerGet<BehaviorInfo[]>("/providers/behaviors"),
    ]).then(([p, b]) => {
      if (!mounted.current) return;
      setProviders(p);
      setBehaviors(b);
      setLoading(false);
    });
    return () => {
      mounted.current = false;
    };
  }, []);

  return { providers, behaviors, loading };
}
