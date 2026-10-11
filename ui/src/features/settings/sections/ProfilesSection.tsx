import { useState } from "react";
import { Button } from "../../../components/Button/Button";
import { Chip } from "../../../components/Chip/Chip";
import { ProfileSwitchDialog } from "../ProfileSwitchDialog";
import { useDrift } from "../state/useDrift";
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
 * Profile cards + switch flow (HANDOFF §3), plus inline rename/delete. A
 * profile's config *snapshot* is still managed via DriftBar's
 * Update/Save-as-new/Revert — the edit here is only its name/description, and
 * delete removes the whole profile. System profiles and the active profile are
 * protected: renaming the snapshot's identity or deleting the profile you're
 * running on has no safe meaning, so those affordances are hidden for them.
 */
export function ProfilesSection({ config, profiles, credentialNames, onSwitched }: ProfilesSectionProps) {
  const [switchTarget, setSwitchTarget] = useState<Profile | null>(null);
  const [previousConfig, setPreviousConfig] = useState<Config | null>(null);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editName, setEditName] = useState("");
  const [editDescription, setEditDescription] = useState("");
  const [confirmingDeleteId, setConfirmingDeleteId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Only for saveAsNewProfile, so the new-profile card does exactly what
  // DriftBar's "Save as new profile" does.
  const drift = useDrift(config, profiles, credentialNames);

  if (!config.saved) return null;

  // Switching re-fetches config, which would wipe the draft — so with unsaved
  // edits pending it waits until they're saved or discarded.
  const switchBlocked = config.dirtyFields.length > 0;

  const closeDialog = () => {
    setSwitchTarget(null);
    setPreviousConfig(null);
  };

  const openSwitch = (profile: Profile) => {
    setPreviousConfig(config.saved);
    setSwitchTarget(profile);
  };

  const startEdit = (profile: Profile) => {
    setError(null);
    setEditingId(profile.id);
    setEditName(profile.name);
    setEditDescription(profile.description ?? "");
  };

  const saveEdit = async (profile: Profile) => {
    const name = editName.trim();
    if (!name) {
      setError("Name must not be empty.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await profiles.update({ ...profile, name, description: editDescription.trim() });
      setEditingId(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const confirmDelete = async (id: string) => {
    setBusy(true);
    setError(null);
    try {
      await profiles.remove(id);
      setConfirmingDeleteId(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className={styles.panel}>
      {error && <p className={styles.error}>{error}</p>}
      {/* One hint for both affordances it holds back: Switch and the new-profile card. */}
      {switchBlocked && (
        <p className={styles.hint}>Save or discard your changes first to switch or create profiles.</p>
      )}
      <div className={styles.grid}>
        {profiles.profiles.map((profile) => {
          const active = profile.id === config.saved?.active_profile_id;
          const editable = !profile.system;
          const editing = editingId === profile.id;
          return (
            <div key={profile.id} className={active ? `${styles.card} ${styles.active}` : styles.card}>
              {editing ? (
                <div className={styles.editForm}>
                  <input
                    className={styles.editInput}
                    aria-label="Profile name"
                    value={editName}
                    autoFocus
                    onChange={(e) => setEditName(e.target.value)}
                  />
                  <input
                    className={styles.editInput}
                    aria-label="Profile description"
                    placeholder="Description (optional)"
                    value={editDescription}
                    onChange={(e) => setEditDescription(e.target.value)}
                  />
                  <div className={styles.cardActions}>
                    <Button variant="primary" disabled={busy} onClick={() => void saveEdit(profile)}>
                      Save
                    </Button>
                    <Button variant="ghost" disabled={busy} onClick={() => setEditingId(null)}>
                      Cancel
                    </Button>
                  </div>
                </div>
              ) : (
                <>
                  <div className={styles.cardHeader}>
                    <span className={styles.name}>{profile.name}</span>
                    {active && <Chip tone="ok">Active</Chip>}
                    {profile.system && <Chip tone="neutral">Built-in</Chip>}
                  </div>
                  {profile.description && <p className={styles.description}>{profile.description}</p>}
                  <div className={styles.cardActions}>
                    {!active && (
                      <Button variant="secondary" disabled={switchBlocked} onClick={() => openSwitch(profile)}>
                        Switch to this profile
                      </Button>
                    )}
                    {editable && (
                      <Button variant="ghost" size="sm" onClick={() => startEdit(profile)}>
                        Rename
                      </Button>
                    )}
                    {editable && !active && (
                      <Button
                        variant="ghost"
                        size="sm"
                        className={styles.delete}
                        onClick={() => setConfirmingDeleteId(profile.id)}
                      >
                        Delete
                      </Button>
                    )}
                  </div>
                  {confirmingDeleteId === profile.id && (
                    <div className={styles.confirm}>
                      <span>Delete “{profile.name}”?</span>
                      <div className={styles.cardActions}>
                        <Button
                          variant="secondary"
                          size="sm"
                          className={styles.delete}
                          disabled={busy}
                          onClick={() => void confirmDelete(profile.id)}
                        >
                          Delete
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={busy}
                          onClick={() => setConfirmingDeleteId(null)}
                        >
                          Cancel
                        </Button>
                      </div>
                    </div>
                  )}
                </>
              )}
            </div>
          );
        })}
        <NewProfileCard blocked={switchBlocked} create={drift.saveAsNewProfile} />
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

interface NewProfileCardProps {
  /** Unsaved edits pending: the snapshot is of saved settings, so it waits for the save bar. */
  blocked: boolean;
  create: (name: string) => Promise<boolean>;
}

/**
 * The grid's last card: "New profile from current settings". Click turns it
 * into a name field; Save snapshots the *saved* config into a new profile and
 * switches to it, as DriftBar's "Save as new profile" does.
 */
function NewProfileCard({ blocked, create }: NewProfileCardProps) {
  const [naming, setNaming] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const cancel = () => {
    setNaming(false);
    setName("");
    setError(null);
  };

  const save = async () => {
    const trimmed = name.trim();
    if (!trimmed || blocked) return;
    setBusy(true);
    setError(null);
    try {
      if (await create(trimmed)) cancel();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  if (!naming) {
    return (
      <button
        type="button"
        className={`${styles.card} ${styles.newCard}`}
        disabled={blocked}
        onClick={() => setNaming(true)}
      >
        <span>＋ New profile from current settings</span>
      </button>
    );
  }

  return (
    <form
      className={`${styles.card} ${styles.newCardForm}`}
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <input
        className={styles.editInput}
        aria-label="New profile name"
        placeholder="New profile name"
        value={name}
        autoFocus
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") cancel();
        }}
      />
      {error && <p className={styles.error}>{error}</p>}
      <div className={styles.cardActions}>
        <Button type="submit" variant="primary" size="sm" disabled={busy || blocked || !name.trim()}>
          Save
        </Button>
        <Button variant="ghost" size="sm" disabled={busy} onClick={cancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
