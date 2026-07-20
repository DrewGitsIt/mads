import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { Chip } from "../../components/Chip/Chip";
import type { UseDrift } from "./state/useDrift";
import styles from "./DriftBar.module.css";

export interface DriftBarProps {
  drift: UseDrift;
}

/** MODIFIED bar: shown whenever the live draft diverges from the active profile's snapshot. */
export function DriftBar({ drift }: DriftBarProps) {
  const [savingAsNew, setSavingAsNew] = useState(false);
  const [newName, setNewName] = useState("");
  const [busy, setBusy] = useState(false);

  if (!drift.isDrifted) return null;

  const handleUpdate = async () => {
    setBusy(true);
    await drift.updateProfile();
    setBusy(false);
  };

  const handleRevert = async () => {
    setBusy(true);
    await drift.revert();
    setBusy(false);
  };

  const handleSaveAsNew = async () => {
    if (!newName.trim()) return;
    setBusy(true);
    const ok = await drift.saveAsNewProfile(newName.trim());
    setBusy(false);
    if (ok) {
      setSavingAsNew(false);
      setNewName("");
    }
  };

  return (
    <div className={styles.bar} role="status">
      <Chip tone="warn">Modified</Chip>
      <span className={styles.text}>
        Settings no longer match “{drift.activeProfile?.name}”.
      </span>
      {savingAsNew ? (
        <div className={styles.newProfileForm}>
          <input
            className={styles.input}
            placeholder="New profile name"
            aria-label="New profile name"
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            autoFocus
          />
          <Button variant="primary" size="sm" onClick={handleSaveAsNew} disabled={busy || !newName.trim()}>
            Save
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setSavingAsNew(false);
              setNewName("");
            }}
            disabled={busy}
          >
            Cancel
          </Button>
        </div>
      ) : (
        <div className={styles.actions}>
          <Button variant="primary" size="sm" onClick={handleUpdate} disabled={busy}>
            Update profile
          </Button>
          <Button variant="secondary" size="sm" onClick={() => setSavingAsNew(true)} disabled={busy}>
            Save as new profile
          </Button>
          <Button variant="ghost" size="sm" onClick={handleRevert} disabled={busy}>
            Revert
          </Button>
        </div>
      )}
    </div>
  );
}
