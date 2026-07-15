import type { AgentLeaderboardEntry } from "../../router/types";
import styles from "./AgentLeaderboardTable.module.css";

export interface AgentLeaderboardTableProps {
  entries: AgentLeaderboardEntry[];
}

/** Agent usage/success/retry table (HANDOFF.md §3). */
export function AgentLeaderboardTable({ entries }: AgentLeaderboardTableProps) {
  if (entries.length === 0) {
    return <div className={styles.empty}>No agent activity yet.</div>;
  }

  return (
    <table className={styles.table}>
      <thead>
        <tr>
          <th>Agent</th>
          <th>Usage</th>
          <th>Successes</th>
          <th>Failures</th>
          <th>Success rate</th>
          <th>Retries</th>
        </tr>
      </thead>
      <tbody>
        {entries.map((entry) => (
          <tr key={entry.agent}>
            <td>{entry.agent}</td>
            <td>{entry.usage_count}</td>
            <td>{entry.successes}</td>
            <td>{entry.failures}</td>
            <td>{Math.round(entry.success_rate * 100)}%</td>
            <td>{entry.retries}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
