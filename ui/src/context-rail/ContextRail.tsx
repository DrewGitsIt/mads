import { useState } from "react";
import type { RouterClient } from "../router/client";
import type { Task, TaskStatus } from "../router/types";
import { Chip } from "../components/Chip/Chip";
import { Button } from "../components/Button/Button";
import { RailTaskList } from "./RailTaskList";
import { RailChangesList } from "./RailChangesList";
import { RailProposalList } from "./RailProposalList";
import { useProposalStore } from "./useProposalStore";
import styles from "./ContextRail.module.css";

export interface ContextRailProps {
  sessionId: string;
  routerClient: RouterClient;
  tasks: Task[];
  tasksById: Record<string, Task>;
  onSelectTask?: (task: Task) => void;
  onEditDraftTask?: (task: Task) => void;
  onOpenFile?: (path: string) => void;
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
}: ContextRailProps) {
  const [tab, setTab] = useState<RailTab>("tasks");
  const [collapsed, setCollapsed] = useState(false);
  const [statusFilter, setStatusFilter] = useState<TaskStatus | undefined>(undefined);
  const proposals = useProposalStore(routerClient, sessionId);

  if (collapsed) {
    return (
      <div className={styles.collapsed}>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setCollapsed(false)}
          aria-label="Expand context rail"
        >
          ◂
        </Button>
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
          </button>
          <button
            type="button"
            className={tab === "changes" ? styles.tabActive : styles.tab}
            onClick={() => setTab("changes")}
          >
            Changes
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
        <RailChangesList sessionId={sessionId} routerClient={routerClient} onOpenFile={onOpenFile} />
      )}
    </div>
  );
}
