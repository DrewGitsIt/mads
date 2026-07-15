import styles from "./TaskFiles.module.css";

export interface TaskFilesProps {
  touchedFiles: string[];
}

export function TaskFiles({ touchedFiles }: TaskFilesProps) {
  if (touchedFiles.length === 0) {
    return <p className={styles.empty}>No files touched yet.</p>;
  }

  return (
    <div className={styles.files}>
      <div className={styles.count}>{touchedFiles.length} files touched</div>
      <ul className={styles.list}>
        {touchedFiles.map((path) => (
          <li key={path} className={styles.path}>
            {path}
          </li>
        ))}
      </ul>
    </div>
  );
}
