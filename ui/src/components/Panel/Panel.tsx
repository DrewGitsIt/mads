import type { ReactNode } from "react";
import styles from "./Panel.module.css";

export interface PanelProps {
  children: ReactNode;
  className?: string;
}

export function Panel({ children, className }: PanelProps) {
  return (
    <div className={[styles.panel, className].filter(Boolean).join(" ")}>
      {children}
    </div>
  );
}

export function PanelHeader({ children }: { children: ReactNode }) {
  return <div className={styles.header}>{children}</div>;
}

export function PanelBody({ children }: { children: ReactNode }) {
  return <div className={styles.body}>{children}</div>;
}

export function PanelFooter({ children }: { children: ReactNode }) {
  return <div className={styles.footer}>{children}</div>;
}
