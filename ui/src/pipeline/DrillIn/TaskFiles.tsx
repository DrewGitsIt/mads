import styles from "./TaskFiles.module.css";

export interface TaskFilesProps {
  touchedFiles: string[];
  /** Makes each row open that file (in the Changes tab). */
  onOpenFile?: (path: string) => void;
  /** The "N files touched" line; off when the host has its own heading. */
  showCount?: boolean;
}

export function TaskFiles({ touchedFiles, onOpenFile, showCount = true }: TaskFilesProps) {
  if (touchedFiles.length === 0) {
    return <p className={styles.empty}>No files touched yet.</p>;
  }

  return (
    <div className={styles.files}>
      {showCount && <div className={styles.count}>{touchedFiles.length} files touched</div>}
      <ul className={styles.list}>
        {touchedFiles.map((path) => (
          <li key={path} className={styles.path}>
            {onOpenFile ? (
              <button
                type="button"
                className={styles.open}
                title="Open this file's diff in Changes"
                onClick={() => onOpenFile(path)}
              >
                <span className={styles.openPath}>{path}</span>
                <span className={styles.openHint} aria-hidden="true">
                  View in Changes ↗
                </span>
              </button>
            ) : (
              path
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
