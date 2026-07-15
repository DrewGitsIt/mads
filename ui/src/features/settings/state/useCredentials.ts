import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";

export interface UseCredentials {
  names: string[];
  loading: boolean;
  envCandidates: string[];
  add: (name: string, value: string) => Promise<void>;
  remove: (name: string) => Promise<void>;
  loadEnvCandidates: () => Promise<void>;
  importFromEnv: (names: string[]) => Promise<string[]>;
}

/**
 * names: string[] only, ever — this hook structurally cannot retain a
 * credential value: add() takes `value` as a plain parameter and never
 * assigns it to any hook state (HANDOFF §4/§8, the security acceptance
 * criterion's mechanism). Env-var values are read server-side inside the
 * Router process (importFromEnv) and never appear in this hook's state or
 * any response body it reads.
 */
export function useCredentials(): UseCredentials {
  const [names, setNames] = useState<string[]>([]);
  const [envCandidates, setEnvCandidates] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    const list = await router.listCredentials();
    if (mounted.current) setNames(list);
  }, []);

  useEffect(() => {
    mounted.current = true;
    setLoading(true);
    refetch().finally(() => {
      if (mounted.current) setLoading(false);
    });
    return () => {
      mounted.current = false;
    };
  }, [refetch]);

  const add = useCallback(async (name: string, value: string) => {
    const result = await router.createCredential(name, value);
    if (mounted.current) {
      setNames((prev) => (prev.includes(result.name) ? prev : [...prev, result.name].sort()));
    }
  }, []);

  const remove = useCallback(async (name: string) => {
    await router.deleteCredential(name);
    if (mounted.current) setNames((prev) => prev.filter((n) => n !== name));
  }, []);

  const loadEnvCandidates = useCallback(async () => {
    const candidates = await router.listCredentialEnvCandidates();
    if (mounted.current) setEnvCandidates(candidates);
  }, []);

  const importFromEnv = useCallback(async (namesToImport: string[]) => {
    const result = await router.importCredentials(namesToImport);
    if (mounted.current) {
      setNames((prev) => [...new Set([...prev, ...result.imported])].sort());
      setEnvCandidates((prev) => prev.filter((n) => !result.imported.includes(n)));
    }
    return result.imported;
  }, []);

  return { names, loading, envCandidates, add, remove, loadEnvCandidates, importFromEnv };
}
