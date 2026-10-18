import { useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties } from "react";
import { formatShortcut } from "../../../platform/shortcut";
import { useLiveShells } from "../../../conversation/ShellDock/useLiveShells";
import { useShellDockState } from "../../../conversation/ShellDock/dockState";
import type { ShellInfo } from "../../../conversation/TerminalRenderer/ptyBridge";
import { SessionDeleteConfirmDialog } from "../actions/SessionDeleteConfirmDialog";
import { SessionUnlinkConfirmDialog } from "../actions/SessionUnlinkConfirmDialog";
import { LinkHolderMenu } from "./LinkHolderMenu";
import type { LinkHolderSubject } from "./LinkHolderMenu";
import { LinkOptionsMenu } from "./LinkOptionsMenu";
import { LinkChooserDialog } from "./LinkChooserDialog";
import { HarnessMark } from "./HarnessMark";
import { LinkConnector } from "./LinkConnector";
import { StatusMark } from "./StatusMark";
import { DelegateRows } from "./DelegateRows";
import {
  EMPTY_AGENT_STATE,
  EMPTY_OPEN_TASK_BREAKDOWN,
  RAIL_FILTERS,
  filterBucketOf,
  asideDemandsAttention,
  isSetAside,
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
import { buildRailRows, computeRailLinks, railDescendants } from "./railLinks";
import type { RailRow, RailShortcut } from "./railLinks";
import {
  isEditableElement,
  matchReason,
  matchSession,
  parseSearchQuery,
  scopeSuggestions,
} from "./railSearch";
import type { MatchReason, SearchMatch } from "./railSearch";
import { activeOrder, doneOrder, rankBetween, useRailRanks } from "./railOrder";
import { useUnseenSessions } from "./railUnseen";
import {
  EMPTY_LABEL_CONTEXT,
  railLabelsFor,
  hasRailLabels,
  railTooltip,
  workspaceFacts,
} from "./railLabels";
import type { RailLabelContext, RailLabels } from "./railLabels";
import { EMPTY_WORKSPACE_INDEX, workspaceIndex } from "../../../router/workspaceRef";
import type { WorkspaceIndex } from "../../../router/workspaceRef";
import { agentIndex } from "../../../router/agentRef";
import { workState, type WorkState } from "../../../conversation/Worktree/workState";
import { plural } from "../../../conversation/Worktree/worktreeCopy";
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
  Agent,
  Delegate,
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
  /**
   * Put a session to sleep: free the agent process. Takes the whole session
   * because the decision isn't the rail's to make — useSessionSleep reads the
   * record to know whether to ask about unlanded commits first, and asks the
   * same question for the workspace kebab.
   *
   * The rail deliberately has no counterpart — waking is not a step the user
   * has to take, since the first send wakes a sleeping session transparently.
   * An explicit pre-warm lived here as a ▶ button and only ever bought a few
   * seconds of latency, at the price of a control that looked like a
   * lifecycle state. The workspace kebab still offers it for a deliberate
   * warm-up.
   */
  onSleep?: (session: Session) => void;
  onDelete?: (id: string) => void;
  /**
   * The ✓: check a session off into Done (which also puts its agent to
   * sleep), or bring it back to the top of the active list. `working` is the
   * rail's read of the session mid-turn, so the owner can ask before stopping
   * it (useSessionSleep.requestDone).
   */
  onSetDone?: (session: Session, done: boolean, working: boolean) => void;
  /**
   * Persist a drag: the session's new rank in the active list
   * (Session.rail_rank). The rail shows the move at once and reverts it if
   * this rejects. Absent, the grip is not offered.
   */
  onMove?: (id: string, rank: number) => Promise<unknown>;
  /** Linked sessions (flag-gated): every link touching a visible session, deduped. */
  links?: Link[];
  /**
   * Workspace records, for the adaptive context labels — a workspace's
   * primary repo (which is what makes a cwd worth printing) and its default
   * persona (which is what makes an agent NOT worth printing). Matched by
   * name: Session.workspace is a name, not an id.
   */
  workspaces?: Workspace[];
  /**
   * Persona records, so a card can name the persona it is running rather than
   * the `agt_…` reference the record stores (router/agentRef.ts). Absent is a
   * normal pre-load state: the label is dropped, never shown as a hash.
   */
  agents?: Agent[];
  routerClient?: RouterClient;
  /**
   * True once the first session list has actually loaded. The rail seeds its
   * "already seen" set from that list, so it must not do so from the empty
   * pre-load one. Absent, a non-empty list counts as loaded.
   */
  sessionsLoaded?: boolean;
  className?: string;
  /**
   * Settings' "Expose subagents in session list": each session's delegates
   * (harness-run subagents) as rows nested under its card. Default on.
   */
  showDelegates?: boolean;
  /** Settings' "Fade out subagent cards after inactivity", in ms (delegateFadeMs). */
  delegateFadeMs?: number;
  /** The delegate open in the main pane, if any — its row reads selected. */
  selectedDelegate?: { sessionId: string; harnessId: string };
  /** Opens a delegate in the main pane. */
  onOpenDelegate?: (d: Delegate) => void;
}

/** Link mode: anchor session (chosen by clicking a card's link action) plus every session tapped in after it. */
interface LinkModeState {
  anchorId: string;
  selectedIds: Set<string>;
}

/** How many Done rows show before "Show N more". */
const DONE_PREVIEW = 5;

/** localStorage key: whether the Done list is open ("0" = collapsed). A view preference, so local. */
export const DONE_OPEN_KEY = "mads.sessionRail.doneOpen";

/** The Done list's open state, remembered across reloads. Open unless the user closed it. */
function useDoneOpen(): [boolean, (open: boolean) => void] {
  const [open, setOpen] = useState(() => {
    try {
      return localStorage.getItem(DONE_OPEN_KEY) !== "0";
    } catch {
      return true;
    }
  });
  return [
    open,
    (next) => {
      setOpen(next);
      try {
        localStorage.setItem(DONE_OPEN_KEY, next ? "1" : "0");
      } catch {
        // Storage blocked: the choice just won't survive a reload.
      }
    },
  ];
}

type DropSide = "before" | "after";

/** A drag in flight: the card being moved, and the slot it is over. */
interface DragState {
  id: string;
  over?: string;
  side?: DropSide;
}

/** The holder card's grip, while the active list can be reordered. */
interface GripProps {
  dragging: boolean;
  onDragStart: () => void;
  onDragEnd: () => void;
}

export function SessionRail({
  sessions,
  openTaskCounts,
  agentStates = {},
  selectedId,
  onSelect,
  onSleep,
  onDelete,
  onSetDone,
  onMove,
  links = [],
  workspaces = [],
  agents = [],
  routerClient = router,
  sessionsLoaded,
  className,
  showDelegates = true,
  delegateFadeMs,
  selectedDelegate,
  onOpenDelegate,
}: SessionRailProps) {
  const linkedSessionsEnabled = FLAGS.linkedSessions;
  // Subscribed once here, not per card: N cards must not each hold a listener
  // on the shell set. Keyed by session; there is at most one shell per session.
  const liveShells = useLiveShells();
  const shellBySession = useMemo(
    () => new Map(liveShells.map((shell) => [shell.sessionId, shell])),
    [liveShells],
  );
  const [query, setQuery] = useState("");
  const railRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  // The user's order, never a live one: see railOrder.ts.
  const { rankOf, setRank } = useRailRanks(sessions, onMove);
  const ordered = activeOrder(sessions, rankOf);
  const { isUnseen, markSeen } = useUnseenSessions(sessions, selectedId, sessionsLoaded);
  const now = useRelativeNow();
  const [filter, setFilter] = useState<RailFilter>("all");
  const [doneOpen, setDoneOpen] = useDoneOpen();
  const [doneShowAll, setDoneShowAll] = useState(false);
  const [drag, setDrag] = useState<DragState>();
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
  // the Done list included, capped or collapsed. A search that only searched what was rendered would be
  // worse than useless.
  const terms = parseSearchQuery(query);
  const searching = terms.length > 0;
  const matchById = new Map<string, SearchMatch>();
  const ranked: { row: RailRow; rank: number; resting: number }[] = [];
  allRows.forEach((row, resting) => {
    let rank: number | undefined;
    // A holder answers for everything nested under it here too, at any depth
    // — a subject can only be shown underneath the holder that owns it.
    // Shortcut rows are pointers, not sessions, so they never match.
    for (const session of [row.session, ...railDescendants(row)]) {
      const match = matchSession(session, terms, wsIndex);
      if (!match) continue;
      matchById.set(session.id, match);
      rank = rank === undefined ? match.rank : Math.min(rank, match.rank);
    }
    if (rank !== undefined) ranked.push({ row, rank, resting });
  });
  // The one sanctioned reorder: relevance while a query is live, because the
  // user asked for it by typing. `resting` is the settled index, so ties break
  // by the user's order and clearing the field drops straight back to it —
  // the order itself is never touched.
  const rows = searching
    ? [...ranked].sort((a, b) => a.rank - b.rank || a.resting - b.resting).map((r) => r.row)
    : allRows;
  const suggestions = scopeSuggestions(sessions, query, wsIndex);

  function bucketOf(session: Session): RailFilterBucket {
    return filterBucketOf(
      statusOf(
        session,
        agentStates[session.id] ?? EMPTY_AGENT_STATE,
        openTaskCounts[session.id] ?? EMPTY_OPEN_TASK_BREAKDOWN,
        now,
      ),
    );
  }

  // A holder answers for every session nested under it, grandchildren
  // included: they ride along when it survives a filter, so counting it as
  // merely "idle" while a nested card is parked would make "Attn. 1" click
  // through to a list without it. Shortcut rows point at cards that live (and
  // are counted) elsewhere, so they never pull a group's bucket.
  const bucketByRowId = new Map<string, RailFilterBucket>(
    rows.map((row) => [
      row.session.id,
      mostUrgentBucket([row.session, ...railDescendants(row)].map(bucketOf)),
    ]),
  );
  const counts: Record<RailFilter, number> = { all: rows.length, working: 0, needs: 0, idle: 0 };
  for (const bucket of bucketByRowId.values()) counts[bucket] += 1;

  // Narrowing only — the survivors keep the order they already had, which is
  // the whole reason a chip can do triage without a group's side effects.
  const visible =
    filter === "all" ? rows : rows.filter((row) => bucketByRowId.get(row.session.id) === filter);
  const facts = workspaceFacts(workspaces);
  const labelContext: RailLabelContext = {
    facts,
    agents: agentIndex(agents),
  };

  // Two lists: the active one, in the user's order, and Done. A card the user
  // checked off goes to Done unless it currently needs them — then it sits in
  // the active list, at its own rank, until it doesn't.
  //
  // Done is checked on the ROOT only. A linked group is one card with its
  // subjects drawn under it, recursively (railLinks.ts), so checking off a
  // leader takes the connector and every session under it with it —
  // splitting a group across two lists would leave arrows pointing at
  // nothing.
  const active: RailRow[] = [];
  const done: RailRow[] = [];
  for (const row of visible) {
    // The group-aware bucket, so a parked subject pulls its whole group back
    // out of Done — the same reason the filter chips count a holder by its
    // most urgent member.
    const bucket = bucketByRowId.get(row.session.id) ?? "idle";
    if (isSetAside(row.session) && !asideDemandsAttention(bucket)) done.push(row);
    else active.push(row);
  }
  // While a query is live there is one list: every hit is a result —
  // including the ones checked off, which is the whole point of going looking.
  const listRows = searching ? visible : active;
  const doneRows = searching ? [] : doneOrder(done, (row) => row.session);
  const shownDone = doneShowAll ? doneRows : doneRows.slice(0, DONE_PREVIEW);

  // Reordering is a gesture on the whole active list, so it is only offered
  // while the whole list is what's showing: a drop between two of a filter's
  // survivors would land somewhere among cards the user can't see.
  const canReorder = Boolean(onMove) && !searching && filter === "all" && !linkMode;

  /** Move an active row to `index` in the active list as it would read without it. */
  function moveRow(id: string, index: number) {
    const from = active.findIndex((row) => row.session.id === id);
    if (from < 0) return;
    const rest = active.filter((row) => row.session.id !== id);
    const to = Math.max(0, Math.min(index, rest.length));
    if (to === from) return;
    const above = rest[to - 1]?.session;
    const below = rest[to]?.session;
    setRank(
      active[from].session,
      rankBetween(above && rankOf(above), below && rankOf(below)),
    );
  }

  function dropOn(targetId: string, where: DropSide) {
    const dragged = drag?.id;
    setDrag(undefined);
    if (!dragged || dragged === targetId) return;
    const rest = active.filter((row) => row.session.id !== dragged);
    const at = rest.findIndex((row) => row.session.id === targetId);
    if (at < 0) return;
    moveRow(dragged, at + (where === "after" ? 1 : 0));
  }

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

  // A shortcut row's jump: bring the session's one real card into view and
  // flash it, so the eye lands on it. A card outside the drawn list (a
  // collapsed Done, a filter) has nothing to scroll to, so this does nothing.
  function jumpTo(id: string) {
    const card = [
      ...(railRef.current?.querySelectorAll<HTMLElement>("[data-rail-card]") ?? []),
    ].find((el) => el.dataset.railId === id);
    if (!card) return;
    card.scrollIntoView({ block: "nearest" });
    // Restart the animation if it is already running: drop the class, force a
    // style flush, add it back.
    card.classList.remove(styles.flash);
    void card.offsetWidth;
    card.classList.add(styles.flash);
    card.addEventListener("animationend", () => card.classList.remove(styles.flash), { once: true });
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
    // ⌥↑ / ⌥↓ on a card: the grip's keyboard equivalent. Focus stays on the
    // card, which keeps its element (and so its focus) across the re-sort.
    if (event.altKey) {
      const id = target.dataset?.railCard !== undefined ? target.dataset.railId : undefined;
      if (!canReorder || !id) return;
      const from = active.findIndex((row) => row.session.id === id);
      if (from < 0) return;
      event.preventDefault();
      moveRow(id, from + (event.key === "ArrowDown" ? 1 : -1));
      return;
    }
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
  // not just the id — including for a row in the Done list or nested under a
  // link, which is why this reads the unfiltered list.
  const deleteTarget = sessions.find((s) => s.id === confirmDeleteId);

  // Shared by the active list and Done — the two render identical cards.
  const rowProps: RailRowContext = {
    openTaskCounts,
    agentStates,
    shellBySession,
    matchById,
    wsIndex,
    selectedId,
    isUnseen,
    onSeen: markSeen,
    onSelect,
    onSleep,
    onSetDone,
    bucketOf,
    onDeleteRequest: setConfirmDeleteId,
    onUnlinkRequest: setUnlinkTarget,
    linkedSessionsEnabled,
    linkMode,
    onStartLinkMode: startLinkMode,
    onToggleLinkSelection: toggleLinkSelection,
    onJump: jumpTo,
    labelContext,
    routerClient,
    showDelegates,
    delegateFadeMs,
    selectedDelegate,
    onOpenDelegate,
  };

  return (
    <div
      ref={railRef}
      className={[styles.rail, className].filter(Boolean).join(" ")}
      aria-label="Session list"
      onKeyDown={walkResults}
    >
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
        {RAIL_FILTERS.map(({ id, label, title }) => (
          <button
            key={id}
            type="button"
            className={[styles.chip, filter === id ? styles.chipOn : ""].filter(Boolean).join(" ")}
            aria-pressed={filter === id}
            title={title}
            onClick={() => setFilter(id)}
          >
            {label}
            <span className={styles.chipCount}>{counts[id]}</span>
          </button>
        ))}
      </div>
      <div className={styles.list} data-rail-list="active">
        {listRows.map((row) =>
          canReorder ? (
            <div
              key={row.session.id}
              className={styles.dropSlot}
              data-drop={drag?.over === row.session.id ? drag.side : undefined}
              onDragOver={(e) => {
                if (!drag) return;
                e.preventDefault();
                e.dataTransfer.dropEffect = "move";
                const rect = e.currentTarget.getBoundingClientRect();
                const side: DropSide = e.clientY < rect.top + rect.height / 2 ? "before" : "after";
                if (drag.over !== row.session.id || drag.side !== side) {
                  setDrag({ ...drag, over: row.session.id, side });
                }
              }}
              onDrop={(e) => {
                if (!drag) return;
                e.preventDefault();
                dropOn(row.session.id, drag.side ?? "before");
              }}
            >
              <RailRowGroup
                row={row}
                {...rowProps}
                grip={{
                  dragging: drag?.id === row.session.id,
                  onDragStart: () => setDrag({ id: row.session.id }),
                  onDragEnd: () => setDrag(undefined),
                }}
              />
            </div>
          ) : (
            <RailRowGroup key={row.session.id} row={row} {...rowProps} />
          ),
        )}
        {visible.length === 0 && (
          <div className={styles.emptyNote}>
            {searching
              ? `No session matches “${query.trim()}”. Searched name, workspace, directory and branch.`
              : "No sessions match this filter."}
          </div>
        )}
      </div>
      {doneRows.length > 0 && (
        <div className={[styles.list, styles.doneList].join(" ")} data-rail-list="done">
          {/* Open by default and remembered: Done is a list worth scanning on
              purpose, so a fold that hid it every visit worked against that.
              The cap is what keeps it from burying the rail over time. */}
          <button
            type="button"
            className={styles.sectionToggle}
            aria-expanded={doneOpen}
            onClick={() => setDoneOpen(!doneOpen)}
          >
            <span aria-hidden="true">{doneOpen ? "▾" : "▸"}</span>
            Done
            <span className={styles.sectionCount}>· {doneRows.length}</span>
          </button>
          {doneOpen &&
            shownDone.map((row) => <RailRowGroup key={row.session.id} row={row} {...rowProps} />)}
          {doneOpen && doneRows.length > DONE_PREVIEW && (
            <button
              type="button"
              className={styles.showMore}
              onClick={() => setDoneShowAll(!doneShowAll)}
            >
              {doneShowAll ? "Show fewer" : `Show ${doneRows.length - DONE_PREVIEW} more`}
            </button>
          )}
        </div>
      )}
      {deleteTarget && (
        <SessionDeleteConfirmDialog
          open
          session={deleteTarget}
          agentState={agentStates[deleteTarget.id]}
          openTaskBreakdown={openTaskCounts[deleteTarget.id]}
          routerClient={routerClient}
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

/** Everything a card needs from the rail, shared by every card at every depth. */
interface RailRowContext {
  openTaskCounts: Record<string, OpenTaskBreakdown>;
  agentStates: Record<string, SessionAgentState>;
  /** Each live dock shell by session id, hidden or visible. */
  shellBySession: Map<string, ShellInfo>;
  /** Only populated while a query is live — a card's own hit, for its reason line. */
  matchById: Map<string, SearchMatch>;
  /** Resolves a session's workspace reference to its workspace (router/workspaceRef.ts). */
  wsIndex: WorkspaceIndex;
  selectedId?: string;
  /** Not yet hovered, focused or opened by the user — see useUnseenSessions. */
  isUnseen: (id: string) => boolean;
  onSeen: (id: string) => void;
  onSelect?: (id: string) => void;
  onSleep?: (session: Session) => void;
  onSetDone?: (session: Session, done: boolean, working: boolean) => void;
  /** The rail's group-aware bucket, which the ✓ reads to know whether a turn is running. */
  bucketOf: (session: Session) => RailFilterBucket;
  onDeleteRequest: (id: string) => void;
  onUnlinkRequest: (target: { linkId: string; name: string }) => void;
  linkedSessionsEnabled: boolean;
  linkMode?: LinkModeState;
  onStartLinkMode: (id: string) => void;
  onToggleLinkSelection: (id: string) => void;
  /** A shortcut row's click: bring the session's real card into view. */
  onJump: (id: string) => void;
  labelContext: RailLabelContext;
  routerClient: RouterClient;
  showDelegates: boolean;
  delegateFadeMs?: number;
  selectedDelegate?: { sessionId: string; harnessId: string };
  onOpenDelegate?: (d: Delegate) => void;
}

/**
 * The subjects a holder answers for in its link menu: the ones drawn under
 * it and the ones it only points at, since it holds a link to each.
 */
function heldSubjectsOf(row: RailRow): LinkHolderSubject[] {
  return [
    ...row.subjects.map((sub) => ({
      name: sub.session.name || "Untitled session",
      badge: sub.link!.badge,
      linkId: sub.link!.linkId,
    })),
    ...row.shortcuts.map((sc) => ({
      name: sc.session.name || "Untitled session",
      badge: sc.role === "leader" ? ("LINKED" as const) : ("WATCHED" as const),
      linkId: sc.linkId,
    })),
  ];
}

/** A session's delegates nest right under its own card — before any linked subjects, which are other sessions. */
function delegatesOf(ctx: RailRowContext, sessionId: string, linkTrunk = false) {
  const list = ctx.showDelegates ? ctx.agentStates[sessionId]?.delegates : undefined;
  if (!list || list.length === 0) return null;
  return (
    <DelegateRows
      delegates={list}
      selectedHarnessId={
        ctx.selectedDelegate?.sessionId === sessionId ? ctx.selectedDelegate.harnessId : undefined
      }
      onOpen={ctx.onOpenDelegate}
      fadeMs={ctx.delegateFadeMs}
      linkTrunk={linkTrunk}
    />
  );
}

/** Whether anything hangs off this row's link trunk. */
function hasLinkChildren(row: RailRow, ctx: RailRowContext): boolean {
  return ctx.linkedSessionsEnabled && (row.subjects.length > 0 || row.shortcuts.length > 0);
}

function RailRowGroup({ row, grip, ...ctx }: RailRowContext & { row: RailRow; grip?: GripProps }) {
  const isAnchor = ctx.linkMode?.anchorId === row.session.id;
  const children = hasLinkChildren(row, ctx);
  return (
    <>
      <SessionRow
        session={row.session}
        openTaskBreakdown={ctx.openTaskCounts[row.session.id] ?? EMPTY_OPEN_TASK_BREAKDOWN}
        agentState={ctx.agentStates[row.session.id] ?? EMPTY_AGENT_STATE}
        shell={ctx.shellBySession.get(row.session.id)}
        matchReason={reasonFor(row.session, ctx.matchById, ctx.wsIndex)}
        selected={row.session.id === ctx.selectedId}
        unseen={ctx.isUnseen(row.session.id)}
        onSeen={ctx.onSeen}
        onSelect={ctx.onSelect}
        onSleep={ctx.onSleep}
        onSetDone={ctx.onSetDone}
        working={ctx.bucketOf(row.session) === "working"}
        grip={grip}
        onDeleteRequest={ctx.onDeleteRequest}
        linkBadge={row.badge}
        linkCount={ctx.linkedSessionsEnabled ? row.linkCount : 0}
        heldSubjects={ctx.linkedSessionsEnabled ? heldSubjectsOf(row) : []}
        onUnlinkSubjectRequest={ctx.onUnlinkRequest}
        linkedSessionsEnabled={ctx.linkedSessionsEnabled}
        linkModeActive={Boolean(ctx.linkMode)}
        isLinkAnchor={isAnchor}
        isLinkSelected={ctx.linkMode?.selectedIds.has(row.session.id) ?? false}
        labelContext={ctx.labelContext}
        onStartLinkMode={() => ctx.onStartLinkMode(row.session.id)}
        onToggleLinkSelection={() => ctx.onToggleLinkSelection(row.session.id)}
      />
      {delegatesOf(ctx, row.session.id, children)}
      {children && <RailSubjects row={row} ctx={ctx} />}
    </>
  );
}

/**
 * What hangs off a holder's trunk: the sessions whose home it is, each with
 * its own subtree, then a one-line shortcut for each session it holds that is
 * drawn somewhere else. Recursive — a subject that holds others draws its own
 * trunk inside its .subject box, the same way a root does.
 */
function RailSubjects({ row, ctx }: { row: RailRow; ctx: RailRowContext }) {
  return (
    // The subjects share one container so the trunk can be drawn by rules
    // that only ever measure their own box — see .subjects in the CSS. A
    // holder of several subjects gets one numeral on the trunk rather than a
    // "LEADER · 3" pill on its meta row. Shortcuts are not sessions, so the
    // numeral doesn't count them.
    <div className={styles.subjects}>
      {row.subjects.length > 1 && (
        <span className={styles.linkFan} aria-hidden="true">
          {row.subjects.length}
        </span>
      )}
      {/* Each subject and everything under it (its delegates, its own
          subjects) share a box, so the trunk's continuation
          (.subject:not(:last-child)) runs past all of that to the next one,
          and stops at the last. */}
      {row.subjects.map((sub) => (
        <div key={sub.session.id} className={styles.subject}>
          <NestedSessionRow row={sub} ctx={ctx} />
          {delegatesOf(ctx, sub.session.id, hasLinkChildren(sub, ctx))}
          {hasLinkChildren(sub, ctx) && <RailSubjects row={sub} ctx={ctx} />}
        </div>
      ))}
      {row.shortcuts.map((sc) => (
        <div key={`sc-${sc.linkId}`} className={styles.subject}>
          <ShortcutRow shortcut={sc} onJump={ctx.onJump} />
        </div>
      ))}
    </div>
  );
}

function NestedSessionRow({ row, ctx }: { row: RailRow; ctx: RailRowContext }) {
  const sub = row.session;
  const link = row.link!;
  return (
    <SessionRow
      session={sub}
      openTaskBreakdown={ctx.openTaskCounts[sub.id] ?? EMPTY_OPEN_TASK_BREAKDOWN}
      agentState={ctx.agentStates[sub.id] ?? EMPTY_AGENT_STATE}
      shell={ctx.shellBySession.get(sub.id)}
      matchReason={reasonFor(sub, ctx.matchById, ctx.wsIndex)}
      selected={sub.id === ctx.selectedId}
      unseen={ctx.isUnseen(sub.id)}
      onSeen={ctx.onSeen}
      onSelect={ctx.onSelect}
      onSleep={ctx.onSleep}
      onDeleteRequest={ctx.onDeleteRequest}
      linkBadge={link.badge}
      linkCount={row.linkCount}
      orchestrateStatus={link.orchestrateStatus}
      orchestrateError={link.orchestrateError}
      nested
      onUnlinkRequest={() =>
        ctx.onUnlinkRequest({ linkId: link.linkId, name: sub.name || "Untitled session" })
      }
      linkedSessionsEnabled={ctx.linkedSessionsEnabled}
      linkModeActive={false}
      isLinkAnchor={false}
      isLinkSelected={false}
      onStartLinkMode={() => {}}
      onToggleLinkSelection={() => {}}
      labelContext={ctx.labelContext}
      cadence={link.cadence}
      grants={link.grants}
      grantingSessionId={link.grantingSessionId}
      linkId={link.linkId}
      routerClient={ctx.routerClient}
    />
  );
}

/**
 * A one-line pointer at a session this holder also leads or watches, whose
 * card is drawn under its home holder instead (railLinks.ts). Not a session
 * row — no status, no actions — so it is a plain button, outside the
 * [data-rail-card] set the keyboard walks.
 */
function ShortcutRow({ shortcut, onJump }: { shortcut: RailShortcut; onJump: (id: string) => void }) {
  const name = shortcut.session.name || "Untitled session";
  const where = shortcut.homeHolder
    ? `Shown under ${shortcut.homeHolder.name || "Untitled session"}`
    : "Shown at the top level";
  const leader = shortcut.role === "leader";
  return (
    <button
      type="button"
      className={styles.shortcut}
      title={`${where}. Click to jump.`}
      aria-label={`Jump to ${name} (${leader ? "led" : "watched"} from here). ${where}.`}
      data-rail-shortcut={shortcut.session.id}
      onClick={(e) => {
        e.stopPropagation();
        onJump(shortcut.session.id);
      }}
    >
      <span
        className={[styles.shortcutGlyph, leader ? styles.shortcutLead : styles.shortcutWatch].join(" ")}
        aria-hidden="true"
      >
        {leader ? "♛" : "◉"}
      </span>
      <span className={styles.shortcutName}>{name}</span>
      <span className={styles.shortcutJump} aria-hidden="true">
        ↗
      </span>
    </button>
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
  shell,
  matchReason: reason,
  selected,
  unseen,
  onSeen,
  onSelect,
  onSleep,
  onSetDone,
  working = false,
  grip,
  onDeleteRequest,
  linkBadge,
  linkCount = 0,
  heldSubjects,
  onUnlinkSubjectRequest,
  orchestrateStatus,
  orchestrateError,
  nested,
  onUnlinkRequest,
  linkedSessionsEnabled,
  linkModeActive,
  isLinkAnchor,
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
  /** This session's live dock shell, hidden or visible; absent on a plain card. */
  shell?: ShellInfo;
  /** Set only when a live search matched this card on something other than its name. */
  matchReason?: MatchReason;
  selected: boolean;
  /** Shown until the user first hovers, focuses or opens the card. */
  unseen: boolean;
  onSeen: (id: string) => void;
  onSelect?: (id: string) => void;
  onSleep?: (session: Session) => void;
  onSetDone?: (session: Session, done: boolean, working: boolean) => void;
  /** Mid-turn, as the rail reads it: the ✓ asks before stopping a running turn. */
  working?: boolean;
  /** Only on a holder card in a reorderable active list. */
  grip?: GripProps;
  onDeleteRequest: (id: string) => void;
  linkBadge?: string;
  /** Counted links touching this session — tints the chain glyph and badges it with the number. */
  linkCount?: number;
  /**
   * Only set on a holder row — the subjects it holds, which is what turns its
   * badge from a bare pill into the menu that can unlink them.
   */
  heldSubjects?: LinkHolderSubject[];
  /** Holder rows only: unlink one named subject, chosen from the badge's menu. */
  onUnlinkSubjectRequest?: (target: { linkId: string; name: string }) => void;
  /** Only ever set on a LINKED (leader) subject row — the orchestrate grant's materialization state. */
  orchestrateStatus?: OrchestrateStatus;
  /** Why the tools failed to start, when orchestrateStatus is "failed" — the badge's tooltip. */
  orchestrateError?: string;
  nested?: boolean;
  onUnlinkRequest?: () => void;
  linkedSessionsEnabled: boolean;
  linkModeActive: boolean;
  isLinkAnchor: boolean;
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
  const status = statusOf(session, agentState, openTaskBreakdown, now);
  const name = session.name || "Untitled session";
  // The work marker (mockup docs/mockups/session-worktrees.html §1): hollow
  // for uncommitted files, filled for commits waiting to land. Quiet — read
  // off the record's counts, the same ones the dock's Commit/Land control uses.
  const work = workState(session);
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
  const isDone = isSetAside(session);
  const openTaskCount = totalOpenTasks(openTaskBreakdown);
  const labels = railLabelsFor(session, labelContext);
  // The rest of the record — model, provider, trust, isolation, context
  // occupancy — plus the labels the line above adaptively dropped, and any
  // sidechain the status line deliberately stays quiet about: a backgrounded
  // subagent never takes that line (railGroups.statusOf), so this is the only
  // place it is still counted.
  const backgroundSubagents =
    agentState.subagents - (agentState.blockingSubagents ?? agentState.subagents);
  // A stalled card withholds the clause the agent last reported, because
  // stating it would be stating it as true. It is still the most useful thing
  // to know about the session, so it goes here with the time attached — the
  // difference between "it is doing this" and "this is the last thing it said
  // it was doing, 23h ago".
  const stalledNote =
    status.kind === "stalled"
      ? `Last reported: ${agentState.label ?? "Working…"} (${formatAbsoluteStamp(status.since!)})`
      : "";
  // A shell card says what its shell is doing in the title. "Hidden" is the
  // case worth a peek at what is running, so that is asked once per hover —
  // never on render and never polled (the host answers inspect on demand).
  const dock = useShellDockState(session.id);
  const [hiddenRunning, setHiddenRunning] = useState<string>();
  // Visible only on the selected session: another session's stored `open`
  // just means its dock comes back when you return — its shell is detached.
  const shellOpen = shell !== undefined && selected === true && dock.open;
  const shellNote = !shell
    ? ""
    : shellOpen
      ? "Shell open"
      : hiddenRunning
        ? `Shell hidden · running: ${hiddenRunning}`
        : "Shell hidden";
  function inspectHiddenShell() {
    const host = typeof window !== "undefined" ? window.mads?.shell : undefined;
    if (!shell || shellOpen || !host) return;
    setHiddenRunning(undefined);
    host
      .inspect(shell.shellId)
      .then((activities) => {
        const activity = activities.find((a) => a.shellId === shell.shellId) ?? activities[0];
        if (!activity?.busy) return;
        const [first, ...rest] = activity.processes;
        const command = first?.command ?? activity.foreground;
        if (!command) return;
        setHiddenRunning(rest.length > 0 ? `${command} (+${rest.length} more)` : command);
      })
      // Best effort: a shell that ended mid-hover just keeps the plain clause.
      .catch(() => undefined);
  }
  const tooltip = [
    railTooltip(session, labelContext),
    shellNote,
    stalledNote,
    backgroundSubagents > 0 ? `Background subagents: ${backgroundSubagents} running` : "",
  ]
    .filter(Boolean)
    .join("\n");
  const hasMeta = openTaskCount > 0;
  const showContext = hasRailLabels(labels);
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
  // keeps its inactive state and wakes transparently on the first composer
  // send. Explicit resume is the ▶ pre-warm button below (and the workspace
  // kebab).
  function activate() {
    onSelect?.(session.id);
  }

  function activateOrToggle() {
    if (linkModeActive) {
      if (isLinkAnchor) return;
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
      data-rail-id={session.id}
      data-unseen={unseen ? "" : undefined}
      // Awake or asleep isn't drawn any more (both read "Idle" — see
      // RailStatusKind), but the e2e journeys still need to see which one the
      // rail's list believes.
      data-activity={session.activity}
      tabIndex={0}
      className={[
        styles.row,
        selected ? styles.selected : "",
        shell ? styles.shellCard : "",
        unseen ? styles.unseen : "",
        nested ? styles.nestedRow : "",
        linkModeActive && isLinkAnchor ? styles.linkAnchor : "",
        grip?.dragging ? styles.dragging : "",
      ]
        .filter(Boolean)
        .join(" ")}
      onClick={activateOrToggle}
      onMouseEnter={() => {
        hovering.current = true;
        onSeen(session.id);
        anchorCluster();
        inspectHiddenShell();
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
      onFocus={() => {
        onSeen(session.id);
        anchorCluster();
      }}
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
      {nested && <LinkConnector watched={linkBadge === "WATCHED"} />}
      {grip && (
        // In the rail's left gutter, outside the card: clear of the unseen
        // bar (inset on the card's own edge), the status mark (inside it) and
        // the link connector (which only ever hangs off a nested subject, and
        // a subject has no grip — it moves with its holder).
        <span
          className={styles.grip}
          draggable
          title={`Drag to reorder · ${formatShortcut("Alt+↑")} / ${formatShortcut("Alt+↓")}`}
          aria-hidden="true"
          data-rail-grip=""
          onClick={(e) => e.stopPropagation()}
          onDragStart={(e) => {
            e.stopPropagation();
            e.dataTransfer.effectAllowed = "move";
            e.dataTransfer.setData("application/x-mads-session", session.id);
            if (rowRef.current) {
              const rect = rowRef.current.getBoundingClientRect();
              e.dataTransfer.setDragImage(rowRef.current, e.clientX - rect.left, e.clientY - rect.top);
            }
            setClusterAt(undefined);
            grip.onDragStart();
          }}
          onDragEnd={grip.onDragEnd}
        >
          ⠿
        </span>
      )}
      <div className={styles.rowTop}>
        {shell && (
          <span
            className={[styles.prompt, styles[`status-${status.tone}`]].join(" ")}
            aria-hidden="true"
            data-shell-prompt=""
          >
            &gt;_
          </span>
        )}
        <span className={styles.name}>{name}</span>
        {/* The tint is colour alone; this is the same fact for a screen reader. */}
        {unseen && <span className={styles.srOnly}>New session</span>}
        {work && (
          <span
            className={[styles.workMark, work.kind === "land" ? "" : styles.workMarkHollow]
              .filter(Boolean)
              .join(" ")}
            title={workNote(work)}
            aria-label={workNote(work)}
            data-work-mark={work.kind}
          />
        )}
        <HarnessMark provider={session.provider} />
        <span className={styles.when} title={stampTitle}>
          {stamp}
        </span>
      </div>
      {/* Always rendered, one clause, never wrapped: the card's height must be
          identical in every state so a state change repaints and never reflows. */}
      <div className={[styles.status, styles[`status-${status.tone}`]].join(" ")} role="status">
        <StatusMark kind={status.kind} />
        <span className={styles.statusLabel}>{status.label}</span>
        {status.since && <StatusElapsed since={status.since} />}
        {/* Only while resting: a live state's clause is the thing to read. */}
        {work && RESTING.has(status.kind) && <span className={styles.workNote}>· {workNote(work)}</span>}
      </div>
      {/* One reason, never an inventory: workspace beats branch beats dir. */}
      {reason && (
        <div className={styles.why} data-match-reason={reason.field}>
          <span className={styles.whyKey}>{reason.field}</span>
          {reason.value}
        </div>
      )}
      {/* One subordinate line, not two. The adaptive labels and the badges are
          both "detail under the status", and giving each its own row stacked a
          four-line card out of two lines of content — the labels truncate, the
          badges keep their width, and in the common case they sit together. */}
      {(showContext || hasMeta) && (
        <div className={styles.detail}>
          {showContext && <ContextLine labels={labels} />}
          {hasMeta && (
            <div className={styles.rowMeta}>
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
            </div>
          )}
        </div>
      )}
      {linkModeActive ? (
        <div className={styles.linkModeIndicator}>
          {isLinkAnchor && <span className={styles.linkAnchorBadge}>Anchor</span>}
          {!isLinkAnchor && (
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
        </div>
      ) : (
        <div
          className={styles.actionCluster}
          style={clusterAt}
          data-anchored={clusterAnchored ? "" : undefined}
          role="group"
          aria-label={`${name} actions`}
        >
          {onSleep && (
            // Always offered, regardless of the activity record: it's the
            // Supervisor's TerminateSession underneath, which is safe to call
            // in every state (kills a live child if one exists, or reconciles
            // an already-inactive record as a no-op) — the record can drift
            // from the real child state, so it must never be the thing that
            // hides the one control that recovers from that drift.
            <button
              type="button"
              className={styles.actionButton}
              title="Sleep now — frees the agent process; it wakes on your next message"
              aria-label={`Sleep ${name}`}
              onClick={(e) => {
                e.stopPropagation();
                onSleep(session);
              }}
            >
              ⏸
            </button>
          )}
          {linkedSessionsEnabled && (
            <LinkTrigger
              name={name}
              sessionId={session.id}
              linkCount={linkCount}
              nested={nested}
              linkBadge={linkBadge}
              heldSubjects={heldSubjects}
              onUnlinkSubjectRequest={onUnlinkSubjectRequest}
              cadence={cadence}
              linkId={linkId}
              grantingSessionId={grantingSessionId}
              grants={grants}
              orchestrateStatus={orchestrateStatus}
              orchestrateError={orchestrateError}
              routerClient={routerClient}
              onUnlinkRequest={onUnlinkRequest}
              onStartLinkMode={onStartLinkMode}
            />
          )}
          {onSetDone && !nested && (
            // ✓ is the task-list gesture, and in this rail it means exactly
            // one thing: a judgement the user made. Every other mark on the
            // card reports what the session is doing — which is why the Done
            // status gave its tick up for a closed ring.
            <button
              type="button"
              className={[styles.actionButton, isDone ? styles.actionButtonOn : ""]
                .filter(Boolean)
                .join(" ")}
              title={
                isDone
                  ? "Bring back — return this to the top of the list"
                  : "Done — check this off and put its agent to sleep; it comes back if it needs you"
              }
              aria-label={isDone ? `Bring back ${name}` : `Check off ${name}`}
              aria-pressed={isDone}
              onClick={(e) => {
                e.stopPropagation();
                onSetDone(session, !isDone, working);
              }}
            >
              ✓
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
 * The chain-link glyph every card's link button wears — nested or not,
 * linked or not — tinted and counted once the session has a counted link.
 */
function ChainGlyph({ count }: { count: number }) {
  return (
    <>
      <svg
        className={styles.chainGlyph}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2.2"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" />
        <path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" />
      </svg>
      {count > 0 && (
        <span className={styles.linkCount} aria-hidden="true" data-link-count={count}>
          {count}
        </span>
      )}
    </>
  );
}

/**
 * A card's one link button. Its click still means what the glyph used to
 * spell out — a subject opens "who leads me" (LinkOptionsMenu), a holder
 * opens "who I lead" (LinkHolderMenu), an unlinked card starts link mode —
 * but every card wears the same chain glyph now. Kept as one component so it
 * can be rewired to a single "open links" action in one place.
 */
function LinkTrigger({
  name,
  sessionId,
  linkCount,
  nested,
  linkBadge,
  heldSubjects,
  onUnlinkSubjectRequest,
  cadence,
  linkId,
  grantingSessionId,
  grants,
  orchestrateStatus,
  orchestrateError,
  routerClient,
  onUnlinkRequest,
  onStartLinkMode,
}: {
  name: string;
  sessionId: string;
  linkCount: number;
  nested?: boolean;
  linkBadge?: string;
  heldSubjects?: LinkHolderSubject[];
  onUnlinkSubjectRequest?: (target: { linkId: string; name: string }) => void;
  cadence?: LinkCadence;
  linkId?: string;
  grantingSessionId?: string;
  grants?: Grant[];
  orchestrateStatus?: OrchestrateStatus;
  orchestrateError?: string;
  routerClient?: RouterClient;
  onUnlinkRequest?: () => void;
  onStartLinkMode: () => void;
}) {
  const glyph = <ChainGlyph count={linkCount} />;
  const tint = linkCount > 0 ? styles.linkTriggerLinked : undefined;
  if (nested && linkBadge && cadence && linkId && grantingSessionId && routerClient && onUnlinkRequest) {
    return (
      <LinkOptionsMenu
        badge={linkBadge}
        subjectName={name}
        linkId={linkId}
        grantingSessionId={grantingSessionId}
        subjectSessionId={sessionId}
        grants={grants ?? []}
        cadence={cadence}
        orchestrateStatus={orchestrateStatus}
        orchestrateError={orchestrateError}
        routerClient={routerClient}
        onUnlinkRequest={onUnlinkRequest}
        glyph={glyph}
        triggerClassName={tint}
      />
    );
  }
  if (linkBadge && heldSubjects?.length && onUnlinkSubjectRequest) {
    return (
      <LinkHolderMenu
        holderName={name}
        subjects={heldSubjects}
        onUnlinkRequest={onUnlinkSubjectRequest}
        glyph={glyph}
        triggerClassName={tint}
      />
    );
  }
  if (nested) return null;
  return (
    <button
      type="button"
      className={[styles.actionButton, styles.linkOptionsTrigger, tint].filter(Boolean).join(" ")}
      title={linkCount > 0 ? `Link · ${linkCount} linked` : "Link"}
      aria-label={`Add ${name} to link selection`}
      onClick={(e) => {
        e.stopPropagation();
        onStartLinkMode();
      }}
    >
      {glyph}
    </button>
  );
}

/** Status kinds with nothing live to say — the work note may share the line. */
const RESTING = new Set(["done", "idle"]);

/** The work marker's words: what the dock's Commit/Land control would offer next. */
function workNote(work: NonNullable<WorkState>): string {
  if (work.kind === "commit") return "uncommitted changes";
  if (work.kind === "outside") return "changes outside this session";
  return `${plural(work.commits, "commit")} to land`;
}

/**
 * The context line (mockup docs/mockups/sessions-ux-v2.html §2): where the
 * session belongs, then its branch. One line, never wrapped — the branch
 * truncates first, since the location is the answer the rail is scanned for.
 */
function ContextLine({ labels }: { labels: RailLabels }) {
  const parts = [
    labels.location ? { key: "location", cls: styles.ctxLocation, text: labels.location } : undefined,
    labels.branch ? { key: "branch", cls: styles.ctxBranch, text: labels.branch } : undefined,
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
