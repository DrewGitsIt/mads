import type { ReactNode } from "react";
import styles from "./Card.module.css";

export interface CardProps {
  children: ReactNode;
  className?: string;
  hoverable?: boolean;
  selected?: boolean;
}

export function Card({ children, className, hoverable, selected }: CardProps) {
  const classes = [
    styles.card,
    hoverable ? styles.hoverable : "",
    selected ? styles.selected : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={classes} tabIndex={hoverable ? 0 : undefined}>
      {children}
    </div>
  );
}
