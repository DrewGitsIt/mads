import { useState } from "react";
import { Chip } from "../../components/Chip/Chip";
import { Button } from "../../components/Button/Button";
import { Panel, PanelBody } from "../../components/Panel/Panel";
import { Popover } from "../../components/Popover/Popover";
import type { Model, Session, SessionTrust, Usage } from "../../router/types";
import { useProviderModels } from "../../router/useProviderModels";
import { modelDisplayName } from "../../router/modelDisplay";
import { trustLevels } from "../../features/sessions/launchpad/pills/trustLevels";
import { ContextMeter } from "./ContextMeter";
import styles from "./ConversationHeader.module.css";

export interface ConversationHeaderProps {
  session: Session;
  usage?: Usage;
  contextWindow?: number;
  /** Active display modality. When set, the Chat/Terminal switch renders here. */
  mode?: "chat" | "terminal";
  onModeChange?: (mode: "chat" | "terminal") => void;
  onUpdateSession?: (patch: Partial<Pick<Session, "name" | "agent">>) => void;
  /**
   * Changes the session's model live (POST /sessions/{id}/model). Separate
   * from onUpdateSession for the same reason onChangeTrust is: this one
   * actually persists and reconfigures the running agent, rather than just
   * relabelling the pill. Absent → the pill still opens but selecting does
   * nothing (e.g. gallery renders).
   */
  onChangeModel?: (model: string) => void;
  /**
   * Changes the session's trust level live (POST /sessions/{id}/trust) —
   * the actionable half of the blocked-permission banner's "raise trust".
   * Absent → the pill renders as a static chip (e.g. gallery renders).
   */
  onChangeTrust?: (trust: SessionTrust) => void;
}

type PillField = "agent" | "model" | "trust";

/**
 * Conversation-tab meta strip: context meter + agent/model pills + workspace/
 * branch subline. The session name, activity chip, and lifecycle/tools kebab
 * live once in the SessionWorkspace bar above — this strip carries only what's
 * specific to the conversation, so nothing here duplicates the bar.
 */
export function ConversationHeader({
  session,
  usage,
  contextWindow,
  mode,
  onModeChange,
  onUpdateSession,
  onChangeModel,
  onChangeTrust,
}: ConversationHeaderProps) {
  const [openPill, setOpenPill] = useState<PillField | null>(null);
  const modelsByProvider = useProviderModels();

  // Narrowed to the free-text pills onUpdateSession still owns. Model and
  // trust are not among them: both persist through their own endpoint
  // (onChangeModel / onChangeTrust) rather than patching local state.
  function commitPill(field: "name" | "agent", value: string) {
    setOpenPill(null);
    const trimmed = value.trim();
    if (trimmed && trimmed !== session[field]) {
      onUpdateSession?.({ [field]: trimmed });
    }
  }

  return (
    <div className={styles.header}>
      <div className={styles.row}>
        <ContextMeter usage={usage} contextWindow={contextWindow} />

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
              if (id !== session.model) onChangeModel?.(id);
            }}
          />
        )}

        <TrustPickerPill
          trust={session.trust}
          open={openPill === "trust"}
          onOpen={() => setOpenPill("trust")}
          onClose={() => setOpenPill(null)}
          onSelect={
            onChangeTrust &&
            ((t) => {
              setOpenPill(null);
              if (t !== session.trust) onChangeTrust(t);
            })
          }
        />

        {mode && onModeChange && (
          <div className={styles.modeSwitch}>
            <Chip tone="neutral" pressed={mode === "chat"} onClick={() => onModeChange("chat")}>
              Chat
            </Chip>
            <Chip
              tone="neutral"
              pressed={mode === "terminal"}
              onClick={() => onModeChange("terminal")}
            >
              Terminal
            </Chip>
          </div>
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

interface TrustPickerPillProps {
  trust?: SessionTrust;
  open: boolean;
  onOpen: () => void;
  onClose: () => void;
  onSelect?: (trust: SessionTrust) => void;
}

/**
 * Shows the session's live trust level and switches it from a dropdown —
 * the affordance the blocked-permission banner's "raise trust" points at.
 * An unset trust renders as "limited": that's the backend's builtin default
 * (services/router/internal/api/sessiondefaults.go), so showing anything
 * else here would misreport what the spawned agent can actually do. Trust
 * is spawn-time-only in the runner, so selecting a level restarts the agent
 * process (the Router bounces a live child as part of the call).
 */
function TrustPickerPill({ trust, open, onOpen, onClose, onSelect }: TrustPickerPillProps) {
  const levels = trustLevels();
  const current = trust ?? "limited";
  const label = `trust: ${levels.find((l) => l.value === current)?.label ?? current}`;

  if (!onSelect) {
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
      <div className={styles.modelMenu} role="listbox" aria-label="Trust level">
        {levels.map((l) => (
          <button
            key={l.value}
            type="button"
            role="option"
            aria-selected={l.value === current}
            className={[styles.modelOption, l.value === current ? styles.modelOptionSelected : ""]
              .filter(Boolean)
              .join(" ")}
            onClick={() => onSelect(l.value)}
          >
            {l.label}
          </button>
        ))}
      </div>
    </Popover>
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
