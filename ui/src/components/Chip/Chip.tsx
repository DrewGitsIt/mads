import type { ReactNode } from "react";
import styles from "./Chip.module.css";

export type ChipTone = "neutral" | "ok" | "warn" | "bad" | "info" | "draft";

export interface ChipProps {
  tone?: ChipTone;
  children: ReactNode;
  className?: string;
}

export function Chip({ tone = "neutral", children, className }: ChipProps) {
  const classes = [styles.chip, styles[tone], className]
    .filter(Boolean)
    .join(" ");
  return <span className={classes}>{children}</span>;
}
