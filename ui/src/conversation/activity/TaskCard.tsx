import type { ToolUse } from "../../router/types";
import type { TaskModel } from "./conversationItems";
import { KindIcon, Spinner, StatusGlyph } from "./primitives";
import { formatMs, formatTokens } from "./toolDisplay";
import styles from "./TaskCard.module.css";

export interface TaskCardProps {
  /** The spawning Agent tool_use — the card's anchor in the stream. */
  tool: ToolUse;
  /** Folded task lifecycle (task_started/progress/updated/notification), when the harness reported one. */
  model?: TaskModel;
  onOpen?: () => void;
}

export function taskCounters(model: TaskModel): string {
  const parts: string[] = [];
  if (model.totalTokens !== undefined) parts.push(`${formatTokens(model.totalTokens)} tok`);
  if (model.toolUses !== undefined) parts.push(`${model.toolUses} tools`);
  if (model.durationMs !== undefined) parts.push(formatMs(model.durationMs));
  return parts.join(" · ");
}

function inputField(tool: ToolUse, key: string): string | undefined {
  if (!tool.input || typeof tool.input !== "object") return undefined;
  const v = (tool.input as Record<string, unknown>)[key];
  return typeof v === "string" && v !== "" ? v : undefined;
}

/**
 * Compact in-stream Task subagent card (T15): agent icon, description,
 * subagent type and live counters while running; the completed variant
 * carries the task_notification summary inline. A replayed task that never
 * saw a notification renders interrupted-shaped — never a fake spinner.
 * Click opens the subagent's own stream in the dock.
 */
export function TaskCard({ tool, model, onOpen }: TaskCardProps) {
  const description =
    model?.description ?? tool.title ?? inputField(tool, "description") ?? "Subagent task";
  const subtype = model?.subagentType ?? inputField(tool, "subagent_type");
  const counters = model ? taskCounters(model) : "";
  const running = model?.running ?? false;
  // Without lifecycle events (foreign harness), fall back to the tool
  // result: a result means the Agent call finished.
  const completed = model ? model.completed : Boolean(tool.result && !tool.result.is_error);
  const failed = !model && Boolean(tool.result?.is_error);
  const summary = model?.summary;

  return (
    <div className={styles.card} data-testid="task-card">
      <button type="button" className={styles.head} data-dock-source onClick={onOpen}>
        <KindIcon kind="agent" />
        <span className={styles.title}>
          {description}
          {subtype && <span className={styles.sub}> · {subtype}</span>}
        </span>
        <span className={styles.meta}>
          {counters && <span data-testid="task-counters">{counters}</span>}
          {running ? (
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
    </div>
  );
}
