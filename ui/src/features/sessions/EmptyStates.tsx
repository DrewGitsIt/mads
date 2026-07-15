import styles from "./EmptyStates.module.css";

/** Whole-rail teaching copy — shown only when there are no sessions at all (MADS-UX §2: empty states teach). */
export function NoSessionsEmptyState() {
  return (
    <div className={styles.empty}>
      <p className={styles.title}>No sessions yet</p>
      <p className={styles.body}>
        Describe what you want to work on in the composer below — a session is created
        automatically from your first message.
      </p>
    </div>
  );
}
