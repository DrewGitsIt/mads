import { useState } from "react";
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
              onDeleteRequest={setConfirmDeleteId}
              onUnlinkRequest={setUnlinkTarget}
              linkedSessionsEnabled={linkedSessionsEnabled}
              checkedIds={checkedIds}
              onToggleChecked={toggleChecked}
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
              onDeleteRequest={setConfirmDeleteId}
              onUnlinkRequest={setUnlinkTarget}
              linkedSessionsEnabled={linkedSessionsEnabled}
              checkedIds={checkedIds}
              onToggleChecked={toggleChecked}
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
  onDeleteRequest,
  onUnlinkRequest,
  linkedSessionsEnabled,
  checkedIds,
  onToggleChecked,
}: {
  row: ReturnType<typeof buildRailRows>[number];
  openTaskCounts: Record<string, number>;
  selectedId?: string;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  onDeleteRequest: (id: string) => void;
  onUnlinkRequest: (target: { linkId: string; name: string }) => void;
  linkedSessionsEnabled: boolean;
  checkedIds: Set<string>;
  onToggleChecked: (id: string) => void;
}) {
  return (
    <>
      <SessionRow
        session={row.session}
        openTaskCount={openTaskCounts[row.session.id] ?? 0}
        selected={row.session.id === selectedId}
        onSelect={onSelect}
        onResume={onResume}
        onDeleteRequest={onDeleteRequest}
        linkBadge={row.badge}
        linkedSessionsEnabled={linkedSessionsEnabled}
        linkChecked={checkedIds.has(row.session.id)}
        onToggleChecked={onToggleChecked}
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
            onDeleteRequest={onDeleteRequest}
            linkBadge={sub.badge}
            nested
            onUnlinkRequest={() =>
              onUnlinkRequest({ linkId: sub.linkId, name: sub.session.name || "Untitled session" })
            }
            linkedSessionsEnabled={linkedSessionsEnabled}
            linkChecked={checkedIds.has(sub.session.id)}
            onToggleChecked={onToggleChecked}
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
  onDeleteRequest,
  linkBadge,
  nested,
  onUnlinkRequest,
  linkedSessionsEnabled,
  linkChecked,
  onToggleChecked,
}: {
  session: Session;
  openTaskCount: number;
  selected: boolean;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  onDeleteRequest: (id: string) => void;
  linkBadge?: string;
  nested?: boolean;
  onUnlinkRequest?: () => void;
  linkedSessionsEnabled: boolean;
  linkChecked: boolean;
  onToggleChecked: (id: string) => void;
}) {
  const dot = dotState(session, openTaskCount);
  const name = session.name || "Untitled session";
  const isPaused = session.activity !== "active";
  const hasMeta = openTaskCount > 0 || (linkedSessionsEnabled && Boolean(linkBadge));
  const showSummary = isPaused && Boolean(session.summary);

  function activate() {
    if (isPaused && onResume) onResume(session.id);
    onSelect?.(session.id);
  }

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
      onClick={activate}
      onKeyDown={(e) => {
        // Ignore keydowns that bubbled up from a nested action button —
        // those are handled by the button itself and shouldn't also
        // re-trigger the row's own activate().
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          activate();
        }
      }}
    >
      <div className={styles.rowTop}>
        <span className={[styles.dot, styles[`dot-${dot}`]].join(" ")} aria-hidden="true" />
        <span className={styles.name}>{name}</span>
      </div>
      {hasMeta && (
        <div className={styles.rowMeta}>
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
        </div>
      )}
      {showSummary && <div className={styles.summary}>{session.summary}</div>}
      <div className={styles.actionCluster} role="group" aria-label={`${name} actions`}>
        {isPaused && onResume && (
          <button
            type="button"
            className={styles.actionButton}
            title="Resume"
            aria-label={`Resume ${name}`}
            onClick={(e) => {
              e.stopPropagation();
              onResume(session.id);
            }}
          >
            ▶
          </button>
        )}
        {linkedSessionsEnabled && !nested && (
          <button
            type="button"
            className={styles.actionButton}
            title="Link"
            aria-pressed={linkChecked}
            aria-label={
              linkChecked ? `Remove ${name} from link selection` : `Add ${name} to link selection`
            }
            onClick={(e) => {
              e.stopPropagation();
              onToggleChecked(session.id);
            }}
          >
            🔗
          </button>
        )}
        {linkedSessionsEnabled && nested && onUnlinkRequest && (
          <button
            type="button"
            className={styles.actionButton}
            title="Unlink"
            aria-label={`Unlink ${name}`}
            onClick={(e) => {
              e.stopPropagation();
              onUnlinkRequest();
            }}
          >
            ✕
          </button>
        )}
        <button
          type="button"
          className={[styles.actionButton, styles.actionButtonDanger].join(" ")}
          title="Archive"
          aria-label={`Archive ${name}`}
          onClick={(e) => {
            e.stopPropagation();
            onDeleteRequest(session.id);
          }}
        >
          🗑
        </button>
      </div>
    </div>
  );
}
