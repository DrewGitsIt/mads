import type { BackgroundTask } from "../../router/types";
import { useElapsedNow } from "../../utils/relativeTime";
import { KindIcon, Spinner } from "./primitives";
import { formatElapsed } from "./toolDisplay";
import styles from "./Substrip.module.css";

/** How long a background process has been running, on the shared 1s tick. */
export function BackgroundElapsed({ since }: { since: string }) {
  const now = useElapsedNow();
  const start = Date.parse(since);
  if (Number.isNaN(start)) return null;
  return <span>running · {formatElapsed(now - start)}</span>;
}

/**
 * Pinned background rows: one per running background process (a
 * run_in_background Bash call, a Monitor watch) whose card is off screen, so
 * a session that looks idle shows what it's still doing. No action: the card
 * is where the detail lives. Named by the harness's description: the command,
 * or what a Monitor watches.
 */
export function BackgroundStrip({ entries }: { entries: BackgroundTask[] }) {
  if (entries.length === 0) return null;
  return (
    <div data-testid="background-strip">
      {entries.map((task) => (
        <div key={task.task_id} className={`${styles.strip} ${styles.passive}`}>
          <KindIcon kind="execute" />
          <span className={styles.op}>{task.description ?? "Background process"}</span>
          <span className={styles.counters}>
            <BackgroundElapsed since={task.started_at} />
            <Spinner />
          </span>
        </div>
      ))}
    </div>
  );
}
