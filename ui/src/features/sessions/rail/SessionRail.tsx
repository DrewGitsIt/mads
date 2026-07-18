import { useState } from "react";
import { SessionActionsMenu } from "../actions/SessionActionsMenu";
import { SessionDeleteConfirmDialog } from "../actions/SessionDeleteConfirmDialog";
import { SessionUnlinkConfirmDialog } from "../actions/SessionUnlinkConfirmDialog";
import { LinkChooserDialog } from "./LinkChooserDialog";
import { dotState, groupSessions } from "./railGroups";
import { buildRailRows, computeRailLinks } from "./railLinks";
import { FLAGS } from "../../../featureFlags";
import { router, type RouterClient } from "../../../router/client";
import type { Link, Session } from "../../../router/types";
import styles from "./SessionRail.module.css";

export interface SessionRailProps {
  sessions: Session[];
  openTaskCounts: Record<string, number>;
  selectedId?: string;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  onClose?: (id: string) => void;
  onDelete?: (id: string) => void;
  /** Linked sessions (flag-gated): every link touching a visible session, deduped. */
  links?: Link[];
  routerClient?: RouterClient;
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
  links = [],
  routerClient = router,
  className,
}: SessionRailProps) {
  const linkedSessionsEnabled = FLAGS.linkedSessions;
  const { active, recent } = groupSessions(sessions);
  const [confirmDeleteId, setConfirmDeleteId] = useState<string>();
  const [checkedIds, setCheckedIds] = useState<Set<string>>(new Set());
  const [chooserOpen, setChooserOpen] = useState(false);
  const [unlinkTarget, setUnlinkTarget] = useState<{ linkId: string; name: string }>();

  const allSessionsById = new Map(sessions.map((s) => [s.id, s]));
  const graph = computeRailLinks(linkedSessionsEnabled ? links : []);
  const activeRows = buildRailRows(active, allSessionsById, graph);
  const recentRows = buildRailRows(recent, allSessionsById, graph);

  function toggleChecked(id: string) {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  const selectionMode = linkedSessionsEnabled && checkedIds.size > 0;

  return (
    <div className={[styles.rail, className].filter(Boolean).join(" ")} aria-label="Session list">
      {linkedSessionsEnabled && checkedIds.size >= 2 && (
        <div className={styles.linkBar}>
          <button type="button" className={styles.linkBarAction} onClick={() => setChooserOpen(true)}>
            Link {checkedIds.size} sessions…
          </button>
          <button type="button" className={styles.linkBarClear} onClick={() => setCheckedIds(new Set())}>
            Clear
          </button>
        </div>
      )}
      {active.length > 0 && (
        <div className={styles.group}>
          <div className={styles.groupHeader}>
            Active <span className={styles.count}>· {active.length}</span>
          </div>
          {activeRows.map((row) => (
            <RailRowGroup
              key={row.session.id}
              row={row}
              openTaskCounts={openTaskCounts}
              selectedId={selectedId}
              onSelect={onSelect}
              onResume={onResume}
              onClose={onClose}
              onDeleteRequest={setConfirmDeleteId}
              onUnlinkRequest={setUnlinkTarget}
              linkedSessionsEnabled={linkedSessionsEnabled}
              checkedIds={checkedIds}
              onToggleChecked={toggleChecked}
              selectionMode={selectionMode}
            />
          ))}
        </div>
      )}
      {recent.length > 0 && (
        <div className={styles.group}>
          <div className={styles.groupHeader}>Recent</div>
          {recentRows.map((row) => (
            <RailRowGroup
              key={row.session.id}
              row={row}
              openTaskCounts={openTaskCounts}
              selectedId={selectedId}
              onSelect={onSelect}
              onResume={onResume}
              onClose={onClose}
              onDeleteRequest={setConfirmDeleteId}
              onUnlinkRequest={setUnlinkTarget}
              linkedSessionsEnabled={linkedSessionsEnabled}
              checkedIds={checkedIds}
              onToggleChecked={toggleChecked}
              selectionMode={selectionMode}
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
      {linkedSessionsEnabled && (
        <SessionUnlinkConfirmDialog
          open={unlinkTarget !== undefined}
          sessionName={unlinkTarget?.name ?? ""}
          onConfirm={() => {
            void routerClient.deleteLink(unlinkTarget!.linkId);
            setUnlinkTarget(undefined);
          }}
          onCancel={() => setUnlinkTarget(undefined)}
        />
      )}
      {linkedSessionsEnabled && chooserOpen && (
        <LinkChooserDialog
          sessionIds={[...checkedIds]}
          sessions={sessions}
          routerClient={routerClient}
          onClose={() => setChooserOpen(false)}
          onCreated={() => {
            setChooserOpen(false);
            setCheckedIds(new Set());
          }}
        />
      )}
    </div>
  );
}

function RailRowGroup({
  row,
  openTaskCounts,
  selectedId,
  onSelect,
  onResume,
  onClose,
  onDeleteRequest,
  onUnlinkRequest,
  linkedSessionsEnabled,
  checkedIds,
  onToggleChecked,
  selectionMode,
}: {
  row: ReturnType<typeof buildRailRows>[number];
  openTaskCounts: Record<string, number>;
  selectedId?: string;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  onClose?: (id: string) => void;
  onDeleteRequest: (id: string) => void;
  onUnlinkRequest: (target: { linkId: string; name: string }) => void;
  linkedSessionsEnabled: boolean;
  checkedIds: Set<string>;
  onToggleChecked: (id: string) => void;
  selectionMode: boolean;
}) {
  return (
    <>
      <SessionRow
        session={row.session}
        openTaskCount={openTaskCounts[row.session.id] ?? 0}
        selected={row.session.id === selectedId}
        onSelect={onSelect}
        onResume={onResume}
        onClose={onClose}
        onDeleteRequest={onDeleteRequest}
        linkBadge={row.badge}
        linkedSessionsEnabled={linkedSessionsEnabled}
        checked={checkedIds.has(row.session.id)}
        onToggleChecked={onToggleChecked}
        selectionMode={selectionMode}
      />
      {linkedSessionsEnabled &&
        row.subjects.map((sub) => (
          <SessionRow
            key={sub.session.id}
            session={sub.session}
            openTaskCount={openTaskCounts[sub.session.id] ?? 0}
            selected={sub.session.id === selectedId}
            onSelect={onSelect}
            onResume={onResume}
            onClose={onClose}
            onDeleteRequest={onDeleteRequest}
            linkBadge={sub.badge}
            nested
            onUnlinkRequest={() =>
              onUnlinkRequest({ linkId: sub.linkId, name: sub.session.name || "Untitled session" })
            }
            linkedSessionsEnabled={linkedSessionsEnabled}
            checked={checkedIds.has(sub.session.id)}
            onToggleChecked={onToggleChecked}
            selectionMode={selectionMode}
          />
        ))}
    </>
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
  linkBadge,
  nested,
  onUnlinkRequest,
  linkedSessionsEnabled,
  checked,
  onToggleChecked,
  selectionMode,
}: {
  session: Session;
  openTaskCount: number;
  selected: boolean;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  onClose?: (id: string) => void;
  onDeleteRequest: (id: string) => void;
  linkBadge?: string;
  nested?: boolean;
  onUnlinkRequest?: () => void;
  linkedSessionsEnabled: boolean;
  checked: boolean;
  onToggleChecked: (id: string) => void;
  selectionMode: boolean;
}) {
  const dot = dotState(session, openTaskCount);
  const [menuOpen, setMenuOpen] = useState(false);
  return (
    <div
      role="button"
      tabIndex={0}
      className={[
        styles.row,
        selected ? styles.selected : "",
        nested ? styles.nestedRow : "",
      ]
        .filter(Boolean)
        .join(" ")}
      onClick={() => onSelect?.(session.id)}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onSelect?.(session.id);
        }
      }}
    >
      {linkedSessionsEnabled && (
        <span
          className={[styles.checkboxWrap, selectionMode ? styles.checkboxVisible : ""]
            .filter(Boolean)
            .join(" ")}
          onClick={(e) => e.stopPropagation()}
        >
          <input
            type="checkbox"
            aria-label={`Select ${session.name || "session"}`}
            checked={checked}
            onChange={() => onToggleChecked(session.id)}
          />
        </span>
      )}
      <span className={[styles.dot, styles[`dot-${dot}`]].join(" ")} aria-hidden="true" />
      <span className={styles.name}>{session.name || "Untitled session"}</span>
      {openTaskCount > 0 && <span className={styles.openCount}>{openTaskCount} open</span>}
      {linkedSessionsEnabled && linkBadge && (
        <span
          className={[styles.linkBadge, linkBadge.startsWith("LEADER") ? styles.linkBadgeLead : ""]
            .filter(Boolean)
            .join(" ")}
        >
          {linkBadge}
        </span>
      )}
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
            onUnlinkRequest={
              onUnlinkRequest &&
              (() => {
                setMenuOpen(false);
                onUnlinkRequest!();
              })
            }
          />
        )}
      </div>
    </div>
  );
}
