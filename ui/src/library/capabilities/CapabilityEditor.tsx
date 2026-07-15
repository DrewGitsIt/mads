import { useState } from "react";
import { Button } from "../../components/Button/Button";
import {
  SideSheet,
  SideSheetBody,
  SideSheetFooter,
  SideSheetHeader,
} from "../../components/SideSheet/SideSheet";
import type { Capability, CapabilityType } from "../../router/types";
import { CredentialEnvVarsField } from "./CredentialEnvVarsField";
import { DiagnosePanel } from "./DiagnosePanel";
import styles from "./CapabilityEditor.module.css";

export interface CapabilityEditorProps {
  capability: Capability | null;
  onClose: () => void;
  onCreate: (capability: Partial<Capability>) => Promise<Capability>;
  onUpdate: (capability: Capability) => Promise<Capability>;
  onDelete: (id: string) => Promise<void>;
}

/** SideSheet-hosted create/edit form for a single capability (MADS-UX §3.5). */
export function CapabilityEditor({
  capability,
  onClose,
  onCreate,
  onUpdate,
  onDelete,
}: CapabilityEditorProps) {
  const [name, setName] = useState(capability?.name ?? "");
  const [description, setDescription] = useState(capability?.description ?? "");
  const [type, setType] = useState<CapabilityType>(capability?.type ?? "skill");
  const [path, setPath] = useState(capability?.config.path ?? "");
  const [command, setCommand] = useState(capability?.config.command ?? "");
  const [url, setUrl] = useState(capability?.config.url ?? "");
  const [credentialEnvVars, setCredentialEnvVars] = useState<string[]>(
    capability?.credential_env_vars ?? [],
  );
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  async function handleSave() {
    setSaving(true);
    setError(null);
    const payload: Partial<Capability> = {
      name,
      description: description || undefined,
      type,
      config: type === "skill" ? { path } : { command: command || undefined, url: url || undefined },
      credential_env_vars: credentialEnvVars.length ? credentialEnvVars : undefined,
    };
    try {
      if (capability) {
        await onUpdate({ ...capability, ...payload } as Capability);
      } else {
        await onCreate(payload);
      }
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete() {
    if (!capability) return;
    setSaving(true);
    try {
      await onDelete(capability.id);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setSaving(false);
    }
  }

  return (
    <SideSheet width={480}>
      <SideSheetHeader>{capability ? `Edit ${capability.name}` : "New capability"}</SideSheetHeader>
      <SideSheetBody>
        <div className={styles.form}>
          <label className={styles.field}>
            Name
            <input value={name} onChange={(e) => setName(e.target.value)} />
          </label>

          <label className={styles.field}>
            Description
            <input value={description} onChange={(e) => setDescription(e.target.value)} />
          </label>

          <label className={styles.field}>
            Type
            <select value={type} onChange={(e) => setType(e.target.value as CapabilityType)}>
              <option value="skill">Skill</option>
              <option value="mcp">MCP</option>
            </select>
          </label>

          {type === "skill" ? (
            <label className={styles.field}>
              Path
              <input value={path} onChange={(e) => setPath(e.target.value)} />
            </label>
          ) : (
            <>
              <label className={styles.field}>
                Command
                <input value={command} onChange={(e) => setCommand(e.target.value)} />
              </label>
              <label className={styles.field}>
                URL
                <input value={url} onChange={(e) => setUrl(e.target.value)} />
              </label>
            </>
          )}

          <CredentialEnvVarsField value={credentialEnvVars} onChange={setCredentialEnvVars} />

          {capability && <DiagnosePanel capability={capability} />}

          {error && <div className={styles.error}>{error}</div>}
        </div>
      </SideSheetBody>
      <SideSheetFooter>
        <Button variant="primary" disabled={saving || !name} onClick={() => void handleSave()}>
          Save
        </Button>
        {capability && (
          <Button variant="ghost" disabled={saving} onClick={() => void handleDelete()}>
            Delete
          </Button>
        )}
        <Button variant="ghost" onClick={onClose}>
          Cancel
        </Button>
      </SideSheetFooter>
    </SideSheet>
  );
}
