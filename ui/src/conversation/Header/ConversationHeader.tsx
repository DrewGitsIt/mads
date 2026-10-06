import { useState } from "react";
import { Chip } from "../../components/Chip/Chip";
import { Button } from "../../components/Button/Button";
import { Panel, PanelBody } from "../../components/Panel/Panel";
import { Popover } from "../../components/Popover/Popover";
import type {
  Model,
  ProviderBehavior,
  ProviderSupport,
  Session,
  SessionTrust,
} from "../../router/types";
import { useProviderModels } from "../../router/useProviderModels";
import { useProviderInfos } from "../../router/useProviderInfos";
import { modelDisplayName } from "../../router/modelDisplay";
import { UNKNOWN_AGENT, agentNameForRef } from "../../router/agentRef";
import type { AgentIndex } from "../../router/agentRef";
import { sessionLocation } from "../../router/sessionLocation";
import { trustLabel, trustLevels } from "../../features/sessions/launchpad/pills/trustLevels";
import { WorkControl } from "../Worktree/WorkControl";
import { isIsolated } from "../Worktree/workState";
import styles from "./ConversationHeader.module.css";

export interface ConversationHeaderProps {
  session: Session;
  /**
   * The session's workspace *name*, resolved by the caller (useSessionWorkspace
   * -> router/workspaceRef.ts). Session.workspace itself is an id-or-name
   * reference, so it is not printable: unresolved, the subline shows no
   * workspace rather than a `wsp_...` id.
   */
  workspaceName?: string;
  /**
   * Personas by id and name (router/useAgentIndex.ts), so the persona pill
   * names the persona rather than printing the `agt_…` reference the record
   * stores. Undefined while loading.
   */
  agents?: AgentIndex;
  /**
   * The checkout's open branch for a session working in place
   * (useCheckoutBranch) — an isolated session's branch is on its record.
   */
  checkoutBranch?: string;
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
  /**
   * Commit and Land (docs/SESSION-WORKTREES-DESIGN.md D4): the state-driven
   * control at the row's right end, before Chat/Terminal. Absent → no
   * control (e.g. gallery renders).
   */
  work?: {
    onCommit: () => void;
    onLand: () => void;
    landing?: boolean;
    committing?: boolean;
  };
}

type PillField = "agent" | "model" | "trust";

/**
 * Conversation-tab meta strip: persona/model/trust pills + a `location /
 * branch` subline. The session name and lifecycle/tools kebab live once in
 * the SessionWorkspace bar above, and the context meter sits in the composer
 * — this strip carries only what's specific to the conversation.
 */
export function ConversationHeader({
  session,
  workspaceName,
  agents,
  checkoutBranch,
  mode,
  onModeChange,
  onUpdateSession,
  onChangeModel,
  onChangeTrust,
  work,
}: ConversationHeaderProps) {
  const [openPill, setOpenPill] = useState<PillField | null>(null);
  const modelsByProvider = useProviderModels();
  const providerInfos = useProviderInfos();

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
        {session.agent && (
          <PillPopover
            value={session.agent}
            label={personaLabel(session.agent, agents)}
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
          effectiveTrust={session.effective_trust}
          provider={session.provider}
          matrix={providerInfos.find((p) => p.name === session.provider)?.matrix}
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

        <div className={styles.end}>
          {work && (
            <WorkControl
              session={session}
              onCommit={work.onCommit}
              onLand={work.onLand}
              landing={work.landing}
              committing={work.committing}
            />
          )}
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
      </div>

      <Subline session={session} workspaceName={workspaceName} checkoutBranch={checkoutBranch} />
    </div>
  );
}

/**
 * The persona pill's text: the persona's name, never the stored `agt_…` id.
 * While the persona list is still loading an id reads as a neutral "Agent"
 * rather than flashing "Unknown agent" at something about to resolve.
 */
function personaLabel(ref: string, agents: AgentIndex | undefined): string {
  return agentNameForRef(ref, agents ?? new Map()) ?? (agents ? UNKNOWN_AGENT : "Agent");
}

/**
 * `{workspace or folder} / {branch}` — the same location rule as the rail
 * card (router/sessionLocation.ts). An isolated session's branch carries the
 * base it lands into; one working in place shows the checkout's branch when
 * the caller could read it. Hover gives the full folder, and for an isolated
 * session the worktree the agent actually runs in.
 */
function Subline({
  session,
  workspaceName,
  checkoutBranch,
}: Pick<ConversationHeaderProps, "session" | "workspaceName" | "checkoutBranch">) {
  const where = sessionLocation(session, workspaceName);
  const branch = isIsolated(session)
    ? `${session.worktree_branch} → ${session.worktree_base}`
    : session.worktree_branch || checkoutBranch;
  if (!where.name && !branch) return null;
  const title = [
    where.folder && `Folder: ${where.folder}`,
    where.worktree && `Worktree: ${where.worktree}`,
  ]
    .filter(Boolean)
    .join("\n");
  return (
    <div className={styles.subline} title={title || undefined} data-testid="conversation-subline">
      {where.name && <span className={styles.location}>{where.name}</span>}
      {where.name && branch && (
        // Spaced in the text too, so a copied subline reads "mads / main".
        <span className={styles.sublineSep}>{" / "}</span>
      )}
      {branch && <span className={styles.branch}>{branch}</span>}
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
      <button
        type="button"
        className={styles.pill}
        aria-haspopup="dialog"
        aria-expanded={open}
        title="Agent"
        onClick={() => {
          setValue(rawValue);
          onOpen();
        }}
      >
        {label ?? rawValue}
        <span className={styles.caret} aria-hidden="true">▾</span>
      </button>
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
  /** Session.effective_trust: what the session really runs at, when not `trust`. */
  effectiveTrust?: SessionTrust;
  provider?: string;
  /** The session's provider's capability matrix, once loaded — marks Auto-review's fallback. */
  matrix?: Record<ProviderBehavior, ProviderSupport>;
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
 *
 * A level the provider can't run is never shown as if it were running: the
 * Router reports the fallback on the record (effective_trust — auto_review
 * on a harness with no reviewer runs as limited), and the pill reads
 * "Trust: Auto-review → Limited" with the reason on hover. The menu marks
 * the same option "(runs as Limited on <provider>)" before it's picked,
 * from the provider's own matrix.
 */
function TrustPickerPill({
  trust,
  effectiveTrust,
  provider,
  matrix,
  open,
  onOpen,
  onClose,
  onSelect,
}: TrustPickerPillProps) {
  const levels = trustLevels(matrix, provider);
  const current = trust ?? "limited";
  const fallback = effectiveTrust && effectiveTrust !== current ? effectiveTrust : undefined;
  const asked = trustLabel(current);
  const label = fallback ? `Trust: ${asked} → ${trustLabel(fallback)}` : `Trust: ${asked}`;
  const title = fallback
    ? `${provider ?? "This provider"} doesn't support ${asked.toLowerCase()}; ` +
      `running as ${trustLabel(fallback)}`
    : undefined;

  if (!onSelect) {
    return (
      <span className={styles.pillStatic} title={title}>
        {label}
      </span>
    );
  }

  return (
    <Popover
      open={open}
      onClose={onClose}
      anchor={
        <button
          type="button"
          className={styles.pill}
          aria-haspopup="listbox"
          aria-expanded={open}
          title={title}
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
    return <span className={styles.pillStatic}>{label}</span>;
  }

  return (
    <Popover
      open={open}
      onClose={onClose}
      anchor={
        <button
          type="button"
          className={styles.pill}
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
