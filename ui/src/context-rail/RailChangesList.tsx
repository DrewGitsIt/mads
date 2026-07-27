import type { FileChange } from "../router/types";
import styles from "./RailChangesList.module.css";

export interface RailChangesListProps {
  changes: FileChange[];
  /** Opens the file's diff (the Files tab's Monaco viewer). Rows are inert without it. */
  onOpenFile?: (path: string) => void;
}

const CHANGE_BADGE: Record<FileChange["change"], string> = {
  added: "A",
  modified: "M",
  deleted: "D",
};

function basename(path: string): string {
  const i = path.lastIndexOf("/");
  return i === -1 ? path : path.slice(i + 1);
}

function dirname(path: string): string {
  const i = path.lastIndexOf("/");
  return i === -1 ? "" : path.slice(0, i);
}

/**
 * Touched-file rows, filename-first: the basename is the primary line so the
 * narrow rail stays legible (an absolute path ellipsized from the right just
 * showed "/home/dre…" for every row). Directory and attribution drop to a
 * dimmed meta line. Clicking a row opens the file's diff via onOpenFile.
 * Presentational only — ContextRail owns the fetch so it can badge the tab
 * with the change count.
 */
export function RailChangesList({ changes, onOpenFile }: RailChangesListProps) {
  if (changes.length === 0) {
    return <div className={styles.empty}>No file changes yet.</div>;
  }

  return (
    <div className={styles.list}>
      {changes.map((change) => {
        const dir = dirname(change.path);
        const attribution = change.attributed ? (change.actor ?? "attributed") : "unattributed";
        return (
          <button
            key={change.id}
            type="button"
            className={styles.row}
            disabled={!onOpenFile}
            title={change.path}
            onClick={onOpenFile ? () => onOpenFile(change.path) : undefined}
          >
            <span className={styles.badge} data-change={change.change}>
              {CHANGE_BADGE[change.change]}
            </span>
            <span className={styles.file}>
              <span className={styles.top}>
                <span className={styles.name}>{basename(change.path)}</span>
                <span className={styles.attr} data-attributed={change.attributed}>
                  {attribution}
                </span>
              </span>
              {dir && <span className={styles.dir}>{dir}</span>}
            </span>
          </button>
        );
      })}
    </div>
  );
}
