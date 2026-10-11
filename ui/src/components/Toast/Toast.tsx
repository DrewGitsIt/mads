import { Button } from "../Button/Button";
import styles from "./Toast.module.css";

export type ToastTone = "neutral" | "ok" | "warn" | "bad" | "info";

export interface ToastProps {
  tone?: ToastTone;
  title: string;
  description?: string;
  className?: string;
  onDismiss?: () => void;
  /** Undo-style affordance (§7): rendered as a small Button below the description. */
  action?: { label: string; onClick: () => void; disabled?: boolean };
}

/** Presentational only — no queue/provider yet (later wave). */
export function Toast({
  tone = "neutral",
  title,
  description,
  className,
  onDismiss,
  action,
}: ToastProps) {
  return (
    <div
      role="status"
      tabIndex={onDismiss ? 0 : undefined}
      className={[styles.toast, styles[tone], className]
        .filter(Boolean)
        .join(" ")}
    >
      <div className={styles.title}>{title}</div>
      {description && <div className={styles.description}>{description}</div>}
      {action && (
        <Button
          variant="ghost"
          size="sm"
          className={styles.action}
          onClick={action.onClick}
          disabled={action.disabled}
        >
          {action.label}
        </Button>
      )}
      {onDismiss && (
        <button
          type="button"
          className={styles.x}
          aria-label="Dismiss"
          onClick={onDismiss}
        >
          ✕
        </button>
      )}
    </div>
  );
}
