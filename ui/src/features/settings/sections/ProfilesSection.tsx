import { useState } from "react";
import { Button } from "../../../components/Button/Button";
import { Chip } from "../../../components/Chip/Chip";
import { ProfileSwitchDialog } from "../ProfileSwitchDialog";
import type { UseConfig } from "../state/useConfig";
import type { UseProfiles } from "../state/useProfiles";
import type { Config, Profile } from "../../../router/types";
import styles from "./ProfilesSection.module.css";

export interface ProfilesSectionProps {
  config: UseConfig;
  profiles: UseProfiles;
  credentialNames: string[];
  onSwitched: (previousConfig: Config, restartRequired: boolean) => void;
}

/**
 * Profile cards + switch flow (HANDOFF §3). No edit/delete UI here — a
 * profile is a full-config snapshot managed via DriftBar's
 * Update/Save-as-new/Revert, not a separate CRUD form.
 */
export function ProfilesSection({ config, profiles, credentialNames, onSwitched }: ProfilesSectionProps) {
  const [switchTarget, setSwitchTarget] = useState<Profile | null>(null);
  const [previousConfig, setPreviousConfig] = useState<Config | null>(null);

  if (!config.saved) return null;

  const closeDialog = () => {
    setSwitchTarget(null);
    setPreviousConfig(null);
  };

  const openSwitch = (profile: Profile) => {
    setPreviousConfig(config.saved);
    setSwitchTarget(profile);
  };

  return (
    <div className={styles.panel}>
      <div className={styles.grid}>
        {profiles.profiles.map((profile) => {
          const active = profile.id === config.saved?.active_profile_id;
          return (
            <div key={profile.id} className={active ? `${styles.card} ${styles.active}` : styles.card}>
              <div className={styles.cardHeader}>
                <span className={styles.name}>{profile.name}</span>
                {active && <Chip tone="ok">Active</Chip>}
              </div>
              {profile.description && <p className={styles.description}>{profile.description}</p>}
              {!active && (
                <Button variant="secondary" onClick={() => openSwitch(profile)}>
                  Switch to this profile
                </Button>
              )}
            </div>
          );
        })}
        {profiles.profiles.length === 0 && <p className={styles.empty}>No profiles yet.</p>}
      </div>

      {switchTarget && previousConfig && (
        <ProfileSwitchDialog
          profile={switchTarget}
          currentConfig={previousConfig}
          credentialNames={credentialNames}
          activate={profiles.activate}
          refetchConfig={config.refetch}
          onApplied={(restartRequired) => {
            onSwitched(previousConfig, restartRequired);
            closeDialog();
          }}
          onClose={closeDialog}
        />
      )}
    </div>
  );
}
