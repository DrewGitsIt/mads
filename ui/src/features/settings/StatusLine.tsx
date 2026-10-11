import type { ReactNode } from "react";
import styles from "./StatusLine.module.css";

export type StatusTone = "ok" | "warn" | "bad" | "muted";

/** A card's closing status line: a coloured dot, then what's going on. */
export function StatusLine({ tone, children, testId }: { tone: StatusTone; children: ReactNode; testId?: string }) {
  return (
    <div className={styles.status} data-testid={testId} data-tone={tone}>
      <span className={[styles.dot, styles[tone]].join(" ")} aria-hidden="true" />
      <span>{children}</span>
    </div>
  );
}

/** An in-page link styled as text — scrolls or opens something, never leaves. */
export function InlineLink({ onClick, children }: { onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" className={styles.link} onClick={onClick}>
      {children}
    </button>
  );
}
