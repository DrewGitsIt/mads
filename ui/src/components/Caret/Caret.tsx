import styles from "./Caret.module.css";

/**
 * The downward chevron that marks a trigger as a menu the user can open —
 * the dock's persona/model/trust segments, the launchpad's pills, Select.
 * Drawn (not a glyph) so it stays crisp at small type; sized in em so it
 * scales with its trigger's text, and colored with it (currentColor), so it
 * brightens on hover along with the label.
 */
export function Caret({ className }: { className?: string }) {
  return <span className={[styles.caret, className].filter(Boolean).join(" ")} aria-hidden="true" />;
}
