import type { GraphStatus } from "./graphStatus";
import styles from "./StatusGlyph.module.css";

export interface StatusGlyphProps {
  status: GraphStatus;
  /** in_progress spin — pass false under prefers-reduced-motion. */
  animate?: boolean;
}

/**
 * Shape-only glyph — no text, color never the only channel. Always
 * aria-hidden; the caller owns the accessible label (card aria-label, tray
 * Chip text, filter strip visible count+label).
 */
export function StatusGlyph({ status, animate = false }: StatusGlyphProps) {
  const cls = [styles.glyph, styles[status], status === "in_progress" && animate && styles.spin]
    .filter(Boolean)
    .join(" ");
  return <span aria-hidden="true" className={cls} />;
}
