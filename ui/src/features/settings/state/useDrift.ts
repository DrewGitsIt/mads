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
  /** True when the active profile's snapshot no longer matches the saved config. */
  isDrifted: boolean;
  fieldDiff: DiffEntry[];
  credentialDiff: CredentialDiffEntry[];
  /**
   * The draft has unsaved edits. The actions below work on saved config, so
   * DriftBar holds them until the save bar is resolved — otherwise those edits
   * would be silently dropped (Revert) or silently left out (Update profile).
   */
  blocked: boolean;
  /** Discards the drift: overlays what the active profile prescribes onto saved config and persists it. */
  revert: () => Promise<boolean>;
  /** Recaptures the saved config into the active profile's snapshot. */
  updateProfile: () => Promise<boolean>;
  /** Creates a new profile from the saved config and activates it. */
  saveAsNewProfile: (name: string, description?: string) => Promise<boolean>;
}

/**
 * Drift = the *saved* config diverging from the active profile's own
 * snapshot. Unsaved edits are the save bar's question, not this one — diffing
 * the draft would raise both bars on every keystroke. So the Modified bar
 * appears once an edit is saved (or a live control like theme persists).
 */
export function useDrift(
  config: UseConfig,
  profiles: UseProfiles,
  credentialNames: string[],
): UseDrift {
  const { saved } = config;
  const activeProfile = useMemo(
    () => profiles.profiles.find((p) => p.id === saved?.active_profile_id),
    [profiles.profiles, saved?.active_profile_id],
  );

  const fieldDiff = useMemo(() => {
    if (!activeProfile || !saved) return [];
    return computeProfileDiff(saved, activeProfile.snapshot);
  }, [activeProfile, saved]);

  const credentialDiff = useMemo(() => {
    if (!activeProfile || !saved) return [];
    const targetBindings = activeProfile.snapshot.credential_bindings as string[] | undefined;
    return computeCredentialDiff(credentialNames, saved.credential_bindings, targetBindings);
  }, [activeProfile, saved, credentialNames]);

  const isDrifted = !!activeProfile && (fieldDiff.length > 0 || credentialDiff.length > 0);
  const blocked = config.dirtyFields.length > 0;

  const revert = useCallback(async () => {
    if (!activeProfile || !saved || blocked) return false;
    return config.save(applySnapshot(saved, activeProfile.snapshot));
  }, [activeProfile, saved, blocked, config]);

  const updateProfile = useCallback(async () => {
    if (!activeProfile || !saved || blocked) return false;
    await profiles.update({ ...activeProfile, snapshot: snapshotFromConfig(saved) });
    return true;
  }, [activeProfile, saved, blocked, profiles]);

  const saveAsNewProfile = useCallback(
    async (name: string, description?: string) => {
      if (!saved || blocked) return false;
      const created = await profiles.create({ name, description, snapshot: snapshotFromConfig(saved) });
      await profiles.activate(created.id);
      await config.refetch();
      return true;
    },
    [saved, blocked, config, profiles],
  );

  return { activeProfile, isDrifted, fieldDiff, credentialDiff, blocked, revert, updateProfile, saveAsNewProfile };
}
