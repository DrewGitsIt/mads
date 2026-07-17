import type { CSSProperties, ReactNode } from "react";
import styles from "./Card.module.css";

export interface CardProps {
  children: ReactNode;
  className?: string;
  hoverable?: boolean;
  selected?: boolean;
  /** Escape hatch for per-instance overrides (e.g. a status-toned border) that a className can't express. */
  style?: CSSProperties;
}

export function Card({ children, className, hoverable, selected, style }: CardProps) {
  const classes = [
    styles.card,
    hoverable ? styles.hoverable : "",
    selected ? styles.selected : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={classes} style={style} tabIndex={hoverable ? 0 : undefined}>
      {children}
    </div>
  );
}
