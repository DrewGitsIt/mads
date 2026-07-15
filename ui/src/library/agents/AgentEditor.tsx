import { useState } from "react";
import { Button } from "../../components/Button/Button";
import {
  SideSheet,
  SideSheetBody,
  SideSheetFooter,
  SideSheetHeader,
} from "../../components/SideSheet/SideSheet";
import type { Agent, AgentCapabilities, Quadrant } from "../../router/types";
import { EnrichmentActions } from "./EnrichmentActions";
import { ImportFromProviderDialog } from "./ImportFromProviderDialog";
import { ModelField } from "./ModelField";
import { NLGenerateBox } from "./NLGenerateBox";
import { PromptScaffold } from "./PromptScaffold";
import { QuadrantField } from "./QuadrantField";
import styles from "./AgentEditor.module.css";

function csv(list?: string[]): string {
  return (list ?? []).join(", ");
}

function parseCsv(value: string): string[] | undefined {
  const items = value
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  return items.length ? items : undefined;
}

export interface AgentEditorProps {
  agent: Agent | null;
  onClose: () => void;
  onCreate: (agent: Partial<Agent>) => Promise<Agent>;
  onUpdate: (agent: Agent) => Promise<Agent>;
  onDelete: (id: string) => Promise<void>;
}

/** SideSheet-hosted create/edit form for a single agent (MADS-UX §3.5). */
export function AgentEditor({ agent, onClose, onCreate, onUpdate, onDelete }: AgentEditorProps) {
  const [name, setName] = useState(agent?.name ?? "");
  const [description, setDescription] = useState(agent?.description ?? "");
  const [prompt, setPrompt] = useState(agent?.prompt ?? "");
  const [quadrant, setQuadrant] = useState<Quadrant | undefined>(agent?.quadrant);
  const [tags, setTags] = useState(csv(agent?.tags));
  const [tools, setTools] = useState(csv(agent?.capabilities?.tools));
  const [mcpServers, setMcpServers] = useState(csv(agent?.capabilities?.mcp_servers));
  const [skills, setSkills] = useState(csv(agent?.capabilities?.skills));
  const [model, setModel] = useState(agent?.preferences?.model ?? "");
  const [showImport, setShowImport] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  function applyDraft(draft: Agent) {
    setName(draft.name);
    setDescription(draft.description ?? "");
    setPrompt(draft.prompt);
    setQuadrant(draft.quadrant);
    setTags(csv(draft.tags));
    setTools(csv(draft.capabilities?.tools));
    setMcpServers(csv(draft.capabilities?.mcp_servers));
    setSkills(csv(draft.capabilities?.skills));
  }

  function applySuggestedCapabilities(capabilities: AgentCapabilities) {
    setTools(csv(capabilities.tools));
    setMcpServers(csv(capabilities.mcp_servers));
    setSkills(csv(capabilities.skills));
  }

  async function handleSave() {
    setSaving(true);
    setError(null);
    const capabilities: AgentCapabilities = {
      tools: parseCsv(tools),
      mcp_servers: parseCsv(mcpServers),
      skills: parseCsv(skills),
    };
    const payload: Partial<Agent> = {
      name,
      description: description || undefined,
      prompt,
      quadrant,
      tags: parseCsv(tags),
      capabilities,
      preferences: model ? { model } : undefined,
    };
    try {
      if (agent) {
        await onUpdate({ ...agent, ...payload } as Agent);
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
    if (!agent) return;
    setSaving(true);
    try {
      await onDelete(agent.id);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setSaving(false);
    }
  }

  return (
    <SideSheet width={480}>
      <SideSheetHeader>{agent ? `Edit ${agent.name}` : "New agent"}</SideSheetHeader>
      <SideSheetBody>
        <div className={styles.form}>
          {!agent && <NLGenerateBox onApply={applyDraft} />}

          <label className={styles.field}>
            Name
            <input value={name} onChange={(e) => setName(e.target.value)} />
          </label>

          <label className={styles.field}>
            Description
            <input value={description} onChange={(e) => setDescription(e.target.value)} />
          </label>

          <PromptScaffold />
          <label className={styles.field}>
            Prompt
            <textarea
              className={styles.promptInput}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
            />
          </label>

          <EnrichmentActions
            prompt={prompt}
            onDescribe={setDescription}
            onSuggestCapabilities={applySuggestedCapabilities}
            onRefine={setPrompt}
          />

          <QuadrantField value={quadrant} onChange={setQuadrant} />
          <ModelField value={model} onChange={setModel} />

          <label className={styles.field}>
            Tags (comma-separated)
            <input value={tags} onChange={(e) => setTags(e.target.value)} />
          </label>
          <label className={styles.field}>
            Tools (comma-separated)
            <input value={tools} onChange={(e) => setTools(e.target.value)} />
          </label>
          <label className={styles.field}>
            MCP servers (comma-separated)
            <input value={mcpServers} onChange={(e) => setMcpServers(e.target.value)} />
          </label>
          <label className={styles.field}>
            Skills (comma-separated)
            <input value={skills} onChange={(e) => setSkills(e.target.value)} />
          </label>

          <Button variant="ghost" onClick={() => setShowImport(true)}>
            Import from provider…
          </Button>

          {error && <div className={styles.error}>{error}</div>}
        </div>
      </SideSheetBody>
      <SideSheetFooter>
        <Button variant="primary" disabled={saving || !name || !prompt} onClick={() => void handleSave()}>
          Save
        </Button>
        {agent && !agent.system && (
          <Button variant="ghost" disabled={saving} onClick={() => void handleDelete()}>
            Delete
          </Button>
        )}
        <Button variant="ghost" onClick={onClose}>
          Cancel
        </Button>
      </SideSheetFooter>
      {showImport && <ImportFromProviderDialog onClose={() => setShowImport(false)} />}
    </SideSheet>
  );
}
