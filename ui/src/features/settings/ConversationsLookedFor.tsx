import styles from "./EnvLookedFor.module.css";

export interface ConversationsLookedForProps {
  /** The places the import scan read (GET /imports/conversations looked_for). */
  places: string[];
}

/**
 * Disclosure naming exactly where the conversation scan looked, so an empty
 * result is legible. Reuses EnvLookedFor's styling: same disclosure, different
 * list.
 */
export function ConversationsLookedFor({ places }: ConversationsLookedForProps) {
  const shown = places.filter((p) => p !== "");
  if (shown.length === 0) return null;
  return (
    <details className={styles.details}>
      <summary className={styles.summary}>What does mads look for?</summary>
      <p className={styles.body}>
        Conversation files that agent harnesses keep on this machine, in these places only:
      </p>
      <ul className={styles.names}>
        {shown.map((p) => (
          <li key={p}>
            <code>{p}</code>
          </li>
        ))}
      </ul>
    </details>
  );
}
