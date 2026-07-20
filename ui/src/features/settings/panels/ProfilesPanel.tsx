import { useState } from "react";
import { Button } from "../../../components/Button/Button";
import { Card } from "../../../components/Card/Card";
import type { UseConfig } from "../state/useConfig";
import { useProfiles } from "../state/useProfiles";
import type { Profile } from "../../../router/types";
import styles from "./ProfilesPanel.module.css";

export interface ProfilesPanelProps {
  config: UseConfig;
}

/** Profile list with create/edit/delete + Activate (§10.5). System profiles ship with mads: editable, not deletable. */
export function ProfilesPanel({ config }: ProfilesPanelProps) {
  const profiles = useProfiles();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editName, setEditName] = useState("");
  const [editDescription, setEditDescription] = useState("");
  const [activatingId, setActivatingId] = useState<string | null>(null);

  const handleCreate = async () => {
    if (!name.trim()) return;
    await profiles.create({
      name: name.trim(),
      description: description.trim() || undefined,
      snapshot: {},
    });
    setName("");
    setDescription("");
  };

  const startEdit = (profile: Profile) => {
    setEditingId(profile.id);
    setEditName(profile.name);
    setEditDescription(profile.description ?? "");
  };

  const handleUpdate = async (profile: Profile) => {
    await profiles.update({
      ...profile,
      name: editName.trim(),
      description: editDescription.trim() || undefined,
    });
    setEditingId(null);
  };

  const handleActivate = async (id: string) => {
    setActivatingId(id);
    const merged = await profiles.activate(id);
    config.setDraft(merged);
    await config.refetch();
    setActivatingId(null);
  };

  return (
    <div className={styles.panel}>
      <ul className={styles.list}>
        {profiles.profiles.map((profile) => (
          <li key={profile.id}>
            <Card className={styles.card}>
              {editingId === profile.id ? (
                <div className={styles.editForm}>
                  <input
                    aria-label="Profile name"
                    className={styles.input}
                    value={editName}
                    onChange={(e) => setEditName(e.target.value)}
                  />
                  <input
                    aria-label="Profile description"
                    className={styles.input}
                    value={editDescription}
                    onChange={(e) => setEditDescription(e.target.value)}
                  />
                  <div className={styles.actions}>
                    <Button size="sm" variant="primary" onClick={() => void handleUpdate(profile)}>
                      Save
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setEditingId(null)}>
                      Cancel
                    </Button>
                  </div>
                </div>
              ) : (
                <>
                  <div className={styles.name}>{profile.name}</div>
                  {profile.description && <div className={styles.description}>{profile.description}</div>}
                  <div className={styles.actions}>
                    <Button
                      size="sm"
                      variant="primary"
                      onClick={() => void handleActivate(profile.id)}
                      disabled={activatingId === profile.id}
                    >
                      Activate
                    </Button>
                    <Button size="sm" variant="secondary" onClick={() => startEdit(profile)}>
                      Edit
                    </Button>
                    {!profile.system && (
                      <Button size="sm" variant="ghost" onClick={() => void profiles.remove(profile.id)}>
                        Delete
                      </Button>
                    )}
                  </div>
                </>
              )}
            </Card>
          </li>
        ))}
      </ul>
      <div className={styles.createForm}>
        <input
          aria-label="New profile name"
          className={styles.input}
          placeholder="Profile name"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <input
          aria-label="New profile description"
          className={styles.input}
          placeholder="Description (optional)"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <Button variant="primary" onClick={() => void handleCreate()} disabled={!name.trim()}>
          Create profile
        </Button>
      </div>
    </div>
  );
}
