import type { PermissionRequest, ToolStatus, ToolUse } from "../../router/types";
import { PermissionCard } from "../ChatRenderer/PermissionCard";
import type { TaskModel } from "./conversationItems";
import { KindIcon, Spinner, StatusGlyph } from "./primitives";
import { formatElapsed, formatMs, formatTokens } from "./toolDisplay";
import { useElapsedNow } from "../../utils/relativeTime";
import styles from "./TaskCard.module.css";

export interface TaskCardProps {
  /** The spawning Agent tool_use — the card's anchor in the stream. */
  tool: ToolUse;
  /** Folded task lifecycle (task_started/progress/updated/notification), when the harness reported one. */
  model?: TaskModel;
  /** The Agent call's own stated status (wire or out-of-band) — "blocked" beats everything. */
  status?: ToolStatus;
  /**
   * Permission requests anchored here: the subagent's own calls (its inner
   * rows live only in the dock, so this card is where they stay visible)
   * plus any request for the Agent call itself.
   */
  permissions?: PermissionRequest[];
  onResolvePermission?: (id: string, decision: "allow" | "deny") => Promise<void>;
  onOpen?: () => void;
}

const noResolve = async () => {};

/** Hover text for an ended delegate: what ended it, and whether mads had to conclude that. */
function stateTitle(model?: TaskModel): string | undefined {
  if (!model?.state || model.state === "running") return undefined;
  const why = model.status ? ` (${model.status.replace("_", " ")})` : "";
  return model.inferred ? `${model.state}${why} — concluded by mads, no report from the agent` : `${model.state}${why}`;
}

/**
 * The card's counters. With `now` (a running task), the duration is a live
 * clock from the run's start — the harness only reports duration_ms when a
 * subagent tool finishes, so read alone it moved in steps between tools.
 */
export function taskCounters(model: TaskModel, now?: number): string {
  const parts: string[] = [];
  if (model.totalTokens !== undefined) parts.push(`${formatTokens(model.totalTokens)} tok`);
  if (model.toolUses !== undefined) parts.push(`${model.toolUses} tools`);
  const started = model.startedAt ? Date.parse(model.startedAt) : NaN;
  if (now !== undefined && model.running && Number.isFinite(started)) {
    parts.push(formatElapsed(Math.max(model.durationMs ?? 0, now - started)));
  } else if (model.durationMs !== undefined) {
    parts.push(formatMs(model.durationMs));
  }
  return parts.join(" · ");
}

/** taskCounters as a component: a running task's re-render on the shared 1s clock. */
export function TaskCounters({ model }: { model: TaskModel }) {
  return model.running ? <LiveTaskCounters model={model} /> : <>{taskCounters(model)}</>;
}

function LiveTaskCounters({ model }: { model: TaskModel }) {
  const now = useElapsedNow();
  return <>{taskCounters(model, now)}</>;
}

/**
 * The status glyph for a task's state. A delegate record (model.state) says
 * how it ended — failed and stopped included, which the task-line fold used
 * to render as a green check (any notification) or an interrupted mark.
 */
export function TaskStatusGlyph({ model, blocked }: { model?: TaskModel; blocked?: boolean }) {
  if (blocked) return <StatusGlyph status="blocked" />;
  switch (model?.state) {
    case "running":
      return <Spinner />;
    case "completed":
      return <StatusGlyph status="completed" />;
    case "failed":
      return <StatusGlyph status="failed" />;
    case "stopped":
    case "orphaned":
      return <StatusGlyph status="interrupted" />;
  }
  return null;
}

function inputField(tool: ToolUse, key: string): string | undefined {
  if (!tool.input || typeof tool.input !== "object") return undefined;
  const v = (tool.input as Record<string, unknown>)[key];
  return typeof v === "string" && v !== "" ? v : undefined;
}

/**
 * Compact in-stream Task subagent card (T15): agent icon, description,
 * subagent type, current step and live counters while running; the
 * completed variant carries the task_notification summary inline. A replayed
 * task that never saw a notification renders interrupted-shaped — never a
 * fake spinner. Click opens the subagent's own stream in the dock.
 */
export function TaskCard({
  tool,
  model,
  status,
  permissions,
  onResolvePermission,
  onOpen,
}: TaskCardProps) {
  const description =
    model?.description ?? tool.title ?? inputField(tool, "description") ?? "Subagent task";
  const subtype = model?.subagentType ?? inputField(tool, "subagent_type");
  const counters = model ? taskCounters(model) : "";
  // A refused spawn never ran: blocked wins over the lifecycle and over the
  // refusal's is_error result alike.
  const blocked = (status ?? tool.status) === "blocked";
  const running = !blocked && (model?.running ?? false);
  // Without lifecycle events (foreign harness), fall back to the tool
  // result: a result means the Agent call finished.
  const completed = model ? model.completed : Boolean(tool.result && !tool.result.is_error);
  const failed = !model && Boolean(tool.result?.is_error);
  const summary = model?.summary;

  return (
    <div className={styles.card} data-testid="task-card" data-task-id={model?.taskId}>
      <button type="button" className={styles.head} data-dock-source onClick={onOpen}>
        <KindIcon kind="agent" />
        <span className={styles.title}>
          {description}
          {subtype && <span className={styles.sub}> · {subtype}</span>}
        </span>
        {running && model?.currentStep && (
          <span className={styles.step} key={model.currentStep}>
            {model.currentStep}
          </span>
        )}
        <span className={styles.meta} title={stateTitle(model)}>
          {counters && (
            <span data-testid="task-counters">
              <TaskCounters model={model!} />
            </span>
          )}
          {blocked || model?.state ? (
            <TaskStatusGlyph model={model} blocked={blocked} />
          ) : running ? (
            <Spinner />
          ) : completed ? (
            <StatusGlyph status="completed" />
          ) : failed ? (
            <StatusGlyph status="failed" />
          ) : (
            <StatusGlyph status="interrupted" />
          )}
        </span>
      </button>
      {summary && <div className={styles.summary}>{summary}</div>}
      {permissions?.map((req) => (
        <div key={req.id} className={styles.permission}>
          <PermissionCard request={req} onResolve={onResolvePermission ?? noResolve} />
        </div>
      ))}
    </div>
  );
}
