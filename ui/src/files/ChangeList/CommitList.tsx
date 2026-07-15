import type { Commit } from "../../router/types";
import { AttributionChip } from "./AttributionChip";
import { attributionForCommit } from "./attribution";
import styles from "./CommitList.module.css";

export interface CommitListProps {
  commits: Commit[];
}

/**
 * Commit[] rows: short hash, message, AttributionChip — a separate list
 * from ChangeList, not a join (FileChange has no commit hash field, see
 * HANDOFF §4).
 */
export function CommitList({ commits }: CommitListProps) {
  if (commits.length === 0) {
    return <div className={styles.empty}>No commits linked to this session yet.</div>;
  }

  return (
    <ul className={styles.list}>
      {commits.map((c) => (
        <li key={c.hash} className={styles.row}>
          <span className={styles.hash}>{c.hash.slice(0, 7)}</span>
          <span className={styles.message}>{c.message.split("\n")[0]}</span>
          <AttributionChip kind={attributionForCommit(c)} />
        </li>
      ))}
    </ul>
  );
}
