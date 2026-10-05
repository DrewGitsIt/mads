import type { ReactNode } from "react";
import { useEffect } from "react";
import { Button } from "../Button/Button";
import styles from "./ConfirmDialog.module.css";

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  description: ReactNode;
  confirmLabel: string;
  cancelLabel?: string;
  /** "danger" (the default) for destructive verbs; "primary" for the accent button a non-destructive action takes. */
  confirmTone?: "danger" | "primary";
  confirmDisabled?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  /**
   * Escape and a backdrop click. Defaults to onCancel; set it when the cancel
   * button is itself a choice ("Keep for later") that dismissing mustn't make.
   */
  onDismiss?: () => void;
}

export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel,
  cancelLabel = "Cancel",
  confirmTone = "danger",
  confirmDisabled,
  onConfirm,
  onCancel,
  onDismiss,
}: ConfirmDialogProps) {
  const dismiss = onDismiss ?? onCancel;
  useEffect(() => {
    if (!open) return;
    function handleKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") dismiss();
    }
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [open, dismiss]);

  if (!open) return null;

  return (
    <div className={styles.backdrop} onMouseDown={dismiss}>
      <div
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="confirm-dialog-title"
        className={styles.dialog}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <h2 id="confirm-dialog-title" className={styles.title}>
          {title}
        </h2>
        <div className={styles.description}>{description}</div>
        <div className={styles.actions}>
          <Button variant="ghost" onClick={onCancel}>
            {cancelLabel}
          </Button>
          <Button
            variant="primary"
            className={confirmTone === "danger" ? styles.confirmDanger : undefined}
            disabled={confirmDisabled}
            onClick={onConfirm}
          >
            {confirmLabel}
          </Button>
        </div>
      </div>
    </div>
  );
}
