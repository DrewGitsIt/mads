import { useMemo, useState } from "react";
import { Chip } from "../../../../components/Chip/Chip";
import { Select } from "../../../../components/Select/Select";
import { SettingsCard } from "../../SettingsCard";
import { useDeviceActivity, type DeviceRow } from "../../../../router/devices";
import { activityLine } from "./model";
import styles from "./PairedDevices.module.css";

/** The log's entries: one device's (compact, inside its row) or every device's. */
export function ActivityList({ device, rows, compact, limit }: { device?: string; rows: DeviceRow[]; compact?: boolean; limit?: number }) {
  const { value, loaded, error } = useDeviceActivity(device, limit);
  if (error && !value) return <p className={styles.activityEmpty}>Couldn't read the activity log: {error}</p>;
  if (!loaded) return <p className={styles.activityEmpty}>Loading…</p>;
  if (!value || value.length === 0) return <p className={styles.activityEmpty}>Nothing yet.</p>;
  const now = new Date();
  return (
    <ul className={[styles.activityList, compact && styles.compact].filter(Boolean).join(" ")} aria-label="Device activity">
      {value.map((env) => {
        const line = activityLine(env, rows, now);
        return (
          <li key={line.seq} className={styles.activityItem} data-activity-type={env.type}>
            <Chip tone={line.chip.tone}>{line.chip.label}</Chip>
            <span>
              {line.parts.map((p, i) => (typeof p === "string" ? <span key={i}>{p}</span> : <b key={i}>{p.bold}</b>))}
            </span>
            <time dateTime={line.at} title={new Date(line.at).toLocaleString()}>
              {line.time}
            </time>
          </li>
        );
      })}
    </ul>
  );
}

/** The section-wide Device activity log, filterable by device. */
export function DeviceActivity({ rows }: { rows: DeviceRow[] }) {
  const [filter, setFilter] = useState("all");
  const options = useMemo(
    () => [
      { value: "all", label: "All devices" },
      ...rows.filter((r) => r.fingerprint).map((r) => ({ value: r.fingerprint!, label: r.name })),
    ],
    [rows],
  );
  const device = filter === "all" || !options.some((o) => o.value === filter) ? undefined : filter;
  return (
    <SettingsCard>
      <div className={styles.activityHead}>
        <h3 className={styles.activityTitle} id="device-activity-title">
          Device activity
        </h3>
        <div className={styles.activityFilter} aria-label="Show activity from">
          <Select value={device ?? "all"} options={options} onChange={setFilter} id="device-activity-filter" />
        </div>
      </div>
      <ActivityList device={device} rows={rows} />
    </SettingsCard>
  );
}
