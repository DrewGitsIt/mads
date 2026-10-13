import { useState } from "react";
import type { BackgroundTask } from "../../router/types";
import { useElapsedNow } from "../../utils/relativeTime";
import { BackgroundElapsed, BackgroundStrip } from "./Background";
import type { TaskModel } from "./conversationItems";
import { KindIcon, Spinner } from "./primitives";
import { Substrip, type SubstripEntry } from "./Substrip";
import { TaskCounters } from "./TaskCard";
import { formatElapsed } from "./toolDisplay";
import styles from "./Substrip.module.css";

/** Up to this many items keep a full row each; past it they fold into one. */
export const RUNNING_ROWS_MAX = 2;

/**
 * The pinned running items: subagents whose card is off screen, then
 * background processes whose call's row is (docs/mockups/
 * pinned-running-stack.html, option 1). A few keep today's full rows; more
 * fold into ONE summary row — counts, the first subagent's current step,
 * the oldest item's clock — so a session running many costs the
 * conversation one row's height, however many there are. Clicking it opens
 * the list above it, capped and scrolling on its own: subagents first, each
 * with the processes it started nested under it, then the main agent's.
 *
 * `processes` may hold a subagent's own process (parent_tool_use_id); one is
 * drawn under its subagent, and only when that subagent is listed here.
 */
export function RunningStack({
  agents,
  processes,
  onOpen,
}: {
  agents: SubstripEntry[];
  processes: BackgroundTask[];
  onOpen: (model: TaskModel) => void;
}) {
  const [open, setOpen] = useState(false);
  const listed = new Set(agents.map((a) => a.model.toolUseId).filter(Boolean));
  const own = processes.filter((p) => !p.parent_tool_use_id);
  const nested = processes.filter((p) => p.parent_tool_use_id && listed.has(p.parent_tool_use_id));
  const count = agents.length + own.length + nested.length;
  if (count === 0) return null;

  if (count <= RUNNING_ROWS_MAX) {
    return (
      <>
        <Substrip entries={agents} onOpen={onOpen} />
        <BackgroundStrip entries={[...nested, ...own]} />
      </>
    );
  }

  const procs = own.length + nested.length;
  const counts = [
    agents.length > 0 && `${agents.length} ${agents.length === 1 ? "subagent" : "subagents"}`,
    procs > 0 && `${procs} ${procs === 1 ? "process" : "processes"}`,
  ].filter(Boolean);
  const lead = agents[0];
  const leadName = lead && (lead.model.description ?? lead.model.subagentType ?? "Subagent");
  const starts = [
    ...agents.map((a) => a.model.startedAt),
    ...own.map((p) => p.started_at),
    ...nested.map((p) => p.started_at),
  ].filter((s): s is string => !!s);

  return (
    <div data-testid="running-stack">
      {open && (
        <div className={styles.panel} data-testid="running-list">
          {agents.length > 0 && (
            <>
              <div className={styles.group}>Subagents</div>
              {agents.map(({ model, op }) => (
                <div key={model.taskId}>
                  <button
                    type="button"
                    className={`${styles.li} ${styles.liAgent}`}
                    data-dock-source
                    title="Open subagent stream"
                    onClick={() => onOpen(model)}
                  >
                    <KindIcon kind="agent" size="sm" />
                    <span className={styles.name}>{model.description ?? model.subagentType ?? "Subagent"}</span>
                    {op && <span className={styles.op}>{op}</span>}
                    <span className={styles.counters}>
                      <span>
                        <TaskCounters model={model} />
                      </span>
                      <Spinner />
                    </span>
                  </button>
                  {nested
                    .filter((p) => p.parent_tool_use_id === model.toolUseId)
                    .map((p) => (
                      <ProcessRow key={p.task_id} task={p} nested />
                    ))}
                </div>
              ))}
            </>
          )}
          {own.length > 0 && (
            <>
              <div className={styles.group}>Processes</div>
              {own.map((p) => (
                <ProcessRow key={p.task_id} task={p} />
              ))}
            </>
          )}
        </div>
      )}
      <button
        type="button"
        className={styles.strip}
        aria-expanded={open}
        data-testid="running-summary"
        onClick={() => setOpen((o) => !o)}
      >
        <Spinner />
        <span className={styles.name}>{counts.join(" · ")} running</span>
        {lead && (
          <span className={styles.op}>
            {leadName}
            {lead.op ? ` — ${lead.op}` : ""}
          </span>
        )}
        <span className={styles.counters}>
          <OldestElapsed starts={starts} />
          <span className={styles.chev} aria-hidden="true">
            {open ? "▾" : "▴"}
          </span>
        </span>
      </button>
    </div>
  );
}

function ProcessRow({ task, nested }: { task: BackgroundTask; nested?: boolean }) {
  return (
    <div className={`${styles.li} ${nested ? styles.liNested : ""}`}>
      <KindIcon kind="execute" size="sm" />
      <span className={styles.op}>{task.description ?? "Background process"}</span>
      <span className={styles.counters}>
        <BackgroundElapsed since={task.started_at} />
      </span>
    </div>
  );
}

/** The longest-running item's clock, on the shared 1s tick. */
function OldestElapsed({ starts }: { starts: string[] }) {
  const now = useElapsedNow();
  const oldest = Math.min(...starts.map((s) => Date.parse(s)).filter((t) => Number.isFinite(t)));
  if (!Number.isFinite(oldest)) return null;
  return <span>{formatElapsed(now - oldest)}</span>;
}
