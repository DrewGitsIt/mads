import { useState } from "react";
import { ResizablePanel } from "../../components/ResizablePanel/ResizablePanel";
import type { Task } from "../../router/types";
import type { StageAttempt } from "../state/usePipelineRun";
import type { PipelineOutputSource, PipelineTurnsSource } from "../client/pipelineClient";
import { StageTimeline } from "./StageTimeline";
import { DrillInFeed } from "./DrillInFeed";
import { TaskFiles } from "./TaskFiles";
import { loadDrillInHeight, saveDrillInHeight } from "./drillInHeight";
import styles from "./TaskDrillIn.module.css";

const MIN_HEIGHT = 200;
// Must match PipelineTab.module.css's .boardArea min-height — the sliver of
// board (column headers + a peek of cards) left visible above the drill-in.
const BOARD_SLIVER = 96;
const DEFAULT_HEIGHT = () => window.innerHeight * 0.4;

export interface TaskDrillInProps {
  task: Task;
  attempts: StageAttempt[];
  touchedFiles: string[];
  outputSource: PipelineOutputSource;
  turnsSource: PipelineTurnsSource;
  onClose?: () => void;
  /**
   * Measured height of the pipeline tab's root container (PipelineTab.tsx's
   * ResizeObserver) — the real available space, not window.innerHeight.
   * Optional so existing callers/tests that don't measure a container still
   * get a sensible clamp (the 85vh cap alone).
   */
  tabHeight?: number;
}

/**
 * Docked, resizable drill-in split panel — kit's ResizablePanel with its
 * top-edge drag handle, same pattern as ShellDock (HANDOFF.md §3). Left
 * side is the stage timeline + touched files; right side is the live agent
 * terminal stream.
 */
export function TaskDrillIn({
  task,
  attempts,
  touchedFiles,
  outputSource,
  turnsSource,
  onClose,
  tabHeight = Number.POSITIVE_INFINITY,
}: TaskDrillInProps) {
  // Derived every render (not frozen at mount) so it tracks real layout
  // changes — window resize, DevTools open/close, tab container resize.
  // The 85% cap is against the tab's own measured height, not the whole
  // window — window.innerHeight also counts chrome above the tabpanel
  // (rail, tab bar), which would let the panel grow past the tab's actual
  // bottom edge. Falls back to window.innerHeight only when tabHeight
  // wasn't measured (Infinity default).
  const viewportCap = Number.isFinite(tabHeight) ? tabHeight : window.innerHeight;
  const maxHeight = Math.max(MIN_HEIGHT, Math.min(viewportCap * 0.85, tabHeight - BOARD_SLIVER));
  // Computed once per mount, not on every window resize — an initial
  // default, not a live percentage layout (HANDOFF.md §5) — but clamped
  // against the current maxHeight so a value persisted at a larger viewport
  // never loads past what's actually available now.
  const [defaultHeight] = useState(() =>
    Math.min(Math.max(loadDrillInHeight() ?? DEFAULT_HEIGHT(), MIN_HEIGHT), maxHeight),
  );
  return (
    <ResizablePanel
      direction="vertical"
      defaultSize={defaultHeight}
      minSize={MIN_HEIGHT}
      maxSize={maxHeight}
      onResizeEnd={saveDrillInHeight}
      className={styles.drillIn}
    >
      <div className={styles.header}>
        <span className={styles.title}>{task.title}</span>
        {onClose && (
          <button type="button" className={styles.close} onClick={onClose}>
            close
          </button>
        )}
      </div>
      <div className={styles.body}>
        <div className={styles.left}>
          <StageTimeline attempts={attempts} />
          <TaskFiles touchedFiles={touchedFiles} />
        </div>
        <div className={styles.right}>
          <div className={styles.measure}>
            <DrillInFeed
              key={task.id}
              task={task}
              attempts={attempts}
              outputSource={outputSource}
              turnsSource={turnsSource}
            />
          </div>
        </div>
      </div>
    </ResizablePanel>
  );
}
