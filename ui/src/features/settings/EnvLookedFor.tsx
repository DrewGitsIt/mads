import styles from "./EnvLookedFor.module.css";

export interface EnvLookedForProps {
  /** The env scan's allowlist, prefixes as "AWS_*" (GET /credentials/env-candidates). */
  names: string[];
}

/**
 * Disclosure naming exactly which env vars the import scan matches — shared
 * by the wizard's CredentialImportStep and Settings > Credentials, so an
 * empty scan result is legible rather than a bare "nothing found".
 */
export function EnvLookedFor({ names }: EnvLookedForProps) {
  if (names.length === 0) return null;
  return (
    <details className={styles.details}>
      <summary className={styles.summary}>What does mads look for?</summary>
      <p className={styles.body}>
        A fixed list of well-known API keys and tokens, so unrelated secrets in your environment are never offered:
      </p>
      <ul className={styles.names}>
        {names.map((n) => (
          <li key={n}>
            <code>{n}</code>
          </li>
        ))}
      </ul>
    </details>
  );
}
