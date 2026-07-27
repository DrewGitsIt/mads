import { useState } from "react";
import { SessionDeleteConfirmDialog } from "../actions/SessionDeleteConfirmDialog";
import { SessionUnlinkConfirmDialog } from "../actions/SessionUnlinkConfirmDialog";
import { LinkChooserDialog } from "./LinkChooserDialog";
import { EMPTY_OPEN_TASK_BREAKDOWN, dotState, groupSessions, totalOpenTasks } from "./railGroups";
import type { OpenTaskBreakdown } from "./railGroups";
import { buildRailRows, computeRailLinks } from "./railLinks";
import { summaryTeaser } from "./summaryTeaser";
import { FLAGS } from "../../../featureFlags";
import { router, type RouterClient } from "../../../router/client";
import type { Link, Session } from "../../../router/types";
import styles from "./SessionRail.module.css";

export interface SessionRailProps {
  sessions: Session[];
  openTaskCounts: Record<string, OpenTaskBreakdown>;
  selectedId?: string;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  /** Close (pause) a running session — the terminate side of the resume/close pair. */
  onClose?: (id: string) => void;
  onDelete?: (id: string) => void;
  /** Linked sessions (flag-gated): every link touching a visible session, deduped. */
  links?: Link[];
  routerClient?: RouterClient;
  className?: string;
}

/** Link mode: anchor session (chosen by clicking a card's link action) plus every session tapped in after it. */
interface LinkModeState {
  anchorId: string;
  selectedIds: Set<string>;
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
  const [linkMode, setLinkMode] = useState<LinkModeState>();
  const [chooserOpen, setChooserOpen] = useState(false);
  const [unlinkTarget, setUnlinkTarget] = useState<{ linkId: string; name: string }>();

  const allSessionsById = new Map(sessions.map((s) => [s.id, s]));
  const graph = computeRailLinks(linkedSessionsEnabled ? links : []);
  const activeRows = buildRailRows(active, allSessionsById, graph);
  const recentRows = buildRailRows(recent, allSessionsById, graph);
  const anchorWorkspace = linkMode ? allSessionsById.get(linkMode.anchorId)?.workspace : undefined;

  function startLinkMode(id: string) {
    setLinkMode({ anchorId: id, selectedIds: new Set([id]) });
  }

  function toggleLinkSelection(id: string) {
    setLinkMode((prev) => {
      if (!prev) return prev;
      const next = new Set(prev.selectedIds);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return { ...prev, selectedIds: next };
    });
  }

  function cancelLinkMode() {
    setLinkMode(undefined);
  }

  return (
    <div className={[styles.rail, className].filter(Boolean).join(" ")} aria-label="Session list">
      {linkedSessionsEnabled && linkMode && (
        <div className={styles.linkBar}>
          <span className={styles.linkBarCount}>{linkMode.selectedIds.size} selected</span>
          <button
            type="button"
            className={styles.linkBarAction}
            disabled={linkMode.selectedIds.size < 2}
            onClick={() => setChooserOpen(true)}
          >
            Choose mode
          </button>
          <button type="button" className={styles.linkBarCancel} onClick={cancelLinkMode}>
            Cancel
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
              linkMode={linkMode}
              anchorWorkspace={anchorWorkspace}
              onStartLinkMode={startLinkMode}
              onToggleLinkSelection={toggleLinkSelection}
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
              linkMode={linkMode}
              anchorWorkspace={anchorWorkspace}
              onStartLinkMode={startLinkMode}
              onToggleLinkSelection={toggleLinkSelection}
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
      {linkedSessionsEnabled && chooserOpen && linkMode && (
        <LinkChooserDialog
          sessionIds={[...linkMode.selectedIds]}
          sessions={sessions}
          routerClient={routerClient}
          onClose={() => setChooserOpen(false)}
          onCreated={() => {
            setChooserOpen(false);
            setLinkMode(undefined);
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
  linkMode,
  anchorWorkspace,
  onStartLinkMode,
  onToggleLinkSelection,
}: {
  row: ReturnType<typeof buildRailRows>[number];
  openTaskCounts: Record<string, OpenTaskBreakdown>;
  selectedId?: string;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  onClose?: (id: string) => void;
  onDeleteRequest: (id: string) => void;
  onUnlinkRequest: (target: { linkId: string; name: string }) => void;
  linkedSessionsEnabled: boolean;
  linkMode?: LinkModeState;
  anchorWorkspace?: string;
  onStartLinkMode: (id: string) => void;
  onToggleLinkSelection: (id: string) => void;
}) {
  const isAnchor = linkMode?.anchorId === row.session.id;
  const isCrossWorkspace = Boolean(linkMode) && !isAnchor && row.session.workspace !== anchorWorkspace;
  return (
    <>
      <SessionRow
        session={row.session}
        openTaskBreakdown={openTaskCounts[row.session.id] ?? EMPTY_OPEN_TASK_BREAKDOWN}
        selected={row.session.id === selectedId}
        onSelect={onSelect}
        onResume={onResume}
        onClose={onClose}
        onDeleteRequest={onDeleteRequest}
        linkBadge={row.badge}
        linkedSessionsEnabled={linkedSessionsEnabled}
        linkModeActive={Boolean(linkMode)}
        isLinkAnchor={isAnchor}
        isLinkCrossWorkspace={isCrossWorkspace}
        isLinkSelected={linkMode?.selectedIds.has(row.session.id) ?? false}
        onStartLinkMode={() => onStartLinkMode(row.session.id)}
        onToggleLinkSelection={() => onToggleLinkSelection(row.session.id)}
      />
      {linkedSessionsEnabled &&
        row.subjects.map((sub) => (
          <SessionRow
            key={sub.session.id}
            session={sub.session}
            openTaskBreakdown={openTaskCounts[sub.session.id] ?? EMPTY_OPEN_TASK_BREAKDOWN}
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
            linkModeActive={false}
            isLinkAnchor={false}
            isLinkCrossWorkspace={false}
            isLinkSelected={false}
            onStartLinkMode={() => {}}
            onToggleLinkSelection={() => {}}
          />
        ))}
    </>
  );
}

function SessionRow({
  session,
  openTaskBreakdown,
  selected,
  onSelect,
  onResume,
  onClose,
  onDeleteRequest,
  linkBadge,
  nested,
  onUnlinkRequest,
  linkedSessionsEnabled,
  linkModeActive,
  isLinkAnchor,
  isLinkCrossWorkspace,
  isLinkSelected,
  onStartLinkMode,
  onToggleLinkSelection,
}: {
  session: Session;
  openTaskBreakdown: OpenTaskBreakdown;
  selected: boolean;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  onClose?: (id: string) => void;
  onDeleteRequest: (id: string) => void;
  linkBadge?: string;
  nested?: boolean;
  onUnlinkRequest?: () => void;
  linkedSessionsEnabled: boolean;
  linkModeActive: boolean;
  isLinkAnchor: boolean;
  isLinkCrossWorkspace: boolean;
  isLinkSelected: boolean;
  onStartLinkMode: () => void;
  onToggleLinkSelection: () => void;
}) {
  const dot = dotState(session, openTaskBreakdown);
  const name = session.name || "Untitled session";
  const isPaused = session.activity !== "active";
  const openTaskCount = totalOpenTasks(openTaskBreakdown);
  const hasMeta = openTaskCount > 0 || (linkedSessionsEnabled && Boolean(linkBadge));
  const showSummary = isPaused && Boolean(session.summary);

  // Clicking a card opens it read-only — no eager resume. A paused session
  // keeps its inactive state (so SessionSummaryCard shows the full summary)
  // and wakes transparently on the first composer send. Explicit resume is the
  // ▶ pre-warm button below (and the workspace kebab).
  function activate() {
    onSelect?.(session.id);
  }

  function activateOrToggle() {
    if (linkModeActive) {
      if (isLinkAnchor || isLinkCrossWorkspace) return;
      onToggleLinkSelection();
      return;
    }
    activate();
  }

  return (
    <div
      role="button"
      tabIndex={linkModeActive && isLinkCrossWorkspace ? -1 : 0}
      aria-disabled={linkModeActive && isLinkCrossWorkspace ? true : undefined}
      className={[
        styles.row,
        selected ? styles.selected : "",
        nested ? styles.nestedRow : "",
        linkModeActive && isLinkAnchor ? styles.linkAnchor : "",
        linkModeActive && isLinkCrossWorkspace ? styles.linkDimmed : "",
      ]
        .filter(Boolean)
        .join(" ")}
      onClick={activateOrToggle}
      onKeyDown={(e) => {
        // Ignore keydowns that bubbled up from a nested action button —
        // those are handled by the button itself and shouldn't also
        // re-trigger the row's own activate().
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          activateOrToggle();
        }
      }}
    >
      <div className={styles.rowTop}>
        <span className={[styles.dot, styles[`dot-${dot}`]].join(" ")} aria-hidden="true" />
        <span className={styles.name}>{name}</span>
      </div>
      {hasMeta && (
        <div className={styles.rowMeta}>
          {openTaskCount > 0 && (
            <span className={styles.workBreakdown}>
              {openTaskBreakdown.running > 0 && (
                <span className={styles.bucketRunning}>{openTaskBreakdown.running} running</span>
              )}
              {openTaskBreakdown.ready > 0 && (
                <span className={styles.bucketReady}>{openTaskBreakdown.ready} ready</span>
              )}
              {openTaskBreakdown.blocked > 0 && (
                <span className={styles.bucketBlocked}>{openTaskBreakdown.blocked} blocked</span>
              )}
            </span>
          )}
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
      {showSummary && <div className={styles.summary}>{summaryTeaser(session.summary!)}</div>}
      {linkModeActive ? (
        <div className={styles.linkModeIndicator}>
          {isLinkAnchor && <span className={styles.linkAnchorBadge}>Anchor</span>}
          {!isLinkAnchor && !isLinkCrossWorkspace && (
            <input
              type="checkbox"
              className={styles.linkCheckbox}
              checked={isLinkSelected}
              readOnly
              tabIndex={-1}
              aria-label={
                isLinkSelected ? `Remove ${name} from link selection` : `Add ${name} to link selection`
              }
            />
          )}
          {isLinkCrossWorkspace && <span className={styles.linkDimmedTag}>different workspace</span>}
        </div>
      ) : (
        <div className={styles.actionCluster} role="group" aria-label={`${name} actions`}>
          {isPaused && onResume && (
            <button
              type="button"
              className={styles.actionButton}
              title="Resume agent"
              aria-label={`Resume ${name}`}
              onClick={(e) => {
                e.stopPropagation();
                onResume(session.id);
              }}
            >
              ▶
            </button>
          )}
          {onClose && (
            // Always offered, regardless of the activity record: it's the
            // Supervisor's TerminateSession underneath, which is safe to call
            // in every state (kills a live child if one exists, or reconciles
            // an already-inactive record as a no-op) — the record can drift
            // from the real child state, so it must never be the thing that
            // hides the one control that recovers from that drift.
            <button
              type="button"
              className={styles.actionButton}
              title="Close (pause) agent"
              aria-label={`Close ${name}`}
              onClick={(e) => {
                e.stopPropagation();
                onClose(session.id);
              }}
            >
              ⏸
            </button>
          )}
          {linkedSessionsEnabled && !nested && (
            <button
              type="button"
              className={styles.actionButton}
              title="Link"
              aria-label={`Add ${name} to link selection`}
              onClick={(e) => {
                e.stopPropagation();
                onStartLinkMode();
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
      )}
    </div>
  );
}
