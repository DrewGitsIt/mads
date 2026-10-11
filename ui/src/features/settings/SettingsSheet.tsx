import { useEffect, useRef, type ReactNode } from "react";
import { SideSheet, SideSheetBody, SideSheetFooter, SideSheetHeader } from "../../components/SideSheet/SideSheet";
import styles from "./SettingsSheet.module.css";

export interface SettingsSheetProps {
  title: string;
  onClose: () => void;
  width?: number;
  footer?: ReactNode;
  children: ReactNode;
}

/**
 * A side sheet for a Settings row's "Manage…"/"Import…": the shared SideSheet
 * with a titled header and a close button. Escape closes it unless a dialog
 * raised from inside is open (that dialog's own Escape answers first). Focus
 * moves to the sheet on open and back to whatever opened it on close.
 */
export function SettingsSheet({ title, onClose, width = 560, footer, children }: SettingsSheetProps) {
  const closeRef = useRef<HTMLButtonElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    closeRef.current?.focus();
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || document.querySelector('[role="alertdialog"]')) return;
      onCloseRef.current();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      if (opener?.isConnected) opener.focus();
    };
  }, []);

  return (
    <SideSheet width={width} aria-label={title}>
      <SideSheetHeader>
        <div className={styles.header}>
          <span className={styles.title}>{title}</span>
          <button ref={closeRef} type="button" className={styles.close} aria-label="Close" onClick={onClose}>
            ✕
          </button>
        </div>
      </SideSheetHeader>
      <SideSheetBody>{children}</SideSheetBody>
      {footer && <SideSheetFooter>{footer}</SideSheetFooter>}
    </SideSheet>
  );
}
