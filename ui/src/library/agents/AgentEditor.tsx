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
import type {
  Agent,
  AgentCapabilities,
  AgentPermissions,
  Capability,
  CapabilitySuggestion,
  Quadrant,
} from "../../router/types";
import { PERMISSION_INTENT_DESCRIPTIONS, PERMISSION_INTENT_LABELS } from "../../permissions/intents";
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

/**
 * Splits skill / MCP server names into those the capability registry holds
 * and those it doesn't. The data service rejects a save that references an
 * unregistered name, so nothing proposed by a model may reach the form
 * without passing through here.
 */
export function partitionRegistered(
  names: string[] | undefined,
  type: Capability["type"],
  registry: Capability[],
): { kept: string[]; missing: string[] } {
  const known = new Set(registry.filter((c) => c.type === type).map((c) => c.name));
  const kept: string[] = [];
  const missing: string[] = [];
  for (const name of names ?? []) (known.has(name) ? kept : missing).push(name);
  return { kept, missing };
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
  const { capabilities, loading: capabilitiesLoading } = useCapabilities();
  const [name, setName] = useState(agent?.name ?? "");
  const [description, setDescription] = useState(agent?.description ?? "");
  const [prompt, setPrompt] = useState(agent?.prompt ?? "");
  const [quadrant, setQuadrant] = useState<Quadrant | undefined>(agent?.quadrant);
  const [tags, setTags] = useState<string[]>(agent?.tags ?? []);
  const [tools, setTools] = useState(csv(agent?.capabilities?.tools));
  const [mcpServers, setMcpServers] = useState<string[]>(agent?.capabilities?.mcp_servers ?? []);
  const [skills, setSkills] = useState<string[]>(agent?.capabilities?.skills ?? []);
  const [model, setModel] = useState(agent?.preferences?.model ?? "");
  const [deniedCommands, setDeniedCommands] = useState<string[]>(
    agent?.permissions?.denied_commands ?? [],
  );
  const [allowedPaths, setAllowedPaths] = useState<string[]>(
    agent?.permissions?.allowed_paths ?? [],
  );
  const [showImport, setShowImport] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [skipped, setSkipped] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);

  // Applies model-proposed skills / MCP servers, keeping only registered
  // names. The server grounds these too; this catches a registry that changed
  // since, and names what was skipped instead of failing later on save.
  function applyRegistered(
    proposedSkills: string[] | undefined,
    proposedMcp: string[] | undefined,
    alreadyDropped: string[] = [],
  ) {
    // Until the registry has loaded there's nothing to check against; the
    // server-side grounding is the guard in that window.
    const registry = capabilitiesLoading ? null : capabilities;
    const s = registry
      ? partitionRegistered(proposedSkills, "skill", registry)
      : { kept: proposedSkills ?? [], missing: [] };
    const m = registry
      ? partitionRegistered(proposedMcp, "mcp", registry)
      : { kept: proposedMcp ?? [], missing: [] };
    setSkills(s.kept);
    setMcpServers(m.kept);
    setSkipped([...alreadyDropped, ...s.missing, ...m.missing]);
  }

  function applyDraft(draft: Agent) {
    setName(draft.name);
    setDescription(draft.description ?? "");
    setPrompt(draft.prompt);
    setQuadrant(draft.quadrant);
    setTags(draft.tags ?? []);
    setTools(csv(draft.capabilities?.tools));
    applyRegistered(draft.capabilities?.skills, draft.capabilities?.mcp_servers);
  }

  function applySuggestedCapabilities(suggested: CapabilitySuggestion) {
    setTools(csv(suggested.tools));
    applyRegistered(suggested.skills, suggested.mcp_servers, suggested.dropped);
  }

  async function handleSave() {
    if (!capabilitiesLoading) {
      const missing = [
        ...partitionRegistered(skills, "skill", capabilities).missing,
        ...partitionRegistered(mcpServers, "mcp", capabilities).missing,
      ];
      if (missing.length > 0) {
        setError(`Not in the capability registry: ${missing.join(", ")}. Remove them or register them first.`);
        return;
      }
    }
    setSaving(true);
    setError(null);
    const capabilitiesPayload: AgentCapabilities = {
      tools: parseCsv(tools),
      mcp_servers: mcpServers.length ? mcpServers : undefined,
      skills: skills.length ? skills : undefined,
    };
    const permissionsPayload: AgentPermissions = {
      denied_commands: deniedCommands.length ? deniedCommands : undefined,
      allowed_paths: allowedPaths.length ? allowedPaths : undefined,
    };
    const payload: Partial<Agent> = {
      name,
      description: description || undefined,
      prompt,
      quadrant,
      tags: tags.length ? tags : undefined,
      capabilities: capabilitiesPayload,
      permissions: permissionsPayload,
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
            {skipped.length > 0 && (
              <p className={styles.sectionNote} role="status">
                Skipped suggestions not in the capability registry: {skipped.join(", ")}
              </p>
            )}
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

          <Section heading="Permissions">
            <p className={styles.sectionNote}>
              Constraints you place on this agent. At runtime they combine with the
              session&rsquo;s trust choice — whichever is more restrictive applies.
            </p>

            <div className={styles.intentHeading}>
              <strong>{PERMISSION_INTENT_LABELS.deny_commands}</strong>
              <span className={styles.fieldHint}>
                {PERMISSION_INTENT_DESCRIPTIONS.deny_commands}
              </span>
            </div>
            <ChipListField
              label="Denied command patterns"
              value={deniedCommands}
              onChange={setDeniedCommands}
              placeholder="e.g. rm, git push --force"
            />
            {deniedCommands.length > 0 && (
              <p className={styles.enforcementNote}>
                Enforcement depends on the provider chosen when starting a session — see
                Settings → Providers.
              </p>
            )}

            <div className={styles.intentHeading}>
              <strong>{PERMISSION_INTENT_LABELS.path_scope}</strong>
              <span className={styles.fieldHint}>
                {PERMISSION_INTENT_DESCRIPTIONS.path_scope}
              </span>
            </div>
            <ChipListField
              label="Allowed paths"
              value={allowedPaths}
              onChange={setAllowedPaths}
              placeholder="e.g. src/, docs/"
            />
            {allowedPaths.length > 0 && (
              <p className={styles.enforcementNote}>
                Enforcement depends on the provider chosen when starting a session — see
                Settings → Providers.
              </p>
            )}

            <div className={styles.disabledIntent}>
              <strong>{PERMISSION_INTENT_LABELS.read_only}</strong>
              <span className={styles.fieldHint}>
                {PERMISSION_INTENT_DESCRIPTIONS.read_only} Not yet an agent-level setting —
                set via the session&rsquo;s trust level when starting a session.
              </span>
            </div>

            <div className={styles.disabledIntent}>
              <strong>{PERMISSION_INTENT_LABELS.require_approval}</strong>
              <span className={styles.fieldHint}>
                {PERMISSION_INTENT_DESCRIPTIONS.require_approval} Not yet an agent-level
                setting — set via the session&rsquo;s trust level when starting a session.
              </span>
            </div>
          </Section>

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
