import { useEffect, useState } from "react";
import type { CSSProperties } from "react";
import { Popover } from "../../../components/Popover/Popover";
import pillStyles from "../pills/pills.module.css";
import { flyoutPlacement } from "./LinkOptionsMenu";
import type { SubjectBadge } from "./railLinks";
import styles from "./SessionRail.module.css";

export interface LinkHolderSubject {
  name: string;
  badge: SubjectBadge;
  /** The link to delete — the same L1 route the nested row's own Unlink uses. */
  linkId: string;
}

export interface LinkHolderMenuProps {
  holderName: string;
  subjects: LinkHolderSubject[];
  onUnlinkRequest: (target: { linkId: string; name: string }) => void;
}

const ROLE_NOTE: Record<SubjectBadge, string> = {
  LINKED: "Leading this session",
  WATCHED: "Watching this session",
};

/**
 * Unlink, reachable from the holder's own badge.
 *
 * A holder can hold several subjects, so this names each one rather than
 * assuming which link "unlink" means. Cadence stays on the child row — that is
 * per-link detail, and this menu is the whole-relationship view.
 *
 * It takes no badge any more. The role used to be a pill on the holder's meta
 * row that doubled as this trigger; the connector states the relationship now,
 * the fan-out count sits on the trunk, and the trigger is a cluster button.
 */
export function LinkHolderMenu({
  holderName,
  subjects,
  onUnlinkRequest,
}: LinkHolderMenuProps) {
  const [placement, setPlacement] = useState<CSSProperties>();
  const open = placement !== undefined;
  const close = () => setPlacement(undefined);

  // Fixed coordinates go stale as soon as anything scrolls; close rather than
  // float detached from the row (same reason as LinkOptionsMenu's).
  useEffect(() => {
    if (!open) return;
    const dismiss = () => setPlacement(undefined);
    window.addEventListener("scroll", dismiss, true);
    window.addEventListener("resize", dismiss);
    return () => {
      window.removeEventListener("scroll", dismiss, true);
      window.removeEventListener("resize", dismiss);
    };
  }, [open]);

  return (
    <Popover
      open={open}
      onClose={close}
      className={styles.linkOptions}
      style={placement}
      anchor={
        <button
          type="button"
          className={[styles.actionButton, styles.linkOptionsTrigger].join(" ")}
          title="Linked sessions"
          aria-label={`Linked sessions held by ${holderName}`}
          aria-haspopup="menu"
          aria-expanded={open}
          onClick={(e) => {
            e.stopPropagation();
            if (open) close();
            else setPlacement(flyoutPlacement(e.currentTarget.getBoundingClientRect()));
          }}
        >
          {/* Points down the connector, at the subjects hanging off it. */}
          <span aria-hidden="true">⇲</span>
        </button>
      }
    >
      {/* Clicks inside the flyout must not bubble to the row and open it. */}
      <div className={pillStyles.menu} role="menu" onClick={(e) => e.stopPropagation()}>
        <div className={pillStyles.groupLabel}>Linked sessions</div>
        {subjects.map((sub) => (
          <button
            key={sub.linkId}
            type="button"
            role="menuitem"
            className={[pillStyles.option, styles.linkOptionsDanger].join(" ")}
            onClick={() => {
              // Close first — the confirm dialog also listens for Escape.
              close();
              onUnlinkRequest({ linkId: sub.linkId, name: sub.name });
            }}
          >
            <span className={pillStyles.optionLabel}>Unlink {sub.name}…</span>
            <span className={pillStyles.optionDescription}>{ROLE_NOTE[sub.badge]}</span>
          </button>
        ))}
      </div>
    </Popover>
  );
}
