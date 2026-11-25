import type { ReactNode } from "react";
import styles from "./CheckCode.module.css";

/** "482913" → "482 913". */
export function groupDigits(code: string): string {
  return /^\d{6}$/.test(code) ? `${code.slice(0, 3)} ${code.slice(3)}` : code;
}

/**
 * The check code both screens show while a found device pairs, with what
 * to do with it beside it.
 */
export function CheckCode({ code, children }: { code: string; children: ReactNode }) {
  return (
    <div className={styles.box} data-check-code={code}>
      <span className={styles.code} aria-label={`Check code ${code.split("").join(" ")}`}>
        {groupDigits(code)}
      </span>
      <span className={styles.text}>{children}</span>
    </div>
  );
}
