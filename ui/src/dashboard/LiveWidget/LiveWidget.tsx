import type { MetricsClient } from "../client/metricsClient";
import { useLiveActivity } from "../state/useLiveActivity";
import styles from "./LiveWidget.module.css";

export interface LiveWidgetProps {
  client: MetricsClient;
}

/** Live "right now" counts, refetched on session/pipeline events (HANDOFF.md §7). */
export function LiveWidget({ client }: LiveWidgetProps) {
  const { activeSessionCount, runningPipelineCount, loading, error } = useLiveActivity(client);

  return (
    <div className={styles.widget}>
      <div className={styles.stat}>
        <span className={styles.value}>{loading ? "—" : activeSessionCount}</span>
        <span className={styles.label}>Active sessions</span>
      </div>
      <div className={styles.stat}>
        <span className={styles.value}>{loading ? "—" : runningPipelineCount}</span>
        <span className={styles.label}>Running pipelines</span>
      </div>
      {error && <div className={styles.error}>{error}</div>}
    </div>
  );
}
