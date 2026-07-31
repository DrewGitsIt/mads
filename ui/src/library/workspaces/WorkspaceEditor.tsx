import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { CapabilityPickerField } from "../../components/CapabilityPickerField/CapabilityPickerField";
import {
  SideSheet,
  SideSheetBody,
  SideSheetFooter,
  SideSheetHeader,
} from "../../components/SideSheet/SideSheet";
import { useCapabilities } from "../capabilities/useCapabilities";
import { useTemplates } from "../templates/useTemplates";
import type { Workspace, WorkspaceRepo } from "../../router/types";
import { RepoList } from "./RepoList";
import { UsedBySessions } from "./UsedBySessions";
import styles from "./WorkspaceEditor.module.css";

export interface WorkspaceEditorProps {
  workspace: Workspace | null;
  onClose: () => void;
  onCreate: (workspace: Partial<Workspace>) => Promise<Workspace>;
  onUpdate: (workspace: Workspace) => Promise<Workspace>;
  onDelete: (id: string) => Promise<void>;
}

/** SideSheet-hosted create/edit form for a single workspace (MADS-UX §3.5). */
export function WorkspaceEditor({ workspace, onClose, onCreate, onUpdate, onDelete }: WorkspaceEditorProps) {
  const { templates } = useTemplates();
  const { capabilities } = useCapabilities();
  const [name, setName] = useState(workspace?.name ?? "");
  const [description, setDescription] = useState(workspace?.description ?? "");
  const [repos, setRepos] = useState<WorkspaceRepo[]>(workspace?.repos ?? []);
  const [workspaceCapabilities, setWorkspaceCapabilities] = useState<string[]>(
    workspace?.capabilities ?? [],
  );
  const [agent, setAgent] = useState(workspace?.defaults.agent ?? "");
  const [model, setModel] = useState(workspace?.defaults.model ?? "");
  const [isolation, setIsolation] = useState(workspace?.defaults.isolation ?? "");
  const [template, setTemplate] = useState(workspace?.defaults.template ?? "");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  async function handleSave() {
    setSaving(true);
    setError(null);
    const payload: Partial<Workspace> = {
      name,
      description: description || undefined,
      repos: repos.length ? repos : undefined,
      capabilities: workspaceCapabilities.length ? workspaceCapabilities : undefined,
      defaults: {
        agent: agent || undefined,
        model: model || undefined,
        isolation: isolation || undefined,
        template: template || undefined,
      },
    };
    try {
      if (workspace) {
        await onUpdate({ ...workspace, ...payload } as Workspace);
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
    if (!workspace) return;
    setSaving(true);
    try {
      await onDelete(workspace.id);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setSaving(false);
    }
  }

  return (
    <SideSheet width={480}>
      <SideSheetHeader>{workspace ? `Edit ${workspace.name}` : "New workspace"}</SideSheetHeader>
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

          <RepoList repos={repos} onChange={setRepos} />

          <label className={styles.field}>
            Default agent
            <input value={agent} onChange={(e) => setAgent(e.target.value)} />
          </label>

          <label className={styles.field}>
            Default model
            <input value={model} onChange={(e) => setModel(e.target.value)} />
          </label>

          <label className={styles.field}>
            Default isolation
            <select value={isolation} onChange={(e) => setIsolation(e.target.value)}>
              <option value="">—</option>
              <option value="current_dir">Current directory</option>
              <option value="worktree">Worktree</option>
            </select>
          </label>

          <label className={styles.field}>
            Default template
            <select value={template} onChange={(e) => setTemplate(e.target.value)}>
              <option value="">—</option>
              {templates.map((t) => (
                <option key={t.id} value={t.name}>
                  {t.name}
                </option>
              ))}
            </select>
          </label>

          <CapabilityPickerField
            label="Capabilities"
            capabilities={capabilities}
            value={workspaceCapabilities}
            onChange={setWorkspaceCapabilities}
          />

          {workspace && <UsedBySessions workspaceId={workspace.id} />}

          {error && <div className={styles.error}>{error}</div>}
        </div>
      </SideSheetBody>
      <SideSheetFooter>
        <Button variant="primary" disabled={saving || !name} onClick={() => void handleSave()}>
          Save
        </Button>
        {workspace && (
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
