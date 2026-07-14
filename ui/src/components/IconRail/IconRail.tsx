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
  defaultCollapsed?: boolean;
  className?: string;
}

export function IconRail({
  items,
  activeId,
  onSelect,
  defaultCollapsed = true,
  className,
}: IconRailProps) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed);

  return (
    <nav
      data-collapsed={collapsed}
      className={[styles.rail, collapsed ? styles.collapsed : "", className]
        .filter(Boolean)
        .join(" ")}
    >
      {items.map((item) => (
        <button
          key={item.id}
          type="button"
          data-active={item.id === activeId}
          className={[styles.item, item.id === activeId ? styles.active : ""]
            .filter(Boolean)
            .join(" ")}
          aria-label={item.label}
          onClick={() => onSelect?.(item.id)}
        >
          <span className={styles.icon}>{item.icon}</span>
          <span className={styles.label}>{item.label}</span>
        </button>
      ))}
      <div className={styles.spacer} />
      <button
        type="button"
        className={styles.toggle}
        aria-label={collapsed ? "Expand rail" : "Collapse rail"}
        onClick={() => setCollapsed((c) => !c)}
      >
        {collapsed ? "»" : "«"}
      </button>
    </nav>
  );
}
