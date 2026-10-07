import { useState, type ReactNode } from "react";
import styles from "./IconRail.module.css";

export interface IconRailItem {
  id: string;
  label: string;
  icon: ReactNode;
}

export interface IconRailProps {
  items: IconRailItem[];
  activeId?: string;
  onSelect?: (id: string) => void;
  className?: string;
}

export function IconRail({ items, activeId, onSelect, className }: IconRailProps) {
  const [peekSuppressed, setPeekSuppressed] = useState(false);

  function handleItemClick(id: string) {
    return () => {
      // The pointer is still on the rail after a click, so :hover would hold
      // the peek open at 176px — and since it is absolute and z-index 20, it
      // would cover the left edge of the very panel this click just
      // navigated to. The clicked button keeps focus too, which is why the
      // CSS peeks on :focus-visible rather than :focus-within.
      // mouseleave/handleNavBlur clear this again.
      setPeekSuppressed(true);
      onSelect?.(id);
    };
  }

  function handleNavBlur(e: React.FocusEvent<HTMLElement>) {
    if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
      setPeekSuppressed(false);
    }
  }

  return (
    <div className={[styles.slot, className].filter(Boolean).join(" ")}>
      <nav
        aria-label="Main navigation"
        data-peek-suppressed={peekSuppressed}
        onMouseLeave={() => setPeekSuppressed(false)}
        onBlur={handleNavBlur}
        className={styles.rail}
      >
        {items.map((item) => (
          <button
            key={item.id}
            type="button"
            aria-selected={item.id === activeId}
            className={styles.item}
            aria-label={item.label}
            onClick={handleItemClick(item.id)}
          >
            <span className={styles.icon}>{item.icon}</span>
            <span className={styles.label}>{item.label}</span>
          </button>
        ))}
      </nav>
    </div>
  );
}
