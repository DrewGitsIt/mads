import { useCallback, useEffect, useRef, useState } from "react";
import { routerDelete, routerGet, routerPost, routerPut } from "../../../router/client";
import type { Config, Profile } from "../../../router/types";

export interface UseProfiles {
  profiles: Profile[];
  loading: boolean;
  create: (profile: Partial<Profile>) => Promise<Profile>;
  update: (profile: Profile) => Promise<Profile>;
  remove: (id: string) => Promise<void>;
  activate: (id: string) => Promise<Config>;
}

/**
 * Fetch-once + action shape, mirroring useTaskStore/useRetroHistory — no
 * live event subscription, Settings mutations are refetch-on-own-action
 * only (single-user local app, same reasoning as useRetroDetail).
 * `activate` returns the merged Config so the caller can feed it into the
 * same useConfig instance it's sharing (HANDOFF §4 — don't duplicate config
 * state between panels).
 */
export function useProfiles(): UseProfiles {
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [loading, setLoading] = useState(true);
  const mounted = useRef(true);

  const refetch = useCallback(async () => {
    const list = await routerGet<Profile[]>("/profiles");
    if (mounted.current) setProfiles(list);
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

  const create = useCallback(
    async (profile: Partial<Profile>) => {
      const created = await routerPost<Profile>("/profiles", profile);
      await refetch();
      return created;
    },
    [refetch],
  );

  const update = useCallback(
    async (profile: Profile) => {
      const updated = await routerPut<Profile>(`/profiles/${profile.id}`, profile);
      await refetch();
      return updated;
    },
    [refetch],
  );

  const remove = useCallback(
    async (id: string) => {
      await routerDelete(`/profiles/${id}`);
      await refetch();
    },
    [refetch],
  );

  const activate = useCallback(async (id: string) => {
    return routerPost<Config>(`/profiles/${id}/activate`);
  }, []);

  return { profiles, loading, create, update, remove, activate };
}
