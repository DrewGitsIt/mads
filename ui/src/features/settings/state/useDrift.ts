import { useCallback, useMemo } from "react";
import type { Profile } from "../../../router/types";
import { applySnapshot, snapshotFromConfig } from "../profileSnapshot";
import {
  computeCredentialDiff,
  computeProfileDiff,
  type CredentialDiffEntry,
  type DiffEntry,
} from "../profileDiff";
import type { UseConfig } from "./useConfig";
import type { UseProfiles } from "./useProfiles";

export interface UseDrift {
  /** The profile bound by config.saved.active_profile_id, if any. */
  activeProfile: Profile | undefined;
  /** True when the active profile's snapshot no longer matches the live draft. */
  isDrifted: boolean;
  fieldDiff: DiffEntry[];
  credentialDiff: CredentialDiffEntry[];
  /** Discards the drift: resets draft to what the active profile prescribes and persists it. */
  revert: () => Promise<boolean>;
  /** Recaptures the current draft into the active profile's snapshot. */
  updateProfile: () => Promise<boolean>;
  /** Creates a new profile from the current draft and activates it. */
  saveAsNewProfile: (name: string, description?: string) => Promise<boolean>;
}

/**
 * Drift = the live draft diverging from the active profile's own snapshot —
 * not from `saved` (that's just "unsaved edits," a different question).
 * Computed straight off `draft` so an in-progress, not-yet-saved field edit
 * shows drift immediately, per the task's "any field edit ... shows a
 * MODIFIED bar" requirement.
 */
export function useDrift(
  config: UseConfig,
  profiles: UseProfiles,
  credentialNames: string[],
): UseDrift {
  const activeProfile = useMemo(
    () => profiles.profiles.find((p) => p.id === config.saved?.active_profile_id),
    [profiles.profiles, config.saved?.active_profile_id],
  );

  const fieldDiff = useMemo(() => {
    if (!activeProfile || !config.draft) return [];
    return computeProfileDiff(config.draft, activeProfile.snapshot);
  }, [activeProfile, config.draft]);

  const credentialDiff = useMemo(() => {
    if (!activeProfile || !config.draft) return [];
    const targetBindings = activeProfile.snapshot.credential_bindings as string[] | undefined;
    return computeCredentialDiff(credentialNames, config.draft.credential_bindings, targetBindings);
  }, [activeProfile, config.draft, credentialNames]);

  const isDrifted = !!activeProfile && (fieldDiff.length > 0 || credentialDiff.length > 0);

  const revert = useCallback(async () => {
    if (!activeProfile || !config.draft) return false;
    const reverted = applySnapshot(config.draft, activeProfile.snapshot);
    return config.save(reverted);
  }, [activeProfile, config]);

  const updateProfile = useCallback(async () => {
    if (!activeProfile || !config.draft) return false;
    const ok = await config.save(config.draft);
    if (!ok) return false;
    const snapshot = snapshotFromConfig(config.draft);
    await profiles.update({ ...activeProfile, snapshot });
    return true;
  }, [activeProfile, config, profiles]);

  const saveAsNewProfile = useCallback(
    async (name: string, description?: string) => {
      if (!config.draft) return false;
      const ok = await config.save(config.draft);
      if (!ok) return false;
      const snapshot = snapshotFromConfig(config.draft);
      const created = await profiles.create({ name, description, snapshot });
      await profiles.activate(created.id);
      await config.refetch();
      return true;
    },
    [config, profiles],
  );

  return { activeProfile, isDrifted, fieldDiff, credentialDiff, revert, updateProfile, saveAsNewProfile };
}
