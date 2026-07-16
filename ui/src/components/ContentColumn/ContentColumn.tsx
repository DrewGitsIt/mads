import type { ReactNode } from "react";
import styles from "./ContentColumn.module.css";

export interface ContentColumnProps {
  children: ReactNode;
  /** wide: data-dense surfaces (tables/cards/boards). narrow: hero/prose/form moments. */
  variant?: "wide" | "narrow";
  className?: string;
}

export function ContentColumn({ children, variant = "wide", className }: ContentColumnProps) {
  return (
    <div className={[styles.column, styles[variant], className].filter(Boolean).join(" ")}>
      {children}
    </div>
  );
}
