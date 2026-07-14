import type { ReactNode } from "react";
import styles from "./SideSheet.module.css";

export interface SideSheetProps {
  children: ReactNode;
  width?: number;
  className?: string;
}

export function SideSheet({ children, width = 420, className }: SideSheetProps) {
  return (
    <div
      role="complementary"
      className={[styles.sheet, className].filter(Boolean).join(" ")}
      style={{ width }}
    >
      {children}
    </div>
  );
}

export function SideSheetHeader({ children }: { children: ReactNode }) {
  return <div className={styles.header}>{children}</div>;
}

export function SideSheetBody({ children }: { children: ReactNode }) {
  return <div className={styles.body}>{children}</div>;
}

export function SideSheetFooter({ children }: { children: ReactNode }) {
  return <div className={styles.footer}>{children}</div>;
}
