import { useEffect, useRef, type ReactNode } from "react";
import styles from "./CommandCardHost.module.css";

export interface CommandCardHostProps {
  children: ReactNode;
  /** Esc inside the card, or a press anywhere outside it. The Composer returns focus to its input. */
  onClose: () => void;
}

/**
 * Where a "/" command's card renders: floating directly above the composer,
 * with no header or footer chrome of its own. Cards are UI only — nothing
 * here is ever written to the transcript. The host doesn't take focus (a
 * card that wants it autofocuses its own control); Esc while the composer's
 * input has focus is handled by the Composer.
 */
export function CommandCardHost({ children, onClose }: CommandCardHostProps) {
  const ref = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    function onPointerDown(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node))
        onCloseRef.current();
    }
    // Deferred a tick: a card opened from a menu-row mousedown would
    // otherwise see that same press reach the document and close at once.
    const handle = setTimeout(() =>
      document.addEventListener("mousedown", onPointerDown),
    );
    return () => {
      clearTimeout(handle);
      document.removeEventListener("mousedown", onPointerDown);
    };
  }, []);

  return (
    <div
      ref={ref}
      className={styles.host}
      data-testid="command-card"
      onKeyDown={(e) => {
        if (e.key !== "Escape") return;
        // An inner control had the key first: a Select/Combobox that closed
        // its list (preventDefault), or an open Popover menu (a pill's), which
        // hears Escape on the document — let it bubble there. The card closes
        // on the next Escape, once nothing inside it is open.
        if (e.defaultPrevented) return;
        if (ref.current?.querySelector("[data-popover-open]")) return;
        e.preventDefault();
        e.stopPropagation();
        onClose();
      }}
    >
      {children}
    </div>
  );
}

/** The inline message a command shows instead of running ("/task needs a title", an unavailable command's reason). */
export function CommandNotice({ message }: { message: string }) {
  return (
    <div className={styles.notice} role="alert">
      {message}
    </div>
  );
}
