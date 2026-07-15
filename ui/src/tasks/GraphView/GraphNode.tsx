import type { Task } from "../../router/types";
import { statusPresentation } from "../ListView/statusPresentation";
import type { GraphNodeLayout } from "./layout";
import styles from "./GraphNode.module.css";

export interface GraphNodeProps {
  node: GraphNodeLayout;
  tasksById: Record<string, Task>;
  onClick?: (task: Task) => void;
}

const TONE_VAR: Record<string, string> = {
  neutral: "var(--line)",
  ok: "var(--ok)",
  warn: "var(--warn)",
  bad: "var(--bad)",
  info: "var(--info)",
  draft: "var(--draft)",
};

export function GraphNode({ node, tasksById, onClick }: GraphNodeProps) {
  const presentation = statusPresentation(node.task, tasksById);
  const stroke = TONE_VAR[presentation.chipTone] ?? TONE_VAR.neutral;

  return (
    <g
      className={styles.node}
      transform={`translate(${node.x - node.width / 2}, ${node.y - node.height / 2})`}
      role={onClick ? "button" : undefined}
      tabIndex={onClick ? 0 : undefined}
      onClick={onClick ? () => onClick(node.task) : undefined}
      data-status={presentation.label}
    >
      <rect
        width={node.width}
        height={node.height}
        rx={8}
        className={styles.rect}
        style={{
          stroke,
          strokeDasharray: presentation.dashedRing ? "4 3" : undefined,
        }}
      />
      <text x={node.width / 2} y={node.height / 2} className={styles.title}>
        {node.task.title}
      </text>
    </g>
  );
}
