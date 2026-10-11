import { useCallback, useEffect, useRef, useState } from "react";
import { router, RouterValidationError } from "../../../router/client";
import type { Config } from "../../../router/types";
import { dirtyFields } from "../dirtyFields";
import { fieldErrorMap } from "../validation/fieldErrors";

export interface UseConfig {
  /** Server truth as of the last successful save (or mount). */
  saved: Config | null;
  /** In-progress edits — panels read/write this. */
  draft: Config | null;
  loading: boolean;
  fieldErrors: Record<string, string>;
  /** Top-level fields with unsaved edits (saved vs draft) — the save bar's count. */
  dirtyFields: (keyof Config)[];
  setDraft: (next: Config) => void;
  /** Saves `overrideDraft` if given, else the current `draft` state — lets a
   * caller that just called setDraft() avoid a stale-closure read of the
   * pre-update draft in the same event-handler tick (e.g. AppearanceSection's
   * apply-immediately picker). */
  save: (overrideDraft?: Config) => Promise<boolean>;
  /**
   * Persists just `patch` on top of `saved` — never the draft — for controls
   * that apply live (theme, feature flags). The patch lands in `saved` and the
   * draft at once, so it never reads as an unsaved edit, and every other
   * unsaved draft edit is left alone. On failure the patched fields roll back.
   */
  saveFields: (patch: Partial<Config>) => Promise<boolean>;
  /** Re-fetches GET /config and resets saved/draft to the result —
   * used after a Profiles Activate call merges a new config server-side, so
   * ProfilesSection can share this instance instead of duplicating config
   * state (HANDOFF §4). */
  refetch: () => Promise<void>;
}

/**
 * Fetch-once GET /config, holding both `saved` (server truth) and `draft`
 * (in-progress edits). save() PUTs the whole draft — the Settings save bar —
 * while saveFields() persists one live control's field(s). On a save()
 * validation failure the
 * draft is left untouched (don't discard the user's edits) and fieldErrors
 * is populated for inline display (HANDOFF §4/§7).
 */
export function useConfig(): UseConfig {
  const [saved, setSaved] = useState<Config | null>(null);
  const [draft, setDraftState] = useState<Config | null>(null);
  const [loading, setLoading] = useState(true);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const mounted = useRef(true);
  // Server truth, read synchronously by saveFields: `saved` state also carries
  // in-flight live patches, and a closure's copy can be a render stale.
  const confirmed = useRef<Config | null>(null);
  // saveFields calls run one at a time, so each PUT builds on the last one's
  // result instead of racing it (two quick theme clicks mustn't undo each other).
  const fieldQueue = useRef<Promise<unknown>>(Promise.resolve());

  const fetchConfig = useCallback(async () => {
    const cfg = await router.getConfig();
    if (!mounted.current) return;
    confirmed.current = cfg;
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
        confirmed.current = updated;
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

  const saveFields = useCallback((patch: Partial<Config>) => {
    const keys = Object.keys(patch) as (keyof Config)[];
    const pick = (from: Partial<Config>) =>
      Object.fromEntries(keys.map((k) => [k, from[k]])) as Partial<Config>;
    // Only overwrite a draft field the user hasn't since changed again.
    const mergeIntoDraft = (values: Partial<Config>) =>
      setDraftState((d) => {
        if (!d) return d;
        const next = { ...d };
        for (const k of keys) {
          if (JSON.stringify(d[k]) === JSON.stringify(patch[k])) {
            (next as Record<string, unknown>)[k] = values[k];
          }
        }
        return next;
      });

    // Optimistic, so the control reflects the click before the round trip.
    setSaved((s) => (s ? { ...s, ...patch } : s));
    setDraftState((d) => (d ? { ...d, ...patch } : d));

    const run = async () => {
      const base = confirmed.current;
      if (!base) return false;
      try {
        const updated = await router.updateConfig({ ...base, ...patch });
        confirmed.current = updated;
        const applied = pick(updated);
        setSaved((s) => (s ? { ...s, ...applied } : s));
        mergeIntoDraft(applied);
        return true;
      } catch {
        const previous = pick(base);
        setSaved((s) => (s ? { ...s, ...previous } : s));
        mergeIntoDraft(previous);
        return false;
      }
    };
    const result = fieldQueue.current.then(run);
    fieldQueue.current = result;
    return result;
  }, []);

  return {
    saved,
    draft,
    loading,
    fieldErrors,
    dirtyFields: saved && draft ? dirtyFields(saved, draft) : [],
    setDraft,
    save,
    saveFields,
    refetch: fetchConfig,
  };
}
