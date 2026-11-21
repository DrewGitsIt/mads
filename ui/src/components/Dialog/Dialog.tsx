import { useEffect, useId, useRef, type ReactNode } from "react";
import styles from "./Dialog.module.css";

export interface DialogProps {
  title: ReactNode;
  children?: ReactNode;
  /** The buttons, right-aligned at the bottom. */
  actions: ReactNode;
  /** Escape and a backdrop click. */
  onDismiss: () => void;
  /** An alertdialog: a decision with consequences (remove, raise, approve). */
  alert?: boolean;
  /** The narrow width of a confirmation. */
  confirm?: boolean;
  className?: string;
}

/**
 * The modal shell ConfirmDialog draws, for dialogs with more inside: a
 * title, a body, actions. Focus moves into it on open (to the element
 * marked data-autofocus, else the first control), Tab stays inside, and it
 * goes back where it was on close.
 */
export function Dialog({ title, children, actions, onDismiss, alert, confirm, className }: DialogProps) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const dismiss = useRef(onDismiss);
  dismiss.current = onDismiss;

  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    const el = ref.current;
    const first = el?.querySelector<HTMLElement>("[data-autofocus]") ?? focusables(el)[0];
    first?.focus();
    return () => {
      if (before && document.contains(before)) before.focus();
    };
  }, []);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        // A menu or popover open inside has the key first.
        if (e.defaultPrevented || ref.current?.querySelector('[aria-expanded="true"]')) return;
        e.preventDefault();
        dismiss.current();
        return;
      }
      if (e.key !== "Tab") return;
      const f = focusables(ref.current);
      if (f.length === 0) return;
      const i = f.indexOf(document.activeElement as HTMLElement);
      if (e.shiftKey && i <= 0) {
        e.preventDefault();
        f[f.length - 1].focus();
      } else if (!e.shiftKey && i === f.length - 1) {
        e.preventDefault();
        f[0].focus();
      }
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div className={styles.backdrop} onMouseDown={(e) => e.target === e.currentTarget && dismiss.current()}>
      <div
        ref={ref}
        role={alert ? "alertdialog" : "dialog"}
        aria-modal="true"
        aria-labelledby={titleId}
        className={[styles.dialog, confirm && styles.confirm, className].filter(Boolean).join(" ")}
      >
        <h2 id={titleId} className={styles.title}>
          {title}
        </h2>
        {children}
        <div className={styles.actions}>{actions}</div>
      </div>
    </div>
  );
}

function focusables(root: HTMLElement | null): HTMLElement[] {
  if (!root) return [];
  return [
    ...root.querySelectorAll<HTMLElement>(
      'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    ),
  ].filter((el) => el.getAttribute("tabindex") !== "-1" && el.getAttribute("aria-disabled") !== "true");
}

/** The muted copy under a dialog's title. */
export function DialogText({ children }: { children: ReactNode }) {
  return <div className={styles.description}>{children}</div>;
}

/** A small uppercase heading over a group inside a dialog. */
export function DialogSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div>
      <div className={styles.sectionTitle}>{title}</div>
      {children}
    </div>
  );
}

export { styles as dialogStyles };
