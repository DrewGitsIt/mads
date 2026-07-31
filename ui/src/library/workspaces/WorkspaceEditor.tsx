import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { CapabilityPickerField } from "../../components/CapabilityPickerField/CapabilityPickerField";
import { Select, type SelectOption } from "../../components/Select/Select";
import {
  SideSheet,
  SideSheetBody,
  SideSheetFooter,
  SideSheetHeader,
} from "../../components/SideSheet/SideSheet";
import { useCapabilities } from "../capabilities/useCapabilities";
import { useTemplates } from "../templates/useTemplates";
import { router } from "../../router/client";
import { useProviderModels } from "../../router/useProviderModels";
import type { Agent, Workspace, WorkspaceRepo } from "../../router/types";
import { RepoList } from "./RepoList";
import { UsedBySessions } from "./UsedBySessions";
import styles from "./WorkspaceEditor.module.css";

const ISOLATION_OPTIONS: SelectOption[] = [
  { value: "current_dir", label: "Current directory" },
  { value: "worktree", label: "Worktree" },
];

/**
 * Prepends a "none" option and, if the stored value isn't in the live registry
 * (a retired agent/model, or one from a provider whose discovery failed),
 * surfaces it as its own option so editing a workspace never silently drops a
 * default the user can't see.
 */
function withNoneAndCurrent(options: SelectOption[], current: string): SelectOption[] {
  const withNone: SelectOption[] = [{ value: "", label: "—" }, ...options];
  if (current && !withNone.some((o) => o.value === current)) {
    withNone.push({ value: current, label: current });
  }
  return withNone;
}

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
  const [agents, setAgents] = useState<Agent[]>([]);
  const modelsByProvider = useProviderModels();
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

  useEffect(() => {
    router
      .listAgents()
      .then(setAgents)
      .catch(() => setAgents([]));
  }, []);

  const agentOptions = withNoneAndCurrent(
    agents.map((a) => ({ value: a.name, label: a.name })),
    agent,
  );
  const modelOptions = withNoneAndCurrent(
    Object.entries(modelsByProvider).flatMap(([provider, models]) =>
      (models ?? []).map((m) => ({ value: m.id, label: m.name, groupLabel: provider })),
    ),
    model,
  );
  const templateOptions = templates.map((t) => ({ value: t.name, label: t.name }));

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

          <div className={styles.field}>
            <label htmlFor="ws-agent">Default agent</label>
            <Select
              id="ws-agent"
              className={styles.control}
              value={agent}
              options={agentOptions}
              onChange={setAgent}
            />
          </div>

          <div className={styles.field}>
            <label htmlFor="ws-model">Default model</label>
            <Select
              id="ws-model"
              className={styles.control}
              value={model}
              options={modelOptions}
              onChange={setModel}
            />
          </div>

          <div className={styles.field}>
            <label htmlFor="ws-isolation">Default isolation</label>
            <Select
              id="ws-isolation"
              className={styles.control}
              value={isolation}
              options={[{ value: "", label: "—" }, ...ISOLATION_OPTIONS]}
              onChange={setIsolation}
            />
          </div>

          <div className={styles.field}>
            <label htmlFor="ws-template">Default template</label>
            <Select
              id="ws-template"
              className={styles.control}
              value={template}
              options={[{ value: "", label: "—" }, ...templateOptions]}
              onChange={setTemplate}
            />
          </div>

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
