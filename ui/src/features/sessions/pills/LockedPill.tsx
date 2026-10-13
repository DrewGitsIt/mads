import pillStyles from "./pills.module.css";

/**
 * Why a pill can't be changed right now — e.g. a linked session that must
 * join its counterpart's worktree, so Isolation is fixed. `label` replaces
 * the pill's usual value text when the locked value isn't one of its
 * options ("shares dns-tracing's worktree").
 */
export interface PillLock {
  reason: string;
  label?: string;
}

/**
 * The non-interactive form every pill renders when locked: dashed, muted,
 * no caret or menu, with the reason as its tooltip. Stays focusable
 * (aria-disabled, not disabled) so keyboard and screen-reader users can
 * still reach the reason.
 */
export function LockedPill({
  name,
  label,
  lock,
}: {
  /** The pill's key ("folder", "isolation", …). */
  name: string;
  /** The value text the pill would show unlocked; `lock.label` wins. */
  label: string;
  lock: PillLock;
}) {
  return (
    <button
      type="button"
      className={[pillStyles.trigger, pillStyles.locked].join(" ")}
      aria-disabled="true"
      title={lock.reason}
      data-locked=""
    >
      <span className={pillStyles.key}>{name}</span> {lock.label ?? label}
    </button>
  );
}
