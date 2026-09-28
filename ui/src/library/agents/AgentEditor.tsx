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
import type { Agent, AgentCapabilities, AgentPermissions, Quadrant, ToolIntent } from "../../router/types";
import { PERMISSION_INTENT_DESCRIPTIONS, PERMISSION_INTENT_LABELS } from "../../permissions/intents";
import { EnrichmentActions } from "./EnrichmentActions";
import { ImportFromProviderDialog } from "./ImportFromProviderDialog";
import { ModelField } from "./ModelField";
import { NLGenerateBox } from "./NLGenerateBox";
import { PromptScaffold } from "./PromptScaffold";
import { QuadrantField } from "./QuadrantField";
import { ToolIntentChips } from "./ToolIntentChips";
import styles from "./AgentEditor.module.css";

// parseCsv / csv are no longer needed: the only free-text tool list this
// editor used to render (the deprecated concrete-name `capabilities.tools`)
// is now managed entirely by the ToolIntentChips boolean surface below, and
// the legacy string array lives in state so an existing record's value isn't
// clobbered on save without a UI for it.

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

/** SideSheet-hosted create/edit form for a single persona (agent-concept redesign §6). */
export function AgentEditor({ agent, onClose, onCreate, onUpdate, onDelete }: AgentEditorProps) {
  const { capabilities } = useCapabilities();
  const [name, setName] = useState(agent?.name ?? "");
  const [description, setDescription] = useState(agent?.description ?? "");
  const [prompt, setPrompt] = useState(agent?.prompt ?? "");
  const [quadrant, setQuadrant] = useState<Quadrant | undefined>(agent?.quadrant);
  const [tags, setTags] = useState<string[]>(agent?.tags ?? []);
  // tools: legacy free-text concrete tool names, retained so an existing record
  // doesn't lose that field on save; the text UI for it is gone (replace via
  // the Allow / Deny intent chips below — deny beats allow on every intent, and
  // empty is "allow all").
  const [tools, setTools] = useState<string[]>(agent?.capabilities?.tools ?? []);
  const [mcpServers, setMcpServers] = useState<string[]>(agent?.capabilities?.mcp_servers ?? []);
  const [skills, setSkills] = useState<string[]>(agent?.capabilities?.skills ?? []);
  const [model, setModel] = useState(agent?.preferences?.model ?? "");
  const [allowedPaths, setAllowedPaths] = useState<string[]>(
    agent?.permissions?.allowed_paths ?? [],
  );
  // Persona-level allow / deny over the closed ToolIntent vocabulary (redesign §2):
  // the boolean surface the user asked for. "Allow" = the persona may use that
  // intent; "Deny" = the persona may not, overriding any allow on the same
  // intent; the chips default to allow-all (empty allowlist/denylist) so a
  // persona that says nothing inherits the session's trust level.
  const [allowlist, setAllowlist] = useState<ToolIntent[]>(agent?.allowlist ?? []);
  const [denylist, setDenylist] = useState<ToolIntent[]>(agent?.denylist ?? []);
  const [showImport, setShowImport] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // EnrichmentActions may propose a legacy tools list — we still honor it so
  // the "suggest capabilities" flow doesn't silently drop the user's intent.
  function applyDraft(draft: Agent) {
    setName(draft.name);
    setDescription(draft.description ?? "");
    setPrompt(draft.prompt);
    setQuadrant(draft.quadrant);
    setTags(draft.tags ?? []);
    setTools(draft.capabilities?.tools ?? []);
    setMcpServers(draft.capabilities?.mcp_servers ?? []);
    setSkills(draft.capabilities?.skills ?? []);
    if (draft.allowlist) setAllowlist(draft.allowlist);
    if (draft.denylist) setDenylist(draft.denylist);
  }

  function applySuggestedCapabilities(suggested: AgentCapabilities) {
    setTools(suggested.tools ?? []);
    setMcpServers(suggested.mcp_servers ?? []);
    setSkills(suggested.skills ?? []);
  }

  async function handleSave() {
    setSaving(true);
    setError(null);
    const capabilitiesPayload: AgentCapabilities = {
      tools: tools.length ? tools : undefined,
      mcp_servers: mcpServers.length ? mcpServers : undefined,
      skills: skills.length ? skills : undefined,
    };
    const permissionsPayload: AgentPermissions = {
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
      // Persona-level allow / deny (redesign §2): the boolean surface.
      // Empty allowlist == allow-all; empty denylist == no deny. Deny wins
      // on overlap (Go EffectiveToolIntents enforces the same rule), so a
      // record that sets both for the same intent is still safe.
      allowlist: allowlist.length ? allowlist : undefined,
      denylist: denylist.length ? denylist : undefined,
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
      <SideSheetHeader>{agent ? `Edit ${agent.name}` : "New persona"}</SideSheetHeader>
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
            <p className={styles.sectionNote}>
              Named registry entries only — a skill is a document, an MCP server
              is a process/URL. Both are already harness-agnostic, so they stay
              on the persona as concrete names. (Concrete tool <em>names</em>
              don't belong here; use Allow / Deny intents below for that.)
            </p>
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

          <Section heading="Tool allowances">
            <p className={styles.sectionNote}>
              Boolean allow / deny over the closed set of <em>abstract</em>
              tool intents (harness-free by construction). <strong>Allow</strong> —
              the persona may use that intent. <strong>Deny</strong> — the persona
              must not; overrides any allow on the same intent. Leave a chip unset
              to <strong>allow</strong> (allow-all) so the persona inherits the
              session&rsquo;s trust level. Every intent maps to the concrete tool
              name(s) of whatever harness runs the session, so an intent is the
              same concept regardless of which provider you pick — you&rsquo;re
              describing <em>what</em>, not <em>how</em>.
            </p>
            <ToolIntentChips
              allowlist={allowlist}
              denylist={denylist}
              onChange={(allow, deny) => {
                setAllowlist(allow);
                setDenylist(deny);
              }}
            />
          </Section>

          <Section heading="Constraints">
            <p className={styles.sectionNote}>
              Hard boundaries you place on this persona. At runtime they combine
              with the session&rsquo;s trust choice — whichever is more
              restrictive applies.
            </p>

            <div className={styles.intentHeading}>
              <strong>{PERMISSION_INTENT_LABELS.path_scope}</strong>
              <span className={styles.fieldHint}>
                {PERMISSION_INTENT_DESCRIPTIONS.path_scope} Paths the persona&rsquo;s
                file reads and writes are limited to. Leave blank for no path
                scoping (the persona may read and write anywhere the harness
                permits).
              </span>
            </div>
            <ChipListField
              label="Allowed paths"
              value={allowedPaths}
              onChange={setAllowedPaths}
              placeholder="e.g. src/, docs/"
            />

            {/* deny_commands is the legacy concrete-name shape, replaced by the
             * `shell` intent chip: denying shell is a persona-level deny that
             * maps to the concrete Bash/Shell/run_command name at the harness.
             * read_only and require_approval remain session-trust-only today:
             * they compile only from the session&rsquo;s Trust field, not from
             * the persona, so we surface them as read-only notes here. */}
            <div className={styles.disabledIntent}>
              <strong>{PERMISSION_INTENT_LABELS.deny_commands}</strong>
              <span className={styles.fieldHint}>
                {PERMISSION_INTENT_DESCRIPTIONS.deny_commands} Expressed as a
                denied intent above, not as a list of command strings —
                the harness maps the intent to its own tool names.
              </span>
            </div>

            <div className={styles.disabledIntent}>
              <strong>{PERMISSION_INTENT_LABELS.read_only}</strong>
              <span className={styles.fieldHint}>
                {PERMISSION_INTENT_DESCRIPTIONS.read_only} Not yet a persona-level
                setting — set via the session&rsquo;s trust level when starting
                a session.
              </span>
            </div>

            <div className={styles.disabledIntent}>
              <strong>{PERMISSION_INTENT_LABELS.require_approval}</strong>
              <span className={styles.fieldHint}>
                {PERMISSION_INTENT_DESCRIPTIONS.require_approval} Not yet a
                persona-level setting — set via the session&rsquo;s trust level
                when starting a session.
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
