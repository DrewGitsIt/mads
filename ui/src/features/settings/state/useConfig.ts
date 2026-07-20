import { useCallback, useEffect, useRef, useState } from "react";
import { router, RouterValidationError } from "../../../router/client";
import type { Config } from "../../../router/types";
import { dirtyRestartFields } from "../restartRequired";
import { fieldErrorMap } from "../validation/fieldErrors";

export interface UseConfig {
  /** Config as fetched at mount — never mutated, the diff base for restart badges. */
  initial: Config | null;
  /** Server truth as of the last successful save (or mount). */
  saved: Config | null;
  /** In-progress edits — panels read/write this. */
  draft: Config | null;
  loading: boolean;
  fieldErrors: Record<string, string>;
  dirtyRestartFields: (keyof Config)[];
  setDraft: (next: Config) => void;
  /** Saves `overrideDraft` if given, else the current `draft` state — lets a
   * caller that just called setDraft() avoid a stale-closure read of the
   * pre-update draft in the same event-handler tick (e.g. AppearanceSection's
   * apply-immediately picker). */
  save: (overrideDraft?: Config) => Promise<boolean>;
  /** Re-fetches GET /config and resets initial/saved/draft to the result —
   * used after a Profiles Activate call merges a new config server-side, so
   * ProfilesSection can share this instance instead of duplicating config
   * state (HANDOFF §4). */
  refetch: () => Promise<void>;
}

/**
 * Fetch-once GET /config, holding both `saved` (server truth) and `draft`
 * (in-progress edits). save() PUTs the draft; on a validation failure the
 * draft is left untouched (don't discard the user's edits) and fieldErrors
 * is populated for inline display (HANDOFF §4/§7).
 */
export function useConfig(): UseConfig {
  const [initial, setInitial] = useState<Config | null>(null);
  const [saved, setSaved] = useState<Config | null>(null);
  const [draft, setDraftState] = useState<Config | null>(null);
  const [loading, setLoading] = useState(true);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const mounted = useRef(true);

  const fetchConfig = useCallback(async () => {
    const cfg = await router.getConfig();
    if (!mounted.current) return;
    setInitial(cfg);
    setSaved(cfg);
    setDraftState(cfg);
  }, []);

  useEffect(() => {
    mounted.current = true;
    setLoading(true);
    fetchConfig().finally(() => {
      if (mounted.current) setLoading(false);
    });
    return () => {
      mounted.current = false;
    };
  }, [fetchConfig]);

  const setDraft = useCallback((next: Config) => setDraftState(next), []);

  const save = useCallback(
    async (overrideDraft?: Config) => {
      const body = overrideDraft ?? draft;
      if (!body) return false;
      try {
        const updated = await router.updateConfig(body);
        setFieldErrors({});
        setSaved(updated);
        setDraftState(updated);
        return true;
      } catch (err) {
        if (err instanceof RouterValidationError) {
          setFieldErrors(fieldErrorMap(err.errors));
        } else {
          setFieldErrors({});
        }
        return false;
      }
    },
    [draft],
  );

  return {
    initial,
    saved,
    draft,
    loading,
    fieldErrors,
    dirtyRestartFields: initial && draft ? dirtyRestartFields(initial, draft) : [],
    setDraft,
    save,
    refetch: fetchConfig,
  };
}
