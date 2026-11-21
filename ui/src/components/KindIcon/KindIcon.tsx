import type { ReactElement } from "react";
import styles from "./KindIcon.module.css";

export type DeviceKind = "computer" | "phone" | "unknown";

const PATHS: Record<DeviceKind, ReactElement> = {
  computer: (
    <>
      <rect x="2.5" y="3" width="11" height="7.5" rx="1.2" />
      <path d="M1 13h14" />
    </>
  ),
  phone: (
    <>
      <rect x="4.5" y="1.5" width="7" height="13" rx="1.6" />
      <path d="M7 12.5h2" />
    </>
  ),
  unknown: (
    <>
      <rect x="1.5" y="2.5" width="13" height="8.5" rx="1" />
      <path d="M6 14h4M8 11v3" />
    </>
  ),
};

/** A device's kind as an icon in a small tile; `label` (its platform) is the tooltip. */
export function KindIcon({ kind, label }: { kind: DeviceKind; label: string }) {
  return (
    <span className={styles.kind} title={label} data-kind={kind}>
      <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" aria-hidden="true">
        {PATHS[kind]}
      </svg>
    </span>
  );
}
