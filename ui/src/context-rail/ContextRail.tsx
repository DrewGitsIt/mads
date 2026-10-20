import { useState } from "react";
import type { RouterClient } from "../router/client";
import type { Task, TaskStatus } from "../router/types";
import { Chip } from "../components/Chip/Chip";
import { Button } from "../components/Button/Button";
import { RailTaskList } from "./RailTaskList";
import { RailChangesList } from "./RailChangesList";
import { RailProposalList } from "./RailProposalList";
import { countNonScratch, type ChangesFocus } from "../files/changesModel";
import { useSeenState } from "../files/seenState";
import { useChangeSet } from "../files/useChangeSet";
import { useProposalStore } from "./useProposalStore";
import styles from "./ContextRail.module.css";

export interface ContextRailProps {
  sessionId: string;
  routerClient: RouterClient;
  tasks: Task[];
  tasksById: Record<string, Task>;
  onSelectTask?: (task: Task) => void;
  onEditDraftTask?: (task: Task) => void;
  /** Opens a file's diff in the Changes tab — used when onOpenChanges isn't given. */
  onOpenFile?: (path: string) => void;
  /** Opens the Changes tab on a file, or on the rail's tier and turn ("Open in Changes"). */
  onOpenChanges?: (focus: ChangesFocus) => void;
}

type RailTab = "tasks" | "changes";

const QUICK_FILTERS: { label: string; status?: TaskStatus }[] = [
  { label: "All" },
  { label: "In progress", status: "in_progress" },
  { label: "Ready", status: "ready" },
  { label: "Review", status: "needs_review" },
  { label: "Done", status: "done" },
];

/**
 * Constant across all Session Workspace display modes (MADS-UX §3.3), not
 * Tasks-tab-specific — shares the tasks/tasksById state the main Tasks tab
 * owns (via useTaskStore, one level up) rather than opening a second
 * GET /events subscription for the same session (§7).
 */
export function ContextRail({
  sessionId,
  routerClient,
  tasks,
  tasksById,
  onSelectTask,
  onEditDraftTask,
  onOpenFile,
  onOpenChanges,
}: ContextRailProps) {
  const [tab, setTab] = useState<RailTab>("tasks");
  // Starts collapsed: the conversation owns the pane until the user asks for
  // the rail, and the collapsed strip's counts say when that's worth doing.
  const [collapsed, setCollapsed] = useState(true);
  const [statusFilter, setStatusFilter] = useState<TaskStatus | undefined>(undefined);
  const proposals = useProposalStore(routerClient, sessionId);

  // Fetched here (not in RailChangesList) so the Changes tab can show the
  // file count even while the Tasks tab is active — otherwise the tab gives
  // no hint of what's behind it. Scratch files aren't counted.
  const { changeSet, loading: changesLoading, error: changesError } = useChangeSet(routerClient, sessionId);
  const { isUnseen } = useSeenState(sessionId);
  const changedCount = countNonScratch(changeSet);
  const openChanges =
    onOpenChanges ?? (onOpenFile ? (focus: ChangesFocus) => focus.path && onOpenFile(focus.path) : undefined);
  const openTasks = tasks.filter((t) => t.status !== "done" && t.status !== "wont_do").length;
  const pendingProposals = proposals.filter((p) => p.status === "proposed").length;

  if (collapsed) {
    const open = (next?: RailTab) => {
      if (next) setTab(next);
      setCollapsed(false);
    };
    // The whole strip expands on click (a tall, easy target); the buttons
    // inside carry the keyboard/a11y path and pick which tab opens.
    return (
      <div className={styles.collapsed} onClick={() => open()} title="Show tasks and changes">
        <button type="button" className={styles.expand} aria-label="Expand context rail">
          ◂
        </button>
        <button
          type="button"
          className={styles.peek}
          onClick={() => setTab("tasks")}
          aria-label={`Tasks, ${openTasks} open`}
        >
          Tasks
          <span className={styles.count}>{openTasks}</span>
        </button>
        <button
          type="button"
          className={styles.peek}
          onClick={() => setTab("changes")}
          aria-label={`Changes, ${changedCount} files`}
        >
          Changes
          <span className={styles.count}>{changedCount}</span>
        </button>
        {pendingProposals > 0 && (
          <span
            className={styles.proposals}
            aria-label={`${pendingProposals} pending proposals`}
            title={`${pendingProposals} pending proposals`}
          >
            {pendingProposals}
          </span>
        )}
      </div>
    );
  }

  return (
    <div className={styles.rail}>
      <div className={styles.header}>
        <div className={styles.tabs}>
          <button
            type="button"
            className={tab === "tasks" ? styles.tabActive : styles.tab}
            onClick={() => setTab("tasks")}
          >
            Tasks
            {openTasks > 0 && (
              <span className={styles.count} aria-hidden="true">
                {openTasks}
              </span>
            )}
          </button>
          <button
            type="button"
            className={tab === "changes" ? styles.tabActive : styles.tab}
            onClick={() => setTab("changes")}
          >
            Changes
            {changedCount > 0 && (
              <span className={styles.count} aria-hidden="true">
                {changedCount}
              </span>
            )}
          </button>
        </div>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setCollapsed(true)}
          aria-label="Collapse context rail"
        >
          ▸
        </Button>
      </div>

      <RailProposalList proposals={proposals} tasksById={tasksById} routerClient={routerClient} />

      {tab === "tasks" && (
        <>
          <div className={styles.filters}>
            {QUICK_FILTERS.map(({ label, status }) => (
              <Chip
                key={label}
                tone="neutral"
                pressed={statusFilter === status}
                onClick={() => setStatusFilter(status)}
              >
                {label}
              </Chip>
            ))}
          </div>
          <RailTaskList
            tasks={tasks}
            tasksById={tasksById}
            routerClient={routerClient}
            statusFilter={statusFilter}
            onSelectTask={onSelectTask}
            onEditDraftTask={onEditDraftTask}
          />
        </>
      )}

      {tab === "changes" && (
        <RailChangesList
          changeSet={changeSet}
          loading={changesLoading}
          error={changesError}
          isUnseen={isUnseen}
          onOpen={openChanges}
        />
      )}
    </div>
  );
}
