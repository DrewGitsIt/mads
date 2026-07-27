import type { ReactNode } from "react";
import { useState } from "react";
import { Button } from "../../components/Button/Button";
import { CapabilityPickerField } from "../../components/CapabilityPickerField/CapabilityPickerField";
import { ChipListField } from "../../components/ChipListField/ChipListField";
import {
  SideSheet,
  SideSheetBody,
  SideSheetFooter,
  SideSheetHeader,
} from "../../components/SideSheet/SideSheet";
import { useCapabilities } from "../capabilities/useCapabilities";
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

function Section({ heading, children }: { heading: string; children: ReactNode }) {
  return (
    <section className={styles.section}>
      <h3 className={styles.sectionHeading}>{heading}</h3>
      {children}
    </section>
  );
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
  const { capabilities } = useCapabilities();
  const [name, setName] = useState(agent?.name ?? "");
  const [description, setDescription] = useState(agent?.description ?? "");
  const [prompt, setPrompt] = useState(agent?.prompt ?? "");
  const [quadrant, setQuadrant] = useState<Quadrant | undefined>(agent?.quadrant);
  const [tags, setTags] = useState<string[]>(agent?.tags ?? []);
  const [tools, setTools] = useState(csv(agent?.capabilities?.tools));
  const [mcpServers, setMcpServers] = useState<string[]>(agent?.capabilities?.mcp_servers ?? []);
  const [skills, setSkills] = useState<string[]>(agent?.capabilities?.skills ?? []);
  const [model, setModel] = useState(agent?.preferences?.model ?? "");
  const [showImport, setShowImport] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  function applyDraft(draft: Agent) {
    setName(draft.name);
    setDescription(draft.description ?? "");
    setPrompt(draft.prompt);
    setQuadrant(draft.quadrant);
    setTags(draft.tags ?? []);
    setTools(csv(draft.capabilities?.tools));
    setMcpServers(draft.capabilities?.mcp_servers ?? []);
    setSkills(draft.capabilities?.skills ?? []);
  }

  function applySuggestedCapabilities(suggested: AgentCapabilities) {
    setTools(csv(suggested.tools));
    setMcpServers(suggested.mcp_servers ?? []);
    setSkills(suggested.skills ?? []);
  }

  async function handleSave() {
    setSaving(true);
    setError(null);
    const capabilitiesPayload: AgentCapabilities = {
      tools: parseCsv(tools),
      mcp_servers: mcpServers.length ? mcpServers : undefined,
      skills: skills.length ? skills : undefined,
    };
    const payload: Partial<Agent> = {
      name,
      description: description || undefined,
      prompt,
      quadrant,
      tags: tags.length ? tags : undefined,
      capabilities: capabilitiesPayload,
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

          <Section heading="Identity">
            <label className={styles.field}>
              Name
              <input value={name} onChange={(e) => setName(e.target.value)} />
            </label>

            <label className={styles.field}>
              Description
              <input value={description} onChange={(e) => setDescription(e.target.value)} />
            </label>

            <ChipListField label="Tags" value={tags} onChange={setTags} placeholder="e.g. billing" />
          </Section>

          <Section heading="Persona">
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
          </Section>

          <Section heading="Behavior">
            <QuadrantField value={quadrant} onChange={setQuadrant} />
            <ModelField value={model} onChange={setModel} />
          </Section>

          <Section heading="Capabilities">
            <label className={styles.field}>
              Tools
              <input
                value={tools}
                onChange={(e) => setTools(e.target.value)}
                placeholder="e.g. bash, grep, web_search"
              />
            </label>
            <CapabilityPickerField
              label="Skills"
              type="skill"
              capabilities={capabilities}
              value={skills}
              onChange={setSkills}
            />
            <CapabilityPickerField
              label="MCP servers"
              type="mcp"
              capabilities={capabilities}
              value={mcpServers}
              onChange={setMcpServers}
            />
          </Section>

          {/* Permissions section (allowed_paths / denied_commands) lands here in a follow-up task. */}

          <Section heading="Import">
            <Button variant="ghost" onClick={() => setShowImport(true)}>
              Import from provider…
            </Button>
          </Section>

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
