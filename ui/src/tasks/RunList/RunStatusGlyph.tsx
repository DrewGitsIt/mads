import type { RunStatus } from "../../pipeline/plan/runStatus";
import styles from "./RunStatusGlyph.module.css";

/**
 * A run status as a shape: filled for done/blocked, a spinning arc for
 * running, dashed for queued, dotted for anything not in the run. Always
 * aria-hidden; the caller names the status in text.
 */
export function RunStatusGlyph({ status }: { status: RunStatus }) {
  return <span aria-hidden="true" data-status={status} className={[styles.glyph, styles[status]].join(" ")} />;
}
