import { useState } from "react";
import { Chip } from "../../components/Chip/Chip";
import { Button } from "../../components/Button/Button";
import { Panel, PanelBody } from "../../components/Panel/Panel";
import type { Session } from "../../router/types";
import { ContextMeter } from "./ContextMeter";
import styles from "./ConversationHeader.module.css";

export interface ConversationHeaderProps {
  session: Session;
  usedTokens?: number;
  contextWindow?: number;
  shellOpen: boolean;
  onToggleShell: () => void;
  onRestartAgent: () => void;
  onUpdateSession?: (patch: Partial<Pick<Session, "name" | "agent" | "model">>) => void;
}

type PillField = "agent" | "model";

/**
 * Consolidated header per the task brief's narrowed scope — name/status/
 * pills/context meter/kebab only. Resume/Pause/Merge/Link/fork/delete from
 * the fuller MADS-UX §3.3 header are other tabs' work items (HANDOFF.md
 * §4). No "trust" pill: `PATCH /sessions/:id` accepts it but Session has no
 * such field on the frontend contract yet (mirrors pkg/contracts/session.go
 * exactly, see ui/src/router/types.ts) — same data-gap treatment as
 * ContextMeter's context-window size.
 */
export function ConversationHeader({
  session,
  usedTokens,
  contextWindow,
  shellOpen,
  onToggleShell,
  onRestartAgent,
  onUpdateSession,
}: ConversationHeaderProps) {
  const [editingName, setEditingName] = useState(false);
  const [name, setName] = useState(session.name);
  const [openPill, setOpenPill] = useState<PillField | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);

  function commitName() {
    setEditingName(false);
    const trimmed = name.trim();
    if (trimmed && trimmed !== session.name) {
      onUpdateSession?.({ name: trimmed });
    } else {
      setName(session.name);
    }
  }

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
        {editingName ? (
          <input
            className={styles.nameInput}
            value={name}
            autoFocus
            onChange={(e) => setName(e.target.value)}
            onBlur={commitName}
            onKeyDown={(e) => {
              if (e.key === "Enter") commitName();
              if (e.key === "Escape") {
                setName(session.name);
                setEditingName(false);
              }
            }}
          />
        ) : (
          <button
            type="button"
            className={styles.name}
            onClick={() => setEditingName(true)}
          >
            {session.name}
          </button>
        )}
        <Chip tone={session.activity === "active" ? "ok" : "neutral"}>
          {session.activity}
        </Chip>

        <div className={styles.spacer} />

        <ContextMeter usedTokens={usedTokens} contextWindow={contextWindow} />

        {session.agent && (
          <PillPopover
            label={session.agent}
            open={openPill === "agent"}
            onOpen={() => setOpenPill("agent")}
            onCommit={(v) => commitPill("agent", v)}
            onClose={() => setOpenPill(null)}
          />
        )}
        {session.model && (
          <PillPopover
            label={session.model}
            open={openPill === "model"}
            onOpen={() => setOpenPill("model")}
            onCommit={(v) => commitPill("model", v)}
            onClose={() => setOpenPill(null)}
          />
        )}

        <div className={styles.kebabWrap}>
          <Button
            variant="ghost"
            size="sm"
            aria-label="More actions"
            onClick={() => setMenuOpen((x) => !x)}
          >
            ⋮
          </Button>
          {menuOpen && (
            <Panel className={styles.menu}>
              <PanelBody>
                <ul className={styles.menuList}>
                  <li>
                    <button
                      type="button"
                      className={styles.menuItem}
                      onClick={() => {
                        setMenuOpen(false);
                        onToggleShell();
                      }}
                    >
                      {shellOpen ? "Hide Shell" : "Shell"}
                    </button>
                  </li>
                  <li>
                    <button
                      type="button"
                      className={styles.menuItem}
                      onClick={() => {
                        setMenuOpen(false);
                        onRestartAgent();
                      }}
                    >
                      Restart Agent
                    </button>
                  </li>
                </ul>
              </PanelBody>
            </Panel>
          )}
        </div>
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
  label: string;
  open: boolean;
  onOpen: () => void;
  onCommit: (value: string) => void;
  onClose: () => void;
}

/**
 * Plain editable popover per HANDOFF.md §4 non-goals — the provider-grouped
 * model picker is W6.2 Launchpad's job, not rebuilt here.
 */
function PillPopover({ label, open, onOpen, onCommit, onClose }: PillPopoverProps) {
  const [value, setValue] = useState(label);

  return (
    <div className={styles.pillWrap}>
      <Chip
        tone="info"
        onClick={() => {
          setValue(label);
          onOpen();
        }}
      >
        {label}
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
