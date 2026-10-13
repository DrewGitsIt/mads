import { createContext, useContext, useEffect, useRef } from "react";
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

/**
 * Which side of its anchor a Popover opens on. Default "below"; a surface
 * pinned to the bottom of the window (the composer's /new card) wraps its
 * controls in `above` so their menus open upward into the room there is.
 */
export const PopoverPlacementContext = createContext<"below" | "above">("below");

/**
 * Controlled popover: caller owns open state, this owns outside-click/Escape
 * dismissal. Escape is heard on the document, after React's handlers — an
 * enclosing surface that also closes on Escape (the composer's command card)
 * checks for an open `[data-popover-open]` inside itself and lets this one
 * have the key first.
 */
export function Popover({ open, onClose, anchor, children, className, style }: PopoverProps) {
  const rootRef = useRef<HTMLDivElement>(null);
  const placement = useContext(PopoverPlacementContext);

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
        <div
          className={[styles.popover, placement === "above" && styles.above, className]
            .filter(Boolean)
            .join(" ")}
          style={style}
          data-popover-open=""
        >
          {children}
        </div>
      )}
    </div>
  );
}
