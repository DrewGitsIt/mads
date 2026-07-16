import { useMemo, useState } from "react";
import { ContentColumn } from "../components/ContentColumn/ContentColumn";
import { DatePresets, type DatePreset } from "./DatePresets/DatePresets";
import { StatTiles } from "./StatTiles/StatTiles";
import { CostByModelChart } from "./CostChart/CostByModelChart";
import { AgentLeaderboardTable } from "./AgentLeaderboard/AgentLeaderboardTable";
import { TaskOutcomesPanel } from "./TaskOutcomes/TaskOutcomesPanel";
import { CapabilityUsageTable } from "./CapabilityUsage/CapabilityUsageTable";
import { RunHistoryTable } from "./RunHistory/RunHistoryTable";
import { LiveWidget } from "./LiveWidget/LiveWidget";
import { useDashboardMetrics } from "./state/useDashboardMetrics";
import { FetchMetricsClient, type MetricsClient } from "./client/metricsClient";
import type { CostPoint, PipelineRun } from "../router/types";
import styles from "./DashboardTab.module.css";

const PRESET_DAYS: Record<Exclude<DatePreset, "all">, number> = {
  "7d": 7,
  "30d": 30,
  "90d": 90,
};

function withinPreset(iso: string, preset: DatePreset): boolean {
  if (preset === "all") return true;
  const cutoff = Date.now() - PRESET_DAYS[preset] * 24 * 60 * 60 * 1000;
  return new Date(iso).getTime() >= cutoff;
}

const defaultClient = new FetchMetricsClient();

export interface DashboardTabProps {
  client?: MetricsClient;
  onDrillBack?: (params: { sessionId: string; runSeq: number }) => void;
}

/**
 * Top-level Dashboard tab (HANDOFF.md §3): owns date-preset state and
 * composes all 8 leaf views. Per §2 only the cost chart and run history
 * re-slice on the preset — the other views have no time axis in their
 * response shape and are rendered from the raw fetched data.
 */
export function DashboardTab({ client = defaultClient, onDrillBack }: DashboardTabProps) {
  const [preset, setPreset] = useState<DatePreset>("30d");
  const { cost, agents, tasks, capabilities, runs } = useDashboardMetrics(client);

  const filteredByDay = useMemo<CostPoint[]>(
    () => cost.data.by_day.filter((p) => withinPreset(p.day, preset)),
    [cost.data.by_day, preset],
  );
  const filteredRuns = useMemo<PipelineRun[]>(
    () => runs.data.filter((r) => withinPreset(r.started_at, preset)),
    [runs.data, preset],
  );

  return (
    <div className={styles.tab}>
      <ContentColumn variant="wide" className={styles.body}>
        <div className={styles.header}>
          <DatePresets value={preset} onChange={setPreset} />
          <LiveWidget client={client} />
        </div>

        <StatTiles
          cost={cost.data}
          agents={agents.data}
          tasks={tasks.data}
          capabilities={capabilities.data}
          runs={runs.data}
        />

        <section className={styles.section}>
          <h2 className={styles.heading}>Cost by model</h2>
          <CostByModelChart byDay={filteredByDay} />
        </section>

        <section className={styles.section}>
          <h2 className={styles.heading}>Agent leaderboard</h2>
          <AgentLeaderboardTable entries={agents.data} />
        </section>

        <section className={styles.section}>
          <h2 className={styles.heading}>Task outcomes</h2>
          <TaskOutcomesPanel outcomes={tasks.data} />
        </section>

        <section className={styles.section}>
          <h2 className={styles.heading}>Capability usage</h2>
          <CapabilityUsageTable usage={capabilities.data} />
        </section>

        <section className={styles.section}>
          <h2 className={styles.heading}>Run history</h2>
          <RunHistoryTable runs={filteredRuns} onDrillBack={onDrillBack} />
        </section>
      </ContentColumn>
    </div>
  );
}
