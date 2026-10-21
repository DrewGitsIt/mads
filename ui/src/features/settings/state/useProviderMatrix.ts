import { useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import type { BehaviorInfo, ProviderInfo } from "../../../router/types";
import { INTERNAL_BEHAVIORS } from "../providerBehaviors";

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
    Promise.all([router.listProviders(), router.listProviderBehaviors()]).then(([p, b]) => {
      if (!mounted.current) return;
      setProviders(p);
      setBehaviors(b.filter((x) => !INTERNAL_BEHAVIORS.has(x.behavior)));
      setLoading(false);
    });
    return () => {
      mounted.current = false;
    };
  }, []);

  return { providers, behaviors, loading };
}
