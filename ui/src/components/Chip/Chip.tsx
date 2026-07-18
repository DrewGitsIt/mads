import type { ReactNode } from "react";
import styles from "./Chip.module.css";

export type ChipTone = "neutral" | "ok" | "warn" | "bad" | "info" | "draft";

export interface ChipProps {
  tone?: ChipTone;
  children: ReactNode;
  className?: string;
  onClick?: () => void;
  pressed?: boolean;
  onMouseEnter?: () => void;
  onMouseLeave?: () => void;
}

export function Chip({
  tone = "neutral",
  children,
  className,
  onClick,
  pressed,
  onMouseEnter,
  onMouseLeave,
}: ChipProps) {
  const classes = [styles.chip, styles[tone], className]
    .filter(Boolean)
    .join(" ");
  if (onClick) {
    return (
      <button
        type="button"
        className={classes}
        aria-pressed={pressed}
        onClick={onClick}
        onMouseEnter={onMouseEnter}
        onMouseLeave={onMouseLeave}
      >
        {children}
      </button>
    );
  }
  return (
    <span className={classes} onMouseEnter={onMouseEnter} onMouseLeave={onMouseLeave}>
      {children}
    </span>
  );
}
