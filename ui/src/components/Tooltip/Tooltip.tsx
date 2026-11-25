import { useId, type ReactNode } from "react";
import styles from "./Tooltip.module.css";

export interface TooltipProps {
  /** What the tooltip says. */
  tip: ReactNode;
  children: ReactNode;
  /** Align to the anchor's start or end edge instead of centring (near a card's edge). */
  align?: "start" | "end";
  /**
   * Make the anchor itself a tab stop, so keyboard users can read the tip.
   * Off when the anchor already holds a focusable control (a button): the tip
   * still shows on its focus (focus-within).
   */
  focusable?: boolean;
  className?: string;
}

/**
 * A tip on hover and on keyboard focus — the quiet explanation behind a
 * status, a disabled control or a short value. CSS only: no portal, no
 * timers; the tip is in the DOM (role="tooltip", aria-describedby) so it's
 * read out with its anchor.
 */
export function Tooltip({ tip, children, align, focusable = true, className }: TooltipProps) {
  const id = useId();
  return (
    <span
      className={[styles.anchor, align && styles[align], className].filter(Boolean).join(" ")}
      tabIndex={focusable ? 0 : undefined}
      aria-describedby={id}
    >
      {children}
      <span role="tooltip" id={id} className={styles.tip}>
        {tip}
      </span>
    </span>
  );
}

/** An (i) button that explains the setting beside it. */
export function InfoTip({ label, children }: { label: string; children: ReactNode }) {
  const id = useId();
  return (
    <span className={[styles.anchor, styles.start].join(" ")}>
      <button type="button" className={styles.info} aria-label={`About ${label}`} aria-describedby={id}>
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true">
          <circle cx="8" cy="8" r="6.3" />
          <path d="M8 7.2v4" strokeLinecap="round" />
          <circle cx="8" cy="4.9" r=".6" fill="currentColor" stroke="none" />
        </svg>
      </button>
      <span role="tooltip" id={id} className={styles.tip}>
        {children}
      </span>
    </span>
  );
}
