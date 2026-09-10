import { useEffect, useState, type ReactNode } from "react";
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
  defaultCollapsed?: boolean;
  className?: string;
}

const RAIL_COLLAPSED_KEY = "mads.iconRail.collapsed";

function readStoredCollapsed(fallback: boolean): boolean {
  const stored = localStorage.getItem(RAIL_COLLAPSED_KEY);
  if (stored === "true") return true;
  if (stored === "false") return false;
  return fallback;
}

export function IconRail({
  items,
  activeId,
  onSelect,
  defaultCollapsed = true,
  className,
}: IconRailProps) {
  const [collapsed, setCollapsed] = useState(() =>
    readStoredCollapsed(defaultCollapsed),
  );
  const [peekSuppressed, setPeekSuppressed] = useState(false);

  useEffect(() => {
    localStorage.setItem(RAIL_COLLAPSED_KEY, String(collapsed));
  }, [collapsed]);

  function handleItemClick(id: string) {
    return (e: React.MouseEvent) => {
      e.stopPropagation();
      // Suppress the peek the same way handleToggle does. The clicked
      // button keeps focus after the click, so :focus-within would hold
      // the collapsed rail open at 176px — and since it is absolute and
      // z-index 20, it then covers the left edge of the very panel this
      // click just navigated to. Content under that strip cannot be
      // clicked free, because the click is intercepted by the rail rather
      // than landing and moving focus away. handleNavBlur clears this once
      // focus genuinely leaves the nav.
      setPeekSuppressed(true);
      onSelect?.(id);
    };
  }

  function handleToggle(e: React.MouseEvent) {
    e.stopPropagation();
    setCollapsed((prev) => {
      const next = !prev;
      if (next) setPeekSuppressed(true);
      return next;
    });
  }

  function handleBackgroundClick() {
    setCollapsed(false);
  }

  function handleNavBlur(e: React.FocusEvent<HTMLElement>) {
    if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
      setPeekSuppressed(false);
    }
  }

  return (
    <div
      className={[styles.slot, collapsed ? "" : styles.expanded, className]
        .filter(Boolean)
        .join(" ")}
    >
      <nav
        data-collapsed={collapsed}
        data-peek-suppressed={peekSuppressed}
        onClick={handleBackgroundClick}
        onMouseLeave={() => setPeekSuppressed(false)}
        onBlur={handleNavBlur}
        className={[styles.rail, collapsed ? styles.collapsed : ""]
          .filter(Boolean)
          .join(" ")}
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
        <div className={styles.spacer} />
        <button
          type="button"
          className={styles.toggle}
          aria-label={collapsed ? "Pin rail open" : "Collapse rail"}
          onClick={handleToggle}
        >
          {collapsed ? "»" : "«"}
        </button>
      </nav>
    </div>
  );
}
