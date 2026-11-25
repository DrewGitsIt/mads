import type { ReactNode } from "react";
import styles from "./Disclosure.module.css";

export interface DisclosureProps {
  summary: ReactNode;
  children: ReactNode;
  /** The in-card variant: quieter, under a device's controls. */
  inline?: boolean;
  defaultOpen?: boolean;
  className?: string;
}

/** A <details> section that opens in place (ModelsSection's pattern). */
export function Disclosure({ summary, children, inline, defaultOpen, className }: DisclosureProps) {
  return (
    <details className={[styles.disclosure, inline && styles.inline, className].filter(Boolean).join(" ")} open={defaultOpen}>
      <summary className={styles.summary}>
        <span className={styles.summaryLabel}>{summary}</span>
      </summary>
      <div className={styles.body}>{children}</div>
    </details>
  );
}
