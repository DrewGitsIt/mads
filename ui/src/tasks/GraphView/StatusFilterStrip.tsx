import { Chip } from "../../components/Chip/Chip";
import type { Task } from "../../router/types";
import { GRAPH_STATUS_CHIP_TONE, GRAPH_STATUS_ORDER, graphStatusOf, type GraphStatus } from "./graphStatus";
import { StatusGlyph } from "./StatusGlyph";
import styles from "./StatusFilterStrip.module.css";

const STATUS_STRIP_LABEL: Record<GraphStatus, string> = {
  done: "done",
  in_progress: "in progress",
  ready: "ready",
  blocked: "blocked",
  draft: "draft",
};

export interface StatusFilterStripProps {
  tasks: Task[];
  activeFilter: GraphStatus | null;
  onHoverStatus: (status: GraphStatus | null) => void;
  onToggleFilter: (status: GraphStatus) => void;
}

/**
 * Renders all 5 buckets always, even at count 0 — fixed layout, no shift as
 * counts change live, and "0 blocked" is itself useful at-a-glance info.
 */
export function StatusFilterStrip({ tasks, activeFilter, onHoverStatus, onToggleFilter }: StatusFilterStripProps) {
  const counts = new Map<GraphStatus, number>(GRAPH_STATUS_ORDER.map((s) => [s, 0]));
  for (const task of tasks) {
    const s = graphStatusOf(task);
    counts.set(s, (counts.get(s) ?? 0) + 1);
  }
  return (
    <div className={styles.strip}>
      {GRAPH_STATUS_ORDER.map((status) => (
        <Chip
          key={status}
          tone={GRAPH_STATUS_CHIP_TONE[status]}
          pressed={activeFilter === status}
          onClick={() => onToggleFilter(status)}
          onMouseEnter={() => onHoverStatus(status)}
          onMouseLeave={() => onHoverStatus(null)}
        >
          <StatusGlyph status={status} />
          <b>{counts.get(status)}</b> {STATUS_STRIP_LABEL[status]}
        </Chip>
      ))}
    </div>
  );
}
