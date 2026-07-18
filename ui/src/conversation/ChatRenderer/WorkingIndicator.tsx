import type { ToolUse } from "../../router/types";
import styles from "./WorkingIndicator.module.css";

/** Pre-first-chunk "agent is working" affordance; names the tool once tool_use has arrived. */
export function WorkingIndicator({ tool }: { tool?: ToolUse }) {
  const label = tool ? `Using ${tool.name}…` : "Working…";
  return (
    <div className={styles.working} role="status" aria-label={label}>
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.label}>{label}</span>
    </div>
  );
}
