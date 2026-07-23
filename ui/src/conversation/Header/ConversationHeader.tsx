import { useState } from "react";
import { Chip } from "../../components/Chip/Chip";
import { Button } from "../../components/Button/Button";
import { Panel, PanelBody } from "../../components/Panel/Panel";
import { Popover } from "../../components/Popover/Popover";
import type { Model, Session } from "../../router/types";
import { useProviderModels } from "../../router/useProviderModels";
import { modelDisplayName } from "../../router/modelDisplay";
import { ContextMeter } from "./ContextMeter";
import styles from "./ConversationHeader.module.css";

export interface ConversationHeaderProps {
  session: Session;
  usedTokens?: number;
  contextWindow?: number;
  onUpdateSession?: (patch: Partial<Pick<Session, "name" | "agent" | "model">>) => void;
}

type PillField = "agent" | "model";

/**
 * Conversation-tab meta strip: context meter + agent/model pills + workspace/
 * branch subline. The session name, activity chip, and lifecycle/tools kebab
 * live once in the SessionWorkspace bar above — this strip carries only what's
 * specific to the conversation, so nothing here duplicates the bar.
 */
export function ConversationHeader({
  session,
  usedTokens,
  contextWindow,
  onUpdateSession,
}: ConversationHeaderProps) {
  const [openPill, setOpenPill] = useState<PillField | null>(null);
  const modelsByProvider = useProviderModels();

  function commitPill(field: PillField, value: string) {
    setOpenPill(null);
    const trimmed = value.trim();
    if (trimmed && trimmed !== session[field]) {
      onUpdateSession?.({ [field]: trimmed });
    }
  }

  return (
    <div className={styles.header}>
      <div className={styles.row}>
        <ContextMeter usedTokens={usedTokens} contextWindow={contextWindow} />

        {session.agent && (
          <PillPopover
            value={session.agent}
            open={openPill === "agent"}
            onOpen={() => setOpenPill("agent")}
            onCommit={(v) => commitPill("agent", v)}
            onClose={() => setOpenPill(null)}
          />
        )}
        {session.model && (
          <ModelPickerPill
            provider={session.provider}
            model={session.model}
            models={modelsByProvider[session.provider ?? ""] ?? []}
            open={openPill === "model"}
            onOpen={() => setOpenPill("model")}
            onClose={() => setOpenPill(null)}
            onSelect={(id) => {
              setOpenPill(null);
              if (id !== session.model) onUpdateSession?.({ model: id });
            }}
          />
        )}
      </div>

      {(session.workspace || session.worktree_branch) && (
        <div className={styles.subline}>
          {[session.workspace, session.worktree_branch].filter(Boolean).join(" · ")}
        </div>
      )}
    </div>
  );
}

interface PillPopoverProps {
  value: string;
  label?: string;
  open: boolean;
  onOpen: () => void;
  onCommit: (value: string) => void;
  onClose: () => void;
}

/**
 * Plain editable popover per HANDOFF.md §4 non-goals — the provider-grouped
 * model picker is W6.2 Launchpad's job, not rebuilt here. `value` is the raw
 * wire value (seeds editing and is what gets committed); `label` is an
 * optional display override for the closed chip only — editing always
 * starts from `value`, never from a resolved display string, so committing
 * without changes can't corrupt the field with a display name.
 */
function PillPopover({ value: rawValue, label, open, onOpen, onCommit, onClose }: PillPopoverProps) {
  const [value, setValue] = useState(rawValue);

  return (
    <div className={styles.pillWrap}>
      <Chip
        tone="info"
        onClick={() => {
          setValue(rawValue);
          onOpen();
        }}
      >
        {label ?? rawValue}
      </Chip>
      {open && (
        <Panel className={styles.pillMenu}>
          <PanelBody>
            <input
              className={styles.pillInput}
              value={value}
              autoFocus
              onChange={(e) => setValue(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") onCommit(value);
                if (e.key === "Escape") onClose();
              }}
            />
            <Button variant="primary" size="sm" onClick={() => onCommit(value)}>
              Save
            </Button>
          </PanelBody>
        </Panel>
      )}
    </div>
  );
}

interface ModelPickerPillProps {
  provider?: string;
  model: string;
  models: Model[];
  open: boolean;
  onOpen: () => void;
  onClose: () => void;
  onSelect: (id: string) => void;
}

/**
 * Switch models mid-conversation from a dropdown of the session provider's own
 * models — not a free-text field. With no discovered models (provider Models()
 * empty/failed) it degrades to a static chip rather than an empty menu.
 */
function ModelPickerPill({ provider, model, models, open, onOpen, onClose, onSelect }: ModelPickerPillProps) {
  const label = modelDisplayName(model, models) ?? model;

  if (models.length === 0) {
    return <Chip tone="info">{label}</Chip>;
  }

  return (
    <Popover
      open={open}
      onClose={onClose}
      anchor={
        <button
          type="button"
          className={styles.modelTrigger}
          aria-haspopup="listbox"
          aria-expanded={open}
          onClick={() => (open ? onClose() : onOpen())}
        >
          {label}
          <span className={styles.caret} aria-hidden="true">▾</span>
        </button>
      }
    >
      <div className={styles.modelMenu} role="listbox">
        {provider && <div className={styles.modelGroup}>{provider}</div>}
        {models.map((m) => (
          <button
            key={m.id}
            type="button"
            role="option"
            aria-selected={m.id === model}
            className={[styles.modelOption, m.id === model ? styles.modelOptionSelected : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={() => onSelect(m.id)}
          >
            {m.name}
          </button>
        ))}
      </div>
    </Popover>
  );
}
