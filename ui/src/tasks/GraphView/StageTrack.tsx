import type { StageCell } from "./runView";
import styles from "./StageTrack.module.css";

/** Cards' compact stage track: one segment per stage, colored by that stage's state. */
export function StageTrack({ stages }: { stages: StageCell[] }) {
  if (stages.length === 0) return null;
  return (
    <div className={styles.track} aria-hidden="true">
      {stages.map((s) => (
        <span key={s.name} className={`${styles.seg} ${styles[`seg_${s.state}`]}`} data-state={s.state} title={s.name} />
      ))}
    </div>
  );
}
