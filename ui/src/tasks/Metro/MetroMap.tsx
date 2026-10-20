import { useMemo, useState } from "react";
import type { CSSProperties } from "react";
import type { Task } from "../../router/types";
import type { PickupInfo, RunMode } from "../../pipeline/plan/runStatus";
import type { RunStage } from "../../pipeline/plan/runStages";
import type { TaskRunState } from "../../pipeline/state/usePipelineRun";
import { Popover } from "../../components/Popover/Popover";
import { RunStatusGlyph } from "../RunList/RunStatusGlyph";
import { metroLayout, TOKEN_H, type MetroStack, type MetroToken, type StationFocus } from "./metroLayout";
import styles from "./MetroMap.module.css";

export interface MetroMapProps {
  tasks: Task[];
  tasksById: Record<string, Task>;
  mode: RunMode;
  /** Plan: the include boxes left unticked. Live/past: the run's excluded_task_ids. */
  excluded: ReadonlySet<string>;
  taskRunState: Record<string, TaskRunState>;
  stages: RunStage[];
  /** The task being zoomed into: its token gets a ring and its path through the stations lights up. */
  focusTaskId?: string;
  onSelectTask: (id: string) => void;
  /** Plan and live: the session's Interrupted tasks and where a run picks each up. */
  pickup?: PickupInfo;
}

const FOCUS_CLASS: Record<StationFocus, string | undefined> = {
  now: styles.pathNow,
  visited: styles.path,
  dim: styles.dim,
};

const plural = (n: number) => `${n} task${n === 1 ? "" : "s"}`;

/** The run's template as a metro line, with tasks riding it as tokens. */
export function MetroMap({
  tasks,
  tasksById,
  mode,
  excluded,
  taskRunState,
  stages,
  focusTaskId,
  onSelectTask,
  pickup,
}: MetroMapProps) {
  const layout = useMemo(
    () => metroLayout({ tasks, tasksById, mode, excluded, taskRunState, stages, focusTaskId, pickup }),
    [tasks, tasksById, mode, excluded, taskRunState, stages, focusTaskId, pickup],
  );
  const [openStack, setOpenStack] = useState<string | null>(null);
  const { geometry: g } = layout;

  const stackProps = { openStack, setOpenStack, focusTaskId, onSelectTask };
  // Stacks above the line grow upward from just over it.
  const aboveStyle = (x: number, width: number): CSSProperties => ({
    left: `${x}%`,
    bottom: g.height - (g.lineY - 15),
    maxWidth: `${width}%`,
  });

  return (
    <div className={styles.metro} style={{ height: g.height }} data-testid="metro">
      <svg className={styles.arcs} viewBox={`0 0 1000 ${g.height}`} preserveAspectRatio="none" aria-hidden="true">
        {layout.arcs.map((arc) => (
          <path key={`${arc.from}>${arc.to}`} d={arc.path} className={styles.arc} vectorEffect="non-scaling-stroke" />
        ))}
      </svg>
      {layout.arcs.map((arc) => (
        <span
          key={`head:${arc.from}>${arc.to}`}
          aria-hidden="true"
          className={styles.arrow}
          style={{ left: `${arc.toX}%`, top: g.arcTop - 1 }}
        />
      ))}

      <div
        className={styles.line}
        style={{ left: `${layout.startX}%`, right: `${100 - layout.endX}%`, top: g.lineY - 3 }}
      />

      <Station
        className={[styles.terminus, layout.startFocus && FOCUS_CLASS[layout.startFocus]]}
        x={layout.startX}
        top={g.lineY}
        name={layout.startLabel}
        sub={plural(layout.start.tokens.length)}
      />
      {layout.stations.map((s) => (
        <Station
          key={s.stage.name}
          className={[
            s.stage.kind === "command" && styles.command,
            s.hot && styles.hot,
            s.focus && FOCUS_CLASS[s.focus],
          ]}
          x={s.x}
          top={g.lineY}
          maxWidth={layout.labelWidth}
          name={s.stage.name}
          sub={s.stage.kind}
        />
      ))}
      <Station
        className={[
          styles.terminus,
          layout.done.tokens.length > 0 && styles.reached,
          layout.endFocus && FOCUS_CLASS[layout.endFocus],
        ]}
        x={layout.endX}
        top={g.lineY}
        name="Done"
        sub={plural(layout.done.tokens.length)}
      />

      {layout.arcs.map((arc) => (
        <span
          key={`label:${arc.from}>${arc.to}`}
          className={styles.arcLabel}
          style={{ left: `${arc.labelX}%`, top: arc.bottom - 7 }}
          title={`A failure at ${arc.from} sends the task back to ${arc.to}`}
        >
          {arc.label}
        </span>
      ))}

      {layout.start.tokens.length > 0 && (
        <Stack stack={layout.start} style={aboveStyle(layout.start.x, layout.terminusWidth)} {...stackProps} />
      )}
      {layout.above.map((stack) => (
        <Stack key={stack.key} stack={stack} style={aboveStyle(stack.x, layout.labelWidth)} {...stackProps} />
      ))}
      {layout.done.tokens.length > 0 && (
        <Stack
          stack={layout.done}
          style={aboveStyle(layout.done.x, layout.terminusWidth)}
          popoverRight
          {...stackProps}
        />
      )}

      {layout.blocked.map((stack) => (
        <div
          key={stack.key}
          className={styles.below}
          style={{ left: `${stack.x}%`, top: g.blockedTop, maxWidth: `${layout.labelWidth}%` }}
        >
          <span className={styles.blockedCaption}>Blocked</span>
          <Stack stack={stack} inline {...stackProps} />
        </div>
      ))}

      {layout.out.tokens.length > 0 && (
        <div className={styles.outRow} style={{ top: g.outTop, height: TOKEN_H + 4 }}>
          <span className={styles.caption}>Not in this run</span>
          <Stack stack={layout.out} inline row {...stackProps} />
        </div>
      )}
    </div>
  );
}

function Station({
  className,
  x,
  top,
  maxWidth,
  name,
  sub,
}: {
  className: (string | false | undefined)[];
  x: number;
  top: number;
  maxWidth?: number;
  name: string;
  sub: string;
}) {
  return (
    <div
      className={[styles.station, ...className].filter(Boolean).join(" ")}
      style={{ left: `${x}%`, top: top - 10, maxWidth: maxWidth ? `${maxWidth}%` : undefined }}
      data-station={name}
    >
      <span className={styles.ring} />
      <span className={styles.name} title={name}>
        {name}
      </span>
      <span className={styles.sub}>{sub}</span>
    </div>
  );
}

interface StackProps {
  stack: MetroStack;
  openStack: string | null;
  setOpenStack: (key: string | null) => void;
  focusTaskId?: string;
  onSelectTask: (id: string) => void;
  style?: CSSProperties;
  /** Laid out by its parent (below the line, the out row) rather than positioned itself. */
  inline?: boolean;
  /** Tokens side by side rather than stacked. */
  row?: boolean;
  /** Open the full list leftward (a stack near the right edge). */
  popoverRight?: boolean;
}

function Stack({ stack, openStack, setOpenStack, focusTaskId, onSelectTask, style, inline, row, popoverRight }: StackProps) {
  const open = openStack === stack.key;
  const close = () => setOpenStack(null);
  return (
    <div
      className={[styles.stack, inline ? styles.inline : styles.up, row && styles.row].filter(Boolean).join(" ")}
      style={style}
      data-stack={stack.key}
    >
      {stack.shown.map((t) => (
        <Token key={t.id} token={t} focused={t.id === focusTaskId} onSelect={onSelectTask} />
      ))}
      {stack.hidden > 0 && (
        <Popover
          open={open}
          onClose={close}
          className={styles.stackList}
          style={popoverRight ? { left: "auto", right: 0 } : undefined}
          anchor={
            <button
              type="button"
              className={[styles.token, styles.more].join(" ")}
              aria-expanded={open}
              aria-haspopup="dialog"
              onClick={() => setOpenStack(open ? null : stack.key)}
            >
              +{stack.hidden} more
            </button>
          }
        >
          <StackList
            stack={stack}
            onPick={(id) => {
              close();
              onSelectTask(id);
            }}
          />
        </Popover>
      )}
    </div>
  );
}

function Token({ token, focused, onSelect }: { token: MetroToken; focused: boolean; onSelect: (id: string) => void }) {
  return (
    <button
      type="button"
      className={[styles.token, styles[token.status], focused && styles.focus].filter(Boolean).join(" ")}
      title={token.status === "interrupted" ? `${token.task.title} · Interrupted` : token.task.title}
      data-task={token.id}
      data-status={token.status}
      aria-pressed={focused}
      onClick={() => onSelect(token.id)}
    >
      {token.status === "running" && <span className={styles.spin} aria-hidden="true" />}
      <span className={styles.tokenLabel}>{token.label}</span>
      {token.attempt > 1 && <span className={styles.attempt}>#{token.attempt}</span>}
    </button>
  );
}

function StackList({ stack, onPick }: { stack: MetroStack; onPick: (id: string) => void }) {
  const [filter, setFilter] = useState("");
  const q = filter.trim().toLowerCase();
  const items = q ? stack.tokens.filter((t) => t.task.title.toLowerCase().includes(q)) : stack.tokens;
  return (
    <div role="dialog" aria-label={`${stack.heading}: ${plural(stack.tokens.length)}`}>
      <div className={styles.listHead}>
        <span>
          {stack.heading} · {stack.tokens.length}
        </span>
        <kbd>Esc</kbd>
      </div>
      <input
        className={styles.filter}
        placeholder="Filter…"
        aria-label="Filter tasks"
        value={filter}
        autoFocus
        onChange={(e) => setFilter(e.target.value)}
      />
      <ul className={styles.items}>
        {items.map((t) => (
          <li key={t.id}>
            <button type="button" className={styles.item} onClick={() => onPick(t.id)}>
              <RunStatusGlyph status={t.status} />
              <span className={styles.itemTitle}>{t.task.title}</span>
              {t.attempt > 1 && <span className={styles.itemMeta}>#{t.attempt}</span>}
            </button>
          </li>
        ))}
        {items.length === 0 && <li className={styles.none}>No matching tasks</li>}
      </ul>
    </div>
  );
}
