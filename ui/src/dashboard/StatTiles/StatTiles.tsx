import { Card } from "../../components/Card/Card";
import type { AgentLeaderboardEntry, CapabilityUsage, CostMetrics, PipelineRun, TaskOutcomes } from "../../router/types";
import styles from "./StatTiles.module.css";

export interface StatTilesProps {
  cost: CostMetrics;
  agents: AgentLeaderboardEntry[];
  tasks: TaskOutcomes;
  capabilities: CapabilityUsage[];
  runs: PipelineRun[];
}

function formatUsd(value: number): string {
  return `$${value.toFixed(2)}`;
}

/** Headline numbers derived from the 5 metrics responses (HANDOFF.md §3). */
export function StatTiles({ cost, agents, tasks, capabilities, runs }: StatTilesProps) {
  const totalCost = cost.by_session.reduce((sum, s) => sum + s.cost_usd, 0);
  const neverUsed = capabilities.filter((c) => c.invocations === 0).length;

  const tiles = [
    { label: "Total cost", value: formatUsd(totalCost) },
    { label: "Task completion rate", value: `${Math.round(tasks.completion_rate * 100)}%` },
    { label: "Active agents", value: String(agents.length) },
    { label: "Never-used capabilities", value: String(neverUsed) },
    { label: "Pipeline runs", value: String(runs.length) },
  ];

  return (
    <div className={styles.tiles}>
      {tiles.map((tile) => (
        <Card key={tile.label} className={styles.tile}>
          <div className={styles.value}>{tile.value}</div>
          <div className={styles.label}>{tile.label}</div>
        </Card>
      ))}
    </div>
  );
}
