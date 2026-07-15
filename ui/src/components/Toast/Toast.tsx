import styles from "./Toast.module.css";

export type ToastTone = "neutral" | "ok" | "warn" | "bad" | "info";

export interface ToastProps {
  tone?: ToastTone;
  title: string;
  description?: string;
  className?: string;
  onDismiss?: () => void;
}

/** Presentational only — no queue/provider yet (later wave). */
export function Toast({
  tone = "neutral",
  title,
  description,
  className,
  onDismiss,
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
