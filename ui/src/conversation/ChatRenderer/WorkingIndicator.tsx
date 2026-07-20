import { Chip } from "../../components/Chip/Chip";
import type { ToolUse } from "../../router/types";
import styles from "./WorkingIndicator.module.css";

/**
 * Pre-first-chunk "agent is working" affordance; names the tool once
 * tool_use has arrived. `blocked` overrides the pulsing-dots animation with
 * a system-toned banner when the provider reports a tool call stalled
 * awaiting permission rather than completing — see providers/claudecode's
 * README ("Defense in depth") for why this must never be a silent no-op.
 */
export function WorkingIndicator({ tool, blocked }: { tool?: ToolUse; blocked?: string }) {
  if (blocked) {
    return (
      <div className={styles.working} role="status">
        <Chip tone="bad">{blocked}</Chip>
      </div>
    );
  }
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
