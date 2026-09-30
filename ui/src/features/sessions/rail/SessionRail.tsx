import { useEffect, useRef, useState } from "react";
import type { CSSProperties } from "react";
import { formatShortcut } from "../../../platform/shortcut";
import { SessionDeleteConfirmDialog } from "../actions/SessionDeleteConfirmDialog";
import { SessionUnlinkConfirmDialog } from "../actions/SessionUnlinkConfirmDialog";
import { LinkHolderMenu } from "./LinkHolderMenu";
import type { LinkHolderSubject } from "./LinkHolderMenu";
import { LinkOptionsMenu } from "./LinkOptionsMenu";
import { LinkChooserDialog } from "./LinkChooserDialog";
import {
  EMPTY_AGENT_STATE,
  EMPTY_OPEN_TASK_BREAKDOWN,
  RAIL_FILTERS,
  filterBucketOf,
  isEarlier,
  mostUrgentBucket,
  statusOf,
  totalOpenTasks,
} from "./railGroups";
import type {
  OpenTaskBreakdown,
  RailFilter,
  RailFilterBucket,
  SessionAgentState,
} from "./railGroups";
import { buildRailRows, computeRailLinks } from "./railLinks";
import type { RailRow } from "./railLinks";
import {
  isEditableElement,
  matchReason,
  matchSession,
  parseSearchQuery,
  scopeSuggestions,
} from "./railSearch";
import type { MatchReason, SearchMatch } from "./railSearch";
import { useSettledRailOrder } from "./railOrder";
import {
  EMPTY_LABEL_CONTEXT,
  hasRailLabels,
  railLabelsFor,
  railTooltip,
  workspaceFacts,
} from "./railLabels";
import type { RailLabelContext, RailLabels } from "./railLabels";
import {
  EMPTY_WORKSPACE_INDEX,
  workspaceIndex,
  workspaceNameForRef,
} from "../../../router/workspaceRef";
import type { WorkspaceIndex } from "../../../router/workspaceRef";
import { summaryTeaser } from "./summaryTeaser";
import { FLAGS } from "../../../featureFlags";
import {
  formatAbsoluteStamp,
  formatElapsedStamp,
  formatRelativeStamp,
  useElapsedNow,
  useRelativeNow,
} from "../../../utils/relativeTime";
import { router, type RouterClient } from "../../../router/client";
import type {
  Grant,
  Link,
  LinkCadence,
  OrchestrateStatus,
  Session,
  Workspace,
} from "../../../router/types";
import styles from "./SessionRail.module.css";

export interface SessionRailProps {
  sessions: Session[];
  openTaskCounts: Record<string, OpenTaskBreakdown>;
  /** Live agent state per session (useSessionAgentState) — paints the status line, never the order. */
  agentStates?: Record<string, SessionAgentState>;
  selectedId?: string;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  /** Close (pause) a running session — the terminate side of the resume/close pair. */
  onClose?: (id: string) => void;
  onDelete?: (id: string) => void;
  /** Linked sessions (flag-gated): every link touching a visible session, deduped. */
  links?: Link[];
  /**
   * Workspace records, for the adaptive context labels — a workspace's
   * primary repo (which is what makes a cwd worth printing) and its default
   * persona (which is what makes an agent NOT worth printing). Matched by
   * name: Session.workspace is a name, not an id.
   */
  workspaces?: Workspace[];
  routerClient?: RouterClient;
  /**
   * Opaque stamp of an externally-driven search query, folded together with
   * the rail's own omni-search box. Any change to either is a quiet boundary:
   * the pending order applies at once, because a list the user just asked for
   * has nothing to hold still. (The filter chips below are NOT a boundary —
   * they narrow the order in place, they never re-rank it.)
   */
  filterKey?: string;
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
  agentStates = {},
  selectedId,
  onSelect,
  onResume,
  onClose,
  onDelete,
  links = [],
  workspaces = [],
  routerClient = router,
  filterKey,
  className,
}: SessionRailProps) {
  const linkedSessionsEnabled = FLAGS.linkedSessions;
  const [query, setQuery] = useState("");
  const railRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  // Order is settled, never live: see useSettledRailOrder for the contract.
  // The search box is a settle boundary in its own right, so its text rides
  // in the same opaque stamp as the caller's (NUL-joined, which neither can
  // contain, so two different pairs can never collide into one stamp).
  const { sessions: ordered, pendingMoves, settle, railHandlers } = useSettledRailOrder(
    sessions,
    selectedId,
    `${filterKey ?? ""}\u0000${query}`,
  );
  const now = useRelativeNow();
  const [filter, setFilter] = useState<RailFilter>("all");
  const [earlierOpen, setEarlierOpen] = useState(false);
  const [confirmDeleteId, setConfirmDeleteId] = useState<string>();
  const [linkMode, setLinkMode] = useState<LinkModeState>();
  const [chooserOpen, setChooserOpen] = useState(false);
  const [unlinkTarget, setUnlinkTarget] = useState<{ linkId: string; name: string }>();

  const allSessionsById = new Map(sessions.map((s) => [s.id, s]));
  const graph = computeRailLinks(linkedSessionsEnabled ? links : []);
  const allRows = buildRailRows(ordered, allSessionsById, graph);

  // Session.workspace is an id-or-name reference; one index resolves it for
  // every surface below it (search, labels, tooltip) rather than each guessing.
  const wsIndex = workspaceIndex(workspaces);

  // Search bypasses the view entirely: it runs over every row the rail holds,
  // including the ones folded into the collapsed "Earlier" tail and everything
  // below the fold. A search that only searched what was rendered would be
  // worse than useless.
  const terms = parseSearchQuery(query);
  const searching = terms.length > 0;
  const matchById = new Map<string, SearchMatch>();
  const ranked: { row: RailRow; rank: number; resting: number }[] = [];
  allRows.forEach((row, resting) => {
    let rank: number | undefined;
    // A holder answers for its nested subjects here too — a subject can only
    // be shown underneath the holder that owns it.
    for (const session of [row.session, ...row.subjects.map((sub) => sub.session)]) {
      const match = matchSession(session, terms, wsIndex);
      if (!match) continue;
      matchById.set(session.id, match);
      rank = rank === undefined ? match.rank : Math.min(rank, match.rank);
    }
    if (rank !== undefined) ranked.push({ row, rank, resting });
  });
  // The one sanctioned reorder: relevance while a query is live, because the
  // user asked for it by typing. `resting` is the settled index, so ties break
  // by recency and clearing the field drops straight back to it — the settled
  // order itself is never touched.
  const rows = searching
    ? [...ranked].sort((a, b) => a.rank - b.rank || a.resting - b.resting).map((r) => r.row)
    : allRows;
  const suggestions = scopeSuggestions(sessions, query, wsIndex);
  const anchorWorkspace = linkMode ? allSessionsById.get(linkMode.anchorId)?.workspace : undefined;

  function bucketOf(session: Session): RailFilterBucket {
    return filterBucketOf(
      statusOf(
        session,
        agentStates[session.id] ?? EMPTY_AGENT_STATE,
        openTaskCounts[session.id] ?? EMPTY_OPEN_TASK_BREAKDOWN,
      ),
    );
  }

  // A holder answers for the subjects nested under it: they ride along when it
  // survives a filter, so counting it as merely "idle" while a nested card is
  // parked would make "Needs you 1" click through to a list without it.
  const bucketByRowId = new Map<string, RailFilterBucket>(
    rows.map((row) => [
      row.session.id,
      mostUrgentBucket([row.session, ...row.subjects.map((s) => s.session)].map(bucketOf)),
    ]),
  );
  const counts: Record<RailFilter, number> = { all: rows.length, working: 0, needs: 0, idle: 0 };
  for (const bucket of bucketByRowId.values()) counts[bucket] += 1;

  // Narrowing only — the survivors keep the order they already had, which is
  // the whole reason a chip can do triage without a group's side effects.
  const visible =
    filter === "all" ? rows : rows.filter((row) => bucketByRowId.get(row.session.id) === filter);
  // Labels read the *visible* list, not the whole rail: a workspace name
  // every card repeats says nothing, so it only earns a card once the list
  // is mixed. Deliberately computed before the Earlier fold, so expanding
  // the tail cannot relabel the cards above it.
  const sessionsOf = (row: RailRow) =>
    linkedSessionsEnabled ? [row.session, ...row.subjects.map((s) => s.session)] : [row.session];
  const visibleWorkspaces = new Set<string>();
  for (const row of visible) {
    for (const session of sessionsOf(row)) {
      // Counted by resolved name: two records referring to one workspace, one
      // by id and one by name, are not two workspaces.
      const name = workspaceNameForRef(session.workspace, wsIndex);
      if (name) visibleWorkspaces.add(name);
    }
  }
  const facts = workspaceFacts(workspaces);
  // Whether the line exists at all is a question about the whole rail, not the
  // visible slice: answered off `visible` it would come and go as filters
  // narrow, and every surviving card would change height — the reflow the
  // reservation exists to prevent. So both inputs are rail-wide, and a filter
  // can only change *which* labels a card shows, never whether it has a line.
  const railWorkspaces = new Set<string>();
  for (const row of allRows) {
    for (const session of sessionsOf(row)) {
      const name = workspaceNameForRef(session.workspace, wsIndex);
      if (name) railWorkspaces.add(name);
    }
  }
  const railWide: RailLabelContext = {
    multiWorkspace: railWorkspaces.size > 1,
    lineShown: true,
    facts,
  };
  const labelContext: RailLabelContext = {
    multiWorkspace: visibleWorkspaces.size > 1,
    lineShown: allRows.some((row) =>
      sessionsOf(row).some((session) => hasRailLabels(railLabelsFor(session, railWide))),
    ),
    facts,
  };

  const head: RailRow[] = [];
  const earlier: RailRow[] = [];
  for (const row of visible) (isEarlier(row.session, now) ? earlier : head).push(row);
  // A tail needs a head. With nothing recent above it, the fold would hide the
  // entire rail behind one line, so the whole list just renders. While a query
  // is live there is no fold at all: every hit is a result.
  const listRows = searching ? visible : head.length > 0 ? head : earlier;
  const tailRows = searching || head.length === 0 ? [] : earlier;

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

  // ⌘K / Ctrl-K focuses the field. Capture phase on the document so the rail
  // claims the chord ahead of the command palette's own (bubble-phase)
  // document listener, and only when nothing else is taking the user's
  // typing: with the composer focused the rail keeps its hands off and the
  // chord falls through untouched.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (!(e.metaKey || e.ctrlKey) || e.key.toLowerCase() !== "k") return;
      const active = document.activeElement;
      if (active !== searchRef.current && isEditableElement(active)) return;
      e.preventDefault();
      e.stopPropagation();
      searchRef.current?.focus();
      searchRef.current?.select();
    }
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, []);

  function commitScope(token: string) {
    setQuery(token);
    searchRef.current?.focus();
  }

  function clearSearch() {
    setQuery("");
    searchRef.current?.focus();
  }

  // ↑/↓ walk the rendered results — the list IS the autosuggest, so the
  // cards themselves are the options and there is no dropdown to walk.
  function walkResults(event: React.KeyboardEvent<HTMLElement>) {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    const target = event.target as HTMLElement;
    const fromSearch = target === searchRef.current;
    if (!fromSearch && target.dataset?.railCard === undefined) return;
    const cards = [...(railRef.current?.querySelectorAll<HTMLElement>("[data-rail-card]") ?? [])];
    if (cards.length === 0) return;
    event.preventDefault();
    if (fromSearch) {
      if (event.key === "ArrowDown") cards[0].focus();
      return;
    }
    const next = cards.indexOf(target) + (event.key === "ArrowDown" ? 1 : -1);
    // Walking back off the top returns to the field the results came from.
    if (next < 0) searchRef.current?.focus();
    else if (next < cards.length) cards[next].focus();
  }

  function onSearchKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key === "Enter") {
      // Enter opens the top hit — the result the relevance order put first.
      const top = listRows[0];
      if (!top) return;
      event.preventDefault();
      onSelect?.(top.session.id);
      return;
    }
    if (event.key !== "Escape") return;
    // Escape clears, then blurs: a second press leaves the rail rather than
    // making the first press do two things at once.
    event.preventDefault();
    if (query !== "") setQuery("");
    else searchRef.current?.blur();
  }

  // The delete dialog's copy is about one session, so it needs the record and
  // not just the id — including for a row inside the collapsed "Earlier" tail
  // or nested under a link, which is why this reads the unfiltered list.
  const deleteTarget = sessions.find((s) => s.id === confirmDeleteId);

  // Shared by the main list and the "Earlier" tail — the two render identical
  // cards, they only differ in whether the fold is hiding them.
  const rowProps = {
    openTaskCounts,
    agentStates,
    matchById,
    wsIndex,
    selectedId,
    onSelect,
    onResume,
    onClose,
    onDeleteRequest: setConfirmDeleteId,
    onUnlinkRequest: setUnlinkTarget,
    linkedSessionsEnabled,
    linkMode,
    anchorWorkspace,
    onStartLinkMode: startLinkMode,
    onToggleLinkSelection: toggleLinkSelection,
    labelContext,
    routerClient,
  };

  return (
    <div
      ref={railRef}
      className={[styles.rail, className].filter(Boolean).join(" ")}
      aria-label="Session list"
      onKeyDown={walkResults}
      {...railHandlers}
    >
      {pendingMoves > 0 && (
        <button type="button" className={styles.pendingGhost} onClick={settle}>
          <span className={styles.pendingArrow} aria-hidden="true">
            ↑
          </span>
          {pendingMoves === 1 ? "1 session moved" : `${pendingMoves} sessions moved`}
          <span className={styles.pendingHint}>— click to reorder</span>
        </button>
      )}
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
      {/* One field, no workspace picker: it matches whatever you happen to
          remember — name, workspace, directory or branch. */}
      <div className={styles.searchWrap}>
        <div className={styles.searchField}>
          <span className={styles.searchIcon} aria-hidden="true">
            ⌕
          </span>
          <input
            ref={searchRef}
            type="text"
            className={styles.searchInput}
            value={query}
            placeholder="Search sessions…"
            aria-label="Search sessions"
            aria-keyshortcuts="Meta+k Control+k"
            autoComplete="off"
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onSearchKeyDown}
          />
          {query !== "" && (
            <button
              type="button"
              className={styles.searchClear}
              title="Clear"
              aria-label="Clear search"
              onClick={clearSearch}
            >
              ✕
            </button>
          )}
          <span className={styles.searchKbd} aria-hidden="true">
            {formatShortcut("Mod+K")}
          </span>
        </div>
        {suggestions.length > 0 && (
          <div className={styles.scopeChips} role="group" aria-label="Scope search">
            {suggestions.map((s) => (
              <button
                key={s.token}
                type="button"
                className={styles.scopeChip}
                onClick={() => commitScope(s.token)}
              >
                <span className={styles.scopeChipKey}>{s.field}:</span>
                {s.value}
                <span className={styles.scopeChipCount}>{s.count}</span>
              </button>
            ))}
          </div>
        )}
      </div>
      <div className={styles.filterBar} role="group" aria-label="Filter sessions">
        {RAIL_FILTERS.map(({ id, label }) => (
          <button
            key={id}
            type="button"
            className={[styles.chip, filter === id ? styles.chipOn : ""].filter(Boolean).join(" ")}
            aria-pressed={filter === id}
            onClick={() => setFilter(id)}
          >
            {label}
            <span className={styles.chipCount}>{counts[id]}</span>
          </button>
        ))}
      </div>
      <div className={styles.list}>
        {listRows.map((row) => (
          <RailRowGroup key={row.session.id} row={row} {...rowProps} />
        ))}
        {visible.length === 0 && (
          <div className={styles.emptyNote}>
            {searching
              ? `No session matches “${query.trim()}”. Searched name, workspace, directory and branch.`
              : "No sessions match this filter."}
          </div>
        )}
      </div>
      {tailRows.length > 0 && (
        <div className={styles.list}>
          <button
            type="button"
            className={styles.earlierToggle}
            aria-expanded={earlierOpen}
            onClick={() => setEarlierOpen((open) => !open)}
          >
            <span aria-hidden="true">{earlierOpen ? "▾" : "▸"}</span>
            Earlier
            <span className={styles.earlierCount}>· {tailRows.length}</span>
          </button>
          {earlierOpen &&
            tailRows.map((row) => <RailRowGroup key={row.session.id} row={row} {...rowProps} />)}
        </div>
      )}
      {deleteTarget && (
        <SessionDeleteConfirmDialog
          open
          session={deleteTarget}
          agentState={agentStates[deleteTarget.id]}
          openTaskBreakdown={openTaskCounts[deleteTarget.id]}
          onConfirm={() => {
            onDelete?.(deleteTarget.id);
            setConfirmDeleteId(undefined);
          }}
          onCancel={() => setConfirmDeleteId(undefined)}
        />
      )}
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
          links={links}
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
  agentStates,
  matchById,
  wsIndex,
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
  labelContext,
  routerClient,
}: {
  row: ReturnType<typeof buildRailRows>[number];
  openTaskCounts: Record<string, OpenTaskBreakdown>;
  agentStates: Record<string, SessionAgentState>;
  /** Only populated while a query is live — a card's own hit, for its reason line. */
  matchById: Map<string, SearchMatch>;
  /** Resolves a session's workspace reference to its workspace (router/workspaceRef.ts). */
  wsIndex: WorkspaceIndex;
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
  labelContext: RailLabelContext;
  routerClient: RouterClient;
}) {
  const isAnchor = linkMode?.anchorId === row.session.id;
  const isCrossWorkspace = Boolean(linkMode) && !isAnchor && row.session.workspace !== anchorWorkspace;
  return (
    <>
      <SessionRow
        session={row.session}
        openTaskBreakdown={openTaskCounts[row.session.id] ?? EMPTY_OPEN_TASK_BREAKDOWN}
        agentState={agentStates[row.session.id] ?? EMPTY_AGENT_STATE}
        matchReason={reasonFor(row.session, matchById, wsIndex)}
        selected={row.session.id === selectedId}
        onSelect={onSelect}
        onResume={onResume}
        onClose={onClose}
        onDeleteRequest={onDeleteRequest}
        linkBadge={row.badge}
        heldSubjects={row.subjects.map((sub) => ({
          name: sub.session.name || "Untitled session",
          badge: sub.badge,
          linkId: sub.linkId,
        }))}
        onUnlinkSubjectRequest={onUnlinkRequest}
        linkedSessionsEnabled={linkedSessionsEnabled}
        linkModeActive={Boolean(linkMode)}
        isLinkAnchor={isAnchor}
        isLinkCrossWorkspace={isCrossWorkspace}
        isLinkSelected={linkMode?.selectedIds.has(row.session.id) ?? false}
        labelContext={labelContext}
        onStartLinkMode={() => onStartLinkMode(row.session.id)}
        onToggleLinkSelection={() => onToggleLinkSelection(row.session.id)}
      />
      {linkedSessionsEnabled &&
        row.subjects.map((sub) => (
          <SessionRow
            key={sub.session.id}
            session={sub.session}
            openTaskBreakdown={openTaskCounts[sub.session.id] ?? EMPTY_OPEN_TASK_BREAKDOWN}
            agentState={agentStates[sub.session.id] ?? EMPTY_AGENT_STATE}
            matchReason={reasonFor(sub.session, matchById, wsIndex)}
            selected={sub.session.id === selectedId}
            onSelect={onSelect}
            onResume={onResume}
            onClose={onClose}
            onDeleteRequest={onDeleteRequest}
            linkBadge={sub.badge}
            orchestrateStatus={sub.orchestrateStatus}
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
            labelContext={labelContext}
            cadence={sub.cadence}
            grants={sub.grants}
            grantingSessionId={sub.grantingSessionId}
            linkId={sub.linkId}
            routerClient={routerClient}
          />
        ))}
    </>
  );
}

function reasonFor(
  session: Session,
  matchById: Map<string, SearchMatch>,
  wsIndex: WorkspaceIndex = EMPTY_WORKSPACE_INDEX,
) {
  const match = matchById.get(session.id);
  return match ? matchReason(session, match, wsIndex) : undefined;
}

/** Active renders nothing extra beyond the existing LINKED badge — only the non-terminal/degraded states need a pill. */
function orchestrateStatusLabel(status?: OrchestrateStatus): string | undefined {
  switch (status) {
    case "pending_restart":
      return "TOOLS ON RESTART";
    case "unsupported":
      return "NO TOOLS";
    default:
      return undefined;
  }
}

/**
 * Where the hover action cluster paints: just past the card's right edge,
 * centred on the row (.actionCluster carries the translateY(-50%)). Fixed,
 * not absolute — .rail is overflow-y:auto, and a scroll container clips the
 * cross axis too, so an absolutely positioned child could never escape
 * sideways. Same trick LinkOptionsMenu already plays on this row's badge.
 */
function clusterPlacement(row: HTMLElement): CSSProperties {
  const rect = row.getBoundingClientRect();
  return { top: rect.top + rect.height / 2, left: rect.right };
}

function SessionRow({
  session,
  openTaskBreakdown,
  agentState,
  matchReason: reason,
  selected,
  onSelect,
  onResume,
  onClose,
  onDeleteRequest,
  linkBadge,
  heldSubjects,
  onUnlinkSubjectRequest,
  orchestrateStatus,
  nested,
  onUnlinkRequest,
  linkedSessionsEnabled,
  linkModeActive,
  isLinkAnchor,
  isLinkCrossWorkspace,
  isLinkSelected,
  onStartLinkMode,
  onToggleLinkSelection,
  labelContext = EMPTY_LABEL_CONTEXT,
  cadence,
  grants,
  grantingSessionId,
  linkId,
  routerClient,
}: {
  session: Session;
  openTaskBreakdown: OpenTaskBreakdown;
  agentState: SessionAgentState;
  /** Set only when a live search matched this card on something other than its name. */
  matchReason?: MatchReason;
  selected: boolean;
  onSelect?: (id: string) => void;
  onResume?: (id: string) => void;
  onClose?: (id: string) => void;
  onDeleteRequest: (id: string) => void;
  linkBadge?: string;
  /**
   * Only set on a holder row — the subjects it holds, which is what turns its
   * badge from a bare pill into the menu that can unlink them.
   */
  heldSubjects?: LinkHolderSubject[];
  /** Holder rows only: unlink one named subject, chosen from the badge's menu. */
  onUnlinkSubjectRequest?: (target: { linkId: string; name: string }) => void;
  /** Only ever set on a LINKED (leader) subject row — the orchestrate grant's materialization state. */
  orchestrateStatus?: OrchestrateStatus;
  nested?: boolean;
  onUnlinkRequest?: () => void;
  linkedSessionsEnabled: boolean;
  linkModeActive: boolean;
  isLinkAnchor: boolean;
  isLinkCrossWorkspace: boolean;
  isLinkSelected: boolean;
  onStartLinkMode: () => void;
  onToggleLinkSelection: () => void;
  /** Adaptive-label inputs, shared by every card in the list. */
  labelContext?: RailLabelContext;
  /** Only set on nested subject rows — the underlying link's cadence, editable via LinkOptionsMenu. */
  cadence?: LinkCadence;
  grants?: Grant[];
  grantingSessionId?: string;
  linkId?: string;
  routerClient?: RouterClient;
}) {
  const now = useRelativeNow();
  const status = statusOf(session, agentState, openTaskBreakdown);
  const name = session.name || "Untitled session";
  // last_turn_at, not updated_at: the stamp says "last reply", and only a turn
  // end writes this field (store.Sessions.RecordTurnEnd), so it cannot be a
  // rename or a summary write wearing a reply's clothes.
  //
  // One stamp, not two. This used to be a stamp here plus a reserved footer
  // line carrying "started X · last reply Y" at opacity 0 — 16px of every card
  // spent showing nothing until the pointer arrived, and the half of it that
  // did show repeated this stamp in longer words. Both absolutes moved into
  // the title, where the thing you hover is the thing you asked about.
  const lastReply = formatRelativeStamp(session.last_turn_at, now);
  // Never dressed up as a reply: a session that has not answered yet reads
  // "new", and its title says so, rather than showing a creation time in the
  // slot the eye reads as "when did this last say something".
  const stamp = lastReply || "new";
  const stampTitle =
    (session.last_turn_at
      ? `Last reply ${formatAbsoluteStamp(session.last_turn_at)}`
      : "No reply yet") + ` · Started ${formatAbsoluteStamp(session.created_at)}`;
  const isPaused = session.activity !== "active";
  const openTaskCount = totalOpenTasks(openTaskBreakdown);
  const orchestrateLabel = orchestrateStatusLabel(orchestrateStatus);
  const labels = railLabelsFor(session, labelContext);
  // The rest of the record — model, provider, trust, isolation, context
  // occupancy — plus the labels the line above adaptively dropped, and any
  // sidechain the status line deliberately stays quiet about: a backgrounded
  // subagent never takes that line (railGroups.statusOf), so this is the only
  // place it is still counted.
  const backgroundSubagents =
    agentState.subagents - (agentState.blockingSubagents ?? agentState.subagents);
  const tooltip = [
    railTooltip(session, labelContext),
    backgroundSubagents > 0 ? `Background subagents: ${backgroundSubagents} running` : "",
  ]
    .filter(Boolean)
    .join("\n");
  const hasMeta = openTaskCount > 0 || (linkedSessionsEnabled && (Boolean(linkBadge) || Boolean(orchestrateLabel)));
  const showSummary = isPaused && Boolean(session.summary);
  // A nested subject row's link controls (update policy, unlink) live in one
  // flyout behind its role badge, so the row stays inside the rail's width.
  const linkOptions =
    nested && linkBadge && cadence && linkId && grantingSessionId && routerClient && onUnlinkRequest ? (
      <LinkOptionsMenu
        badge={linkBadge}
        subjectName={name}
        linkId={linkId}
        grantingSessionId={grantingSessionId}
        subjectSessionId={session.id}
        grants={grants ?? []}
        cadence={cadence}
        orchestrateStatus={orchestrateStatus}
        routerClient={routerClient}
        onUnlinkRequest={onUnlinkRequest}
      />
    ) : undefined;
  // And the holder's own badge carries the same relationship from the other
  // end, so the obvious thing to click on the parent card can unlink too.
  const holderMenu =
    linkBadge && heldSubjects?.length && onUnlinkSubjectRequest ? (
      <LinkHolderMenu
        badge={linkBadge}
        holderName={name}
        subjects={heldSubjects}
        onUnlinkRequest={onUnlinkSubjectRequest}
      />
    ) : undefined;

  // The action cluster hangs outside the card, so it is painted position:fixed
  // off the row's rect and only revealed once those coordinates exist.
  const rowRef = useRef<HTMLDivElement>(null);
  const hovering = useRef(false);
  const [clusterAt, setClusterAt] = useState<CSSProperties>();
  const clusterAnchored = clusterAt !== undefined;

  function anchorCluster() {
    if (rowRef.current) setClusterAt(clusterPlacement(rowRef.current));
  }

  // Fixed coordinates go stale the moment anything scrolls or the window
  // resizes. Follow the row rather than dismiss, and let go only once the row
  // has left the rail's scroll viewport — a cluster beside a clipped card is
  // the one thing worse than no cluster. The viewport is the box carrying
  // data-rail-scroll (SessionsSurface's aside), not the rail div in this file:
  // that one has no height of its own and is always exactly as tall as the
  // list, so measuring it would put every row permanently "inside".
  useEffect(() => {
    if (!clusterAnchored) return;
    function follow() {
      const row = rowRef.current;
      if (!row) return;
      const rect = row.getBoundingClientRect();
      const rail = row.closest("[data-rail-scroll]");
      const centre = rect.top + rect.height / 2;
      if (rail) {
        const bounds = rail.getBoundingClientRect();
        if (centre < bounds.top || centre > bounds.bottom) {
          setClusterAt(undefined);
          return;
        }
      }
      setClusterAt({ top: centre, left: rect.right });
    }
    window.addEventListener("scroll", follow, true);
    window.addEventListener("resize", follow);
    return () => {
      window.removeEventListener("scroll", follow, true);
      window.removeEventListener("resize", follow);
    };
  }, [clusterAnchored]);

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
      ref={rowRef}
      role="button"
      title={tooltip || undefined}
      data-rail-card=""
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
      onMouseEnter={() => {
        hovering.current = true;
        anchorCluster();
      }}
      onMouseLeave={() => {
        hovering.current = false;
        // Moving onto the cluster never lands here — it is a DOM child of the
        // row, so :hover and mouseleave both ignore where it paints. Keep the
        // anchor while something inside still holds focus, though: the reveal
        // is also :focus-within, and dropping it would yank the button the
        // keyboard is standing on out from under it.
        if (!rowRef.current?.contains(document.activeElement)) setClusterAt(undefined);
      }}
      onFocus={anchorCluster}
      onBlur={(e) => {
        if (hovering.current) return;
        if (e.relatedTarget instanceof Node && rowRef.current?.contains(e.relatedTarget)) return;
        setClusterAt(undefined);
      }}
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
        <span className={[styles.dot, styles[`dot-${status.dot}`]].join(" ")} aria-hidden="true" />
        <span className={styles.name}>{name}</span>
        <span className={styles.when} title={stampTitle}>
          {stamp}
        </span>
      </div>
      {/* Always rendered, one clause, never wrapped: the card's height must be
          identical in every state so a state change repaints and never reflows. */}
      <div className={[styles.status, styles[`status-${status.tone}`]].join(" ")} role="status">
        {status.glyph ? (
          <span
            className={[styles.statusGlyph, status.kind === "needs" ? styles.statusBell : ""]
              .filter(Boolean)
              .join(" ")}
            aria-hidden="true"
          >
            {status.glyph}
          </span>
        ) : (
          <span className={styles.statusSpinner} aria-hidden="true" />
        )}
        <span className={styles.statusLabel}>{status.label}</span>
        {status.since && <StatusElapsed since={status.since} />}
      </div>
      {/* One reason, never an inventory: workspace beats branch beats dir. */}
      {reason && (
        <div className={styles.why} data-match-reason={reason.field}>
          <span className={styles.whyKey}>{reason.field}</span>
          {reason.value}
        </div>
      )}
      {labelContext.lineShown && <ContextLine labels={labels} />}
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
          {linkedSessionsEnabled && linkOptions}
          {linkedSessionsEnabled && linkBadge && !linkOptions && holderMenu}
          {linkedSessionsEnabled && linkBadge && !linkOptions && !holderMenu && (
            <span
              className={[styles.linkBadge, linkBadge.startsWith("LEADER") ? styles.linkBadgeLead : ""]
                .filter(Boolean)
                .join(" ")}
            >
              {linkBadge}
            </span>
          )}
          {linkedSessionsEnabled && orchestrateLabel && (
            <span
              className={[
                styles.orchestrateBadge,
                orchestrateStatus === "unsupported" ? styles.orchestrateBadgeUnsupported : styles.orchestrateBadgePending,
              ].join(" ")}
            >
              {orchestrateLabel}
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
        <div
          className={styles.actionCluster}
          style={clusterAt}
          data-anchored={clusterAnchored ? "" : undefined}
          role="group"
          aria-label={`${name} actions`}
        >
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

/**
 * The adaptive context line (mockup docs/mockups/sessions-ux-v2.html §2).
 * Always rendered, one line, never wrapped, with its height reserved in CSS
 * even when every label is absent: which labels apply depends on the visible
 * set (a workspace name appears the moment a filter leaves two in the list),
 * and that has to repaint the line without ever reflowing the rail.
 */
function ContextLine({ labels }: { labels: RailLabels }) {
  const parts = [
    labels.branch ? { key: "branch", cls: styles.ctxBranch, text: labels.branch } : undefined,
    labels.workspace ? { key: "workspace", cls: styles.ctxItem, text: labels.workspace } : undefined,
    labels.cwd ? { key: "cwd", cls: styles.ctxItem, text: labels.cwd } : undefined,
    labels.agent ? { key: "agent", cls: styles.ctxItem, text: labels.agent } : undefined,
  ].filter((part): part is { key: string; cls: string; text: string } => part !== undefined);
  return (
    <div className={styles.context}>
      {labels.retro && (
        <span className={styles.ctxRetro} title="Retro session" data-label="retro">
          ↺
        </span>
      )}
      {parts.map((part, i) => [
        i > 0 ? (
          <span key={`${part.key}-sep`} className={styles.ctxSep} aria-hidden="true">
            ·
          </span>
        ) : null,
        <span key={part.key} className={part.cls} data-label={part.key}>
          {part.text}
        </span>,
      ])}
    </div>
  );
}

/**
 * A live state's elapsed counter. Its own component on purpose: the 1s clock
 * is only subscribed while some card is mid-turn or parked, so a rail of
 * resting sessions runs no seconds timer at all.
 */
function StatusElapsed({ since }: { since: string }) {
  const elapsed = formatElapsedStamp(since, useElapsedNow());
  if (!elapsed) return null;
  return <span className={styles.statusElapsed}>{elapsed}</span>;
}
