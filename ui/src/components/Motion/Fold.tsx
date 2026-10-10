import { useEffect, useRef, useState, type ReactNode } from "react";
import styles from "./Fold.module.css";

/** --dur-slow: how long a fold moves. Children unmount this long after closing. */
export const FOLD_MS = 240;

export interface FoldProps {
  /** Shown. Closing folds shut, then unmounts the children. */
  open?: boolean;
  /** Mounted open: fold out from nothing instead of appearing at full height. Read once, at mount. */
  appear?: boolean;
  /** Fade with the fold — for things arriving, not for a disclosure the user toggled. */
  fade?: boolean;
  className?: string;
  children: ReactNode;
}

/**
 * Height fold for content that opens, closes or arrives in a flow — the
 * content below glides instead of jumping. Pure CSS (grid rows 0fr → 1fr);
 * the component only keeps closing content mounted until the fold finishes.
 */
export function Fold({ open = true, appear = false, fade = false, className, children }: FoldProps) {
  const [animateIn] = useState(appear);
  const [mounted, setMounted] = useState(open);
  // Settled: open and done moving, so the content may overflow the fold.
  const [settled, setSettled] = useState(open && !appear);

  if (open && !mounted) setMounted(true);
  // Closing content is on its way out: not focusable, not read.
  const inner = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (inner.current) inner.current.inert = !open;
  }, [open]);
  if (!open && settled) setSettled(false);

  useEffect(() => {
    if (open) {
      if (settled) return;
      const t = window.setTimeout(() => setSettled(true), FOLD_MS);
      return () => window.clearTimeout(t);
    }
    if (!mounted) return;
    const t = window.setTimeout(() => setMounted(false), FOLD_MS);
    return () => window.clearTimeout(t);
  }, [open, settled, mounted]);

  return (
    <div
      className={[styles.fold, className].filter(Boolean).join(" ")}
      data-open={open || undefined}
      data-appear={animateIn || undefined}
      data-fade={fade || undefined}
      data-settled={settled || undefined}
    >
      <div className={styles.inner} ref={inner} aria-hidden={!open || undefined}>
        {mounted && children}
      </div>
    </div>
  );
}
