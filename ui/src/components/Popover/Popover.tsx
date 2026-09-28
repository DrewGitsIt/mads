import { useEffect, useRef } from "react";
import type { CSSProperties, ReactNode } from "react";
import styles from "./Popover.module.css";

export interface PopoverProps {
  open: boolean;
  onClose: () => void;
  anchor: ReactNode;
  children: ReactNode;
  className?: string;
  /** Inline placement override, e.g. fixed coordinates for a popover that must escape a scroll container. */
  style?: CSSProperties;
}

/** Controlled popover: caller owns open state, this owns outside-click/Escape dismissal. */
export function Popover({ open, onClose, anchor, children, className, style }: PopoverProps) {
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    function handlePointerDown(e: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        onClose();
      }
    }
    function handleKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    document.addEventListener("mousedown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("mousedown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [open, onClose]);

  return (
    <div ref={rootRef} className={styles.root}>
      {anchor}
      {open && (
        <div className={[styles.popover, className].filter(Boolean).join(" ")} style={style}>
          {children}
        </div>
      )}
    </div>
  );
}
