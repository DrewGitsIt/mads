import type { Session } from "../../router/types";
import { plural } from "./worktreeCopy";
import { workState } from "./workState";
import styles from "./WorkControl.module.css";

export interface WorkControlProps {
  session: Session;
  onCommit: () => void;
  onLand: () => void;
  /** A Land is in flight. */
  landing?: boolean;
  /** A one-click Commit is in flight. */
  committing?: boolean;
}

/**
 * The header's one state-driven control (mockup §1): Commit… while the
 * session has uncommitted files, Land into <base> once it's clean with
 * commits ahead, hidden otherwise.
 */
export function WorkControl({ session, onCommit, onLand, landing, committing }: WorkControlProps) {
  const state = workState(session);
  if (landing) {
    return (
      <button type="button" className={[styles.work, styles.land].join(" ")} disabled data-work-state="landing">
        Landing…
      </button>
    );
  }
  if (!state) return null;
  if (state.kind === "land") {
    return (
      <button
        type="button"
        className={[styles.work, styles.land].join(" ")}
        onClick={onLand}
        data-work-state="land"
      >
        Land into {state.base}
        <span className={styles.count}>{plural(state.commits, "commit")}</span>
      </button>
    );
  }
  return (
    <button
      type="button"
      className={[styles.work, styles.commit].join(" ")}
      disabled={committing}
      onClick={onCommit}
      data-work-state="commit"
    >
      {committing ? "Committing…" : "Commit…"}
      <span className={styles.count}>{plural(state.files, "file")}</span>
      {state.thenLand > 0 && <span className={styles.then}>then land {state.thenLand}</span>}
    </button>
  );
}
