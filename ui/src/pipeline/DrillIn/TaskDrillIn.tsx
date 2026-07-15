import { ResizablePanel } from "../../components/ResizablePanel/ResizablePanel";
import type { Task } from "../../router/types";
import type { StageAttempt } from "../state/usePipelineRun";
import type { PipelineOutputSource } from "../client/pipelineClient";
import { StageTimeline } from "./StageTimeline";
import { AgentOutputView } from "./AgentOutputView";
import { TaskFiles } from "./TaskFiles";
import styles from "./TaskDrillIn.module.css";

export interface TaskDrillInProps {
  task: Task;
  attempts: StageAttempt[];
  touchedFiles: string[];
  outputSource: PipelineOutputSource;
  onClose?: () => void;
}

/**
 * Docked, resizable drill-in split panel — kit's ResizablePanel with its
 * top-edge drag handle, same pattern as ShellDock (HANDOFF.md §3). Left
 * side is the stage timeline + touched files; right side is the live agent
 * terminal stream.
 */
export function TaskDrillIn({ task, attempts, touchedFiles, outputSource, onClose }: TaskDrillInProps) {
  return (
    <ResizablePanel direction="vertical" defaultSize={320} className={styles.drillIn}>
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
          <AgentOutputView key={task.id} source={outputSource} />
        </div>
      </div>
    </ResizablePanel>
  );
}
