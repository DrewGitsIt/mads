import { useMemo, useState } from "react";
import type { CostPoint } from "../../router/types";
import styles from "./CostByModelChart.module.css";

export interface CostByModelChartProps {
  byDay: CostPoint[];
}

const OTHER = "Other";
const CHART_COLORS = ["var(--chart-1)", "var(--chart-2)", "var(--chart-3)"];
const OTHER_COLOR = "var(--text3)";

interface DayColumn {
  day: string;
  values: { model: string; cost: number }[];
  total: number;
}

function firstSeenModels(byDay: CostPoint[]): string[] {
  const seen: string[] = [];
  for (const point of byDay) {
    if (!seen.includes(point.model)) seen.push(point.model);
  }
  return seen;
}

function buildColumns(byDay: CostPoint[], legendModels: string[]): DayColumn[] {
  const byDayMap = new Map<string, Map<string, number>>();
  for (const point of byDay) {
    const model = legendModels.includes(point.model) ? point.model : OTHER;
    const dayMap = byDayMap.get(point.day) ?? new Map<string, number>();
    dayMap.set(model, (dayMap.get(model) ?? 0) + point.cost_usd);
    byDayMap.set(point.day, dayMap);
  }
  const days = [...byDayMap.keys()].sort();
  const order = [...legendModels, OTHER];
  return days.map((day) => {
    const dayMap = byDayMap.get(day)!;
    const values = order.filter((m) => dayMap.has(m)).map((model) => ({ model, cost: dayMap.get(model)! }));
    const total = values.reduce((sum, v) => sum + v.cost, 0);
    return { day, values, total };
  });
}

function formatDay(day: string): string {
  return day.slice(0, 10);
}

/** Stacked cost-by-model bar chart (HANDOFF.md §6): fixed chart-token order, 2px segment gap, legend, per-column tooltip, table toggle. */
export function CostByModelChart({ byDay }: CostByModelChartProps) {
  const [showTable, setShowTable] = useState(false);
  const [hoveredDay, setHoveredDay] = useState<string | null>(null);

  const allModels = useMemo(() => firstSeenModels(byDay), [byDay]);
  const legendModels = allModels.slice(0, CHART_COLORS.length);
  const hasOther = allModels.length > CHART_COLORS.length;
  const columns = useMemo(() => buildColumns(byDay, legendModels), [byDay, legendModels]);
  const maxTotal = Math.max(1, ...columns.map((c) => c.total));

  const colorFor = (model: string) => {
    const idx = legendModels.indexOf(model);
    return idx === -1 ? OTHER_COLOR : CHART_COLORS[idx];
  };

  if (byDay.length === 0) {
    return <div className={styles.empty}>No cost data for this range.</div>;
  }

  return (
    <div className={styles.chart}>
      <div className={styles.header}>
        <div className={styles.legend}>
          {legendModels.map((model) => (
            <span key={model} className={styles.legendItem}>
              <span className={styles.swatch} style={{ background: colorFor(model) }} />
              {model}
            </span>
          ))}
          {hasOther && (
            <span className={styles.legendItem}>
              <span className={styles.swatch} style={{ background: OTHER_COLOR }} />
              {OTHER}
            </span>
          )}
        </div>
        <button
          type="button"
          className={styles.toggle}
          onClick={() => setShowTable((s) => !s)}
          aria-pressed={showTable}
        >
          {showTable ? "Show chart" : "Show table"}
        </button>
      </div>

      {showTable ? (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Day</th>
              {legendModels.map((m) => (
                <th key={m}>{m}</th>
              ))}
              {hasOther && <th>{OTHER}</th>}
              <th>Total</th>
            </tr>
          </thead>
          <tbody>
            {columns.map((col) => (
              <tr key={col.day}>
                <td>{formatDay(col.day)}</td>
                {legendModels.map((m) => (
                  <td key={m}>{(col.values.find((v) => v.model === m)?.cost ?? 0).toFixed(2)}</td>
                ))}
                {hasOther && <td>{(col.values.find((v) => v.model === OTHER)?.cost ?? 0).toFixed(2)}</td>}
                <td>{col.total.toFixed(2)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <div className={styles.plot}>
          {columns.map((col) => (
            <div
              key={col.day}
              className={styles.column}
              tabIndex={0}
              onMouseEnter={() => setHoveredDay(col.day)}
              onMouseLeave={() => setHoveredDay((d) => (d === col.day ? null : d))}
              onFocus={() => setHoveredDay(col.day)}
              onBlur={() => setHoveredDay((d) => (d === col.day ? null : d))}
            >
              <div className={styles.barWrap}>
                <div className={styles.bar} style={{ height: `${(col.total / maxTotal) * 100}%` }}>
                  {col.values.map((v) => (
                    <div
                      key={v.model}
                      className={styles.segment}
                      style={{ height: `${(v.cost / col.total) * 100}%`, background: colorFor(v.model) }}
                    />
                  ))}
                </div>
              </div>
              <span className={styles.dayLabel}>{formatDay(col.day).slice(5)}</span>
              {hoveredDay === col.day && (
                <div className={styles.tooltip} role="tooltip">
                  <div className={styles.tooltipDay}>{formatDay(col.day)}</div>
                  {col.values.map((v) => (
                    <div key={v.model} className={styles.tooltipRow}>
                      <span className={styles.swatch} style={{ background: colorFor(v.model) }} />
                      <span>{v.model}</span>
                      <span>${v.cost.toFixed(2)}</span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
