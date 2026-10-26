import { useEffect, useRef, useState } from "react";
import { Button } from "../Button/Button";
import styles from "./Banner.module.css";

export type BannerTone = "warn" | "bad" | "info";

export interface BannerProps {
  /** Whether the condition holds. The banner shows while it does, unless dismissed. */
  active: boolean;
  tone?: BannerTone;
  title: string;
  description?: string;
  action?: { label: string; onClick: () => void };
  /**
   * Shown for a moment, in the ok tone, when the condition clears by itself —
   * "Live updates resumed". Absent, the banner just goes.
   */
  resolvedTitle?: string;
  /**
   * "bar": full width, edge to edge, for a condition of the whole app.
   * "inline": a box in the content it describes (a shelf, a list).
   * "compact": inline, stacked for a narrow column (the sessions rail).
   */
  variant?: "bar" | "inline" | "compact";
  className?: string;
}

/** How long the "resolved" state shows before the banner goes. */
export const BANNER_RESOLVED_MS = 1600;

/**
 * A condition that holds until it's fixed — the event stream down, a list
 * that won't load — as opposed to a Toast, which reports something that just
 * happened. It sits with what it describes, stays until the condition clears
 * or the user dismisses it, and a dismissed banner comes back the next time
 * the condition does.
 */
export function Banner({
  active,
  tone = "warn",
  title,
  description,
  action,
  resolvedTitle,
  variant = "bar",
  className,
}: BannerProps) {
  const [dismissed, setDismissed] = useState(false);
  const [resolved, setResolved] = useState(false);
  const wasShowing = useRef(false);

  // Track the condition's edges: clearing resets a dismissal (so the next
  // occurrence shows) and, if the banner was up, flashes the resolved state.
  const showing = active && !dismissed;
  useEffect(() => {
    if (active) {
      setResolved(false);
    } else {
      setDismissed(false);
      if (wasShowing.current && resolvedTitle) {
        setResolved(true);
        const t = window.setTimeout(() => setResolved(false), BANNER_RESOLVED_MS);
        wasShowing.current = false;
        return () => window.clearTimeout(t);
      }
    }
  }, [active, resolvedTitle]);
  useEffect(() => {
    wasShowing.current = showing;
  }, [showing]);

  if (!showing && !resolved) return null;

  const cls = [styles.banner, styles[variant], styles[resolved ? "ok" : tone], className]
    .filter(Boolean)
    .join(" ");
  if (resolved) {
    return (
      <div role="status" className={cls}>
        <span className={styles.icon} aria-hidden="true">
          ✓
        </span>
        <div className={styles.message}>
          <span className={styles.title}>{resolvedTitle}</span>
        </div>
      </div>
    );
  }
  return (
    <div role={tone === "bad" ? "alert" : "status"} className={cls}>
      <span className={styles.icon} aria-hidden="true">
        {tone === "bad" ? "✕" : tone === "info" ? "i" : "!"}
      </span>
      <div className={styles.message}>
        <span className={styles.title}>{title}</span>
        {description && <span className={styles.description}>{description}</span>}
      </div>
      {action && (
        <Button variant="ghost" size="sm" className={styles.action} onClick={action.onClick}>
          {action.label}
        </Button>
      )}
      <button
        type="button"
        className={styles.x}
        aria-label="Dismiss"
        title="Hide until this happens again"
        onClick={() => setDismissed(true)}
      >
        ✕
      </button>
    </div>
  );
}
