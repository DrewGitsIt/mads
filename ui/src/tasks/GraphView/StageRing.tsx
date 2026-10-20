import type { RunStatus } from "../../pipeline/plan/runStatus";
import type { StageCell } from "./runView";
import styles from "./StageRing.module.css";

export const RING_SIZE = 38;
const CENTER = RING_SIZE / 2;
const RADIUS = 15.5;
const GAP_DEG = 4;

function point(deg: number) {
  const rad = ((deg - 90) * Math.PI) / 180;
  return `${(CENTER + RADIUS * Math.cos(rad)).toFixed(2)} ${(CENTER + RADIUS * Math.sin(rad)).toFixed(2)}`;
}

function arcPath(index: number, count: number): string {
  if (count === 1) {
    // A lone stage is a full circle: two half arcs (one arc can't close on itself).
    return `M ${point(0)} A ${RADIUS} ${RADIUS} 0 1 1 ${point(180)} A ${RADIUS} ${RADIUS} 0 1 1 ${point(359.99)}`;
  }
  const span = 360 / count;
  const start = index * span + GAP_DEG / 2;
  const end = (index + 1) * span - GAP_DEG / 2;
  return `M ${point(start)} A ${RADIUS} ${RADIUS} 0 ${end - start > 180 ? 1 : 0} 1 ${point(end)}`;
}

export interface StageRingProps {
  stages: StageCell[];
  status: RunStatus;
  focused?: boolean;
}

/** Overview's node: one arc per stage, colored by that stage's state for this task. */
export function StageRing({ stages, status, focused }: StageRingProps) {
  return (
    <svg
      className={[styles.ring, focused && styles.focused].filter(Boolean).join(" ")}
      width={RING_SIZE}
      height={RING_SIZE}
      viewBox={`0 0 ${RING_SIZE} ${RING_SIZE}`}
      aria-hidden="true"
    >
      {stages.length === 0 && <circle className={styles.empty} cx={CENTER} cy={CENTER} r={RADIUS} />}
      {stages.map((s, i) => (
        <path key={s.name} d={arcPath(i, stages.length)} className={styles[`seg_${s.state}`]} data-state={s.state}>
          <title>{s.name}</title>
        </path>
      ))}
      <circle className={[styles.core, styles[`core_${status}`]].filter(Boolean).join(" ")} cx={CENTER} cy={CENTER} r={10} />
    </svg>
  );
}
