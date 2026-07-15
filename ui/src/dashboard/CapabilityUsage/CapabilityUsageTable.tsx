import { Chip } from "../../components/Chip/Chip";
import type { CapabilityUsage } from "../../router/types";
import styles from "./CapabilityUsageTable.module.css";

export interface CapabilityUsageTableProps {
  usage: CapabilityUsage[];
}

/** Capability usage table with a "never used" flag when invocations === 0 (HANDOFF.md §3). */
export function CapabilityUsageTable({ usage }: CapabilityUsageTableProps) {
  if (usage.length === 0) {
    return <div className={styles.empty}>No capability usage recorded yet.</div>;
  }

  return (
    <table className={styles.table}>
      <thead>
        <tr>
          <th>Capability</th>
          <th>Type</th>
          <th>Invocations</th>
          <th />
        </tr>
      </thead>
      <tbody>
        {usage.map((entry) => (
          <tr key={entry.capability}>
            <td>{entry.capability}</td>
            <td>{entry.type}</td>
            <td>{entry.invocations}</td>
            <td>{entry.invocations === 0 && <Chip tone="warn">Never used</Chip>}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
