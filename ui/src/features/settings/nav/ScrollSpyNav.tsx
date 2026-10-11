import { Fragment } from "react";
import { SECTION_GROUPS, SECTION_LABELS, type SectionId } from "../sections";
import styles from "./ScrollSpyNav.module.css";

export interface ScrollSpyNavProps {
  activeId: SectionId;
  onNavigate: (id: SectionId) => void;
}

/** Left-nav for the one-page Settings surface: scroll-spy highlighted, click-to-scroll. */
export function ScrollSpyNav({ activeId, onNavigate }: ScrollSpyNavProps) {
  return (
    <nav className={styles.nav} aria-label="Settings sections">
      {SECTION_GROUPS.map((group, i) => (
        <Fragment key={group[0]}>
          {i > 0 && <div className={styles.separator} role="separator" />}
          {group.map((id) => (
            <button
              key={id}
              type="button"
              className={styles.item}
              aria-current={id === activeId ? "page" : undefined}
              onClick={() => onNavigate(id)}
            >
              {SECTION_LABELS[id]}
            </button>
          ))}
        </Fragment>
      ))}
    </nav>
  );
}
