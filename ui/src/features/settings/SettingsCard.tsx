import type { ReactNode } from "react";
import styles from "./SettingsCard.module.css";

export interface SettingsCardProps {
  /** Small uppercase heading over the card's rows, e.g. "Fast Model". */
  title?: string;
  children: ReactNode;
  className?: string;
}

/** A bordered group of SettingRows; rows divide themselves with hairlines. */
export function SettingsCard({ title, children, className }: SettingsCardProps) {
  return (
    <div className={[styles.card, className].filter(Boolean).join(" ")}>
      {title && <h3 className={styles.title}>{title}</h3>}
      {children}
    </div>
  );
}
