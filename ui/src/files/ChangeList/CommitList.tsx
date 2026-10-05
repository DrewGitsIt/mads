import { Chip } from "../../components/Chip/Chip";
import type { Commit } from "../../router/types";
import { KIND_LABEL, KIND_TONE } from "./ChangeList";
import { AttributionChip } from "./AttributionChip";
import { attributionForCommit } from "./attribution";
import styles from "./CommitList.module.css";

export interface CommitListProps {
  commits: Commit[];
  taskTitles: Map<string, string>;
  selectedFile?: { hash: string; path: string };
  onSelectFile: (hash: string, path: string) => void;
}

/**
 * Commit[] cards: message first line, task title chip (when resolvable),
 * short hash, formatted time, AttributionChip, and (when the Router could
 * resolve them) the commit's file list — a separate list from ChangeList,
 * not a join (FileChange has no commit hash field, see HANDOFF §4). File
 * rows are selectable, same button idiom as ChangeList's rows.
 *
 * "Orphan" means a commit isn't linked to any of the session's tasks, which
 * is only worth a warning when the session has tasks to link to. In a plain
 * conversation every commit is unlinked by construction, so the chip is
 * dropped there rather than flagging each one. taskTitles holds the
 * session's tasks, so its size is that signal.
 */
export function CommitList({ commits, taskTitles, selectedFile, onSelectFile }: CommitListProps) {
  if (commits.length === 0) {
    return <div className={styles.empty}>No commits linked to this session yet.</div>;
  }

  const showOrphan = taskTitles.size > 0;
  return (
    <ul className={styles.list}>
      {commits.map((c) => (
        <li key={c.hash} className={styles.card}>
          <div className={styles.cardHeader}>
            <span className={styles.message}>{c.message.split("\n")[0]}</span>
            {(showOrphan || !c.orphan) && <AttributionChip kind={attributionForCommit(c)} />}
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
                <li key={f.path}>
                  <button
                    type="button"
                    className={styles.fileRow}
                    aria-current={selectedFile?.hash === c.hash && selectedFile.path === f.path}
                    data-selected={selectedFile?.hash === c.hash && selectedFile.path === f.path}
                    onClick={() => onSelectFile(c.hash, f.path)}
                  >
                    <Chip tone={KIND_TONE[f.change]} className={styles.kindBadge}>
                      {KIND_LABEL[f.change]}
                    </Chip>
                    <span className={styles.filePath}>{f.path}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </li>
      ))}
    </ul>
  );
}
