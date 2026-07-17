import { useState } from "react";
import { SessionActionsMenu } from "../actions/SessionActionsMenu";
import { SessionDeleteConfirmDialog } from "../actions/SessionDeleteConfirmDialog";
import { dotState, groupSessions } from "./railGroups";
import type { Session } from "../../../router/types";
import styles from "./SessionRail.module.css";

export interface SessionRailProps {
  sessions: Session[];
  openTaskCounts: Record<string, number>;
  selectedId?: string;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  onClose?: (id: string) => void;
  onDelete?: (id: string) => void;
  className?: string;
}

export function SessionRail({
  sessions,
  openTaskCounts,
  selectedId,
  onSelect,
  onResume,
  onClose,
  onDelete,
  className,
}: SessionRailProps) {
  const { active, recent } = groupSessions(sessions);
  const [confirmDeleteId, setConfirmDeleteId] = useState<string>();

  return (
    <div className={[styles.rail, className].filter(Boolean).join(" ")} aria-label="Session list">
      {active.length > 0 && (
        <div className={styles.group}>
          <div className={styles.groupHeader}>
            Active <span className={styles.count}>· {active.length}</span>
          </div>
          {active.map((session) => (
            <SessionRow
              key={session.id}
              session={session}
              openTaskCount={openTaskCounts[session.id] ?? 0}
              selected={session.id === selectedId}
              onSelect={onSelect}
              onResume={onResume}
              onClose={onClose}
              onDeleteRequest={() => setConfirmDeleteId(session.id)}
            />
          ))}
        </div>
      )}
      {recent.length > 0 && (
        <div className={styles.group}>
          <div className={styles.groupHeader}>Recent</div>
          {recent.map((session) => (
            <SessionRow
              key={session.id}
              session={session}
              openTaskCount={openTaskCounts[session.id] ?? 0}
              selected={session.id === selectedId}
              onSelect={onSelect}
              onResume={onResume}
              onClose={onClose}
              onDeleteRequest={() => setConfirmDeleteId(session.id)}
            />
          ))}
        </div>
      )}
      <SessionDeleteConfirmDialog
        open={confirmDeleteId !== undefined}
        onConfirm={() => {
          onDelete?.(confirmDeleteId!);
          setConfirmDeleteId(undefined);
        }}
        onCancel={() => setConfirmDeleteId(undefined)}
      />
    </div>
  );
}

function SessionRow({
  session,
  openTaskCount,
  selected,
  onSelect,
  onResume,
  onClose,
  onDeleteRequest,
}: {
  session: Session;
  openTaskCount: number;
  selected: boolean;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  onClose?: (id: string) => void;
  onDeleteRequest: (id: string) => void;
}) {
  const dot = dotState(session, openTaskCount);
  const [menuOpen, setMenuOpen] = useState(false);
  return (
    <div
      role="button"
      tabIndex={0}
      className={[styles.row, selected ? styles.selected : ""].filter(Boolean).join(" ")}
      onClick={() => onSelect?.(session.id)}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onSelect?.(session.id);
        }
      }}
    >
      <span className={[styles.dot, styles[`dot-${dot}`]].join(" ")} aria-hidden="true" />
      <span className={styles.name}>{session.name || "Untitled session"}</span>
      {openTaskCount > 0 && <span className={styles.openCount}>{openTaskCount} open</span>}
      {session.activity !== "active" && onResume && (
        <button
          type="button"
          className={styles.resumeAffordance}
          aria-label={`Resume ${session.name || "session"}`}
          onClick={(e) => {
            e.stopPropagation();
            onResume(session.id);
          }}
        >
          Resume
        </button>
      )}
      <div className={styles.rowMenuWrap} onClick={(e) => e.stopPropagation()}>
        <button
          type="button"
          className={styles.rowMenuTrigger}
          aria-label={`${session.name || "Session"} actions`}
          onClick={() => setMenuOpen((x) => !x)}
        >
          ⋯
        </button>
        {menuOpen && (
          <SessionActionsMenu
            className={styles.rowMenu}
            activity={session.activity}
            onClose={() => {
              setMenuOpen(false);
              onClose?.(session.id);
            }}
            onResume={() => {
              setMenuOpen(false);
              onResume?.(session.id);
            }}
            onDeleteRequest={() => {
              setMenuOpen(false);
              onDeleteRequest(session.id);
            }}
          />
        )}
      </div>
    </div>
  );
}
