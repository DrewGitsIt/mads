import type { ReactNode } from "react";
import { Chip } from "../Chip/Chip";
import styles from "./Draft.module.css";

export interface DraftChipProps {
  className?: string;
}

/** Shared provenance treatment for draft/proposed content (MADS-UX §2 rule 8). */
export function DraftChip({ className }: DraftChipProps) {
  return (
    <Chip tone="draft" className={className}>
      Draft
    </Chip>
  );
}

export interface DraftProps {
  children: ReactNode;
  className?: string;
}

export function Draft({ children, className }: DraftProps) {
  return (
    <div className={[styles.draft, className].filter(Boolean).join(" ")}>
      {children}
    </div>
  );
}
