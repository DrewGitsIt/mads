import { Chip } from "../../components/Chip/Chip";
import type { Commit } from "../../router/types";
import { KIND_LABEL, KIND_TONE } from "./ChangeList";
import { AttributionChip } from "./AttributionChip";
import { attributionForCommit } from "./attribution";
import styles from "./CommitList.module.css";

export interface CommitListProps {
  commits: Commit[];
  taskTitles: Map<string, string>;
}

/**
 * Commit[] cards: message first line, task title chip (when resolvable),
 * short hash, formatted time, AttributionChip, and (when the Router could
 * resolve them) the commit's file list — a separate list from ChangeList,
 * not a join (FileChange has no commit hash field, see HANDOFF §4).
 */
export function CommitList({ commits, taskTitles }: CommitListProps) {
  if (commits.length === 0) {
    return <div className={styles.empty}>No commits linked to this session yet.</div>;
  }

  return (
    <ul className={styles.list}>
      {commits.map((c) => (
        <li key={c.hash} className={styles.card}>
          <div className={styles.cardHeader}>
            <span className={styles.message}>{c.message.split("\n")[0]}</span>
            <AttributionChip kind={attributionForCommit(c)} />
          </div>
          <div className={styles.cardMeta}>
            <span className={styles.hash}>{c.hash.slice(0, 7)}</span>
            {c.task_id && taskTitles.get(c.task_id) && (
              <Chip tone="neutral">{taskTitles.get(c.task_id)}</Chip>
            )}
            <span className={styles.time}>{new Date(c.timestamp).toLocaleString()}</span>
          </div>
          {c.files && c.files.length > 0 && (
            <ul className={styles.files}>
              {c.files.map((f) => (
                <li key={f.path} className={styles.fileRow}>
                  <Chip tone={KIND_TONE[f.change]} className={styles.kindBadge}>
                    {KIND_LABEL[f.change]}
                  </Chip>
                  <span className={styles.filePath}>{f.path}</span>
                </li>
              ))}
            </ul>
          )}
        </li>
      ))}
    </ul>
  );
}
