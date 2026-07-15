import type { Similar } from "../../router/types";
import styles from "./SimilarAgentWarning.module.css";

export interface SimilarAgentWarningProps {
  similar: Similar;
  onViewExisting?: () => void;
}

/** Near-duplicate warning for a freshly generated agent draft (MADS-UX §3.5). */
export function SimilarAgentWarning({ similar, onViewExisting }: SimilarAgentWarningProps) {
  return (
    <div className={styles.warning} role="alert">
      <div className={styles.text}>
        This looks similar to <strong>{similar.agent}</strong> (
        {Math.round(similar.score * 100)}% match).
      </div>
      {onViewExisting && (
        <button type="button" className={styles.link} onClick={onViewExisting}>
          View existing agent
        </button>
      )}
    </div>
  );
}
