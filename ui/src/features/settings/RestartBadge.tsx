import { Chip } from "../../components/Chip/Chip";
import styles from "./RestartBadge.module.css";

export interface RestartBadgeProps {
  visible: boolean;
}

/** "Restart required" indicator — shown next to a panel that has an unsaved (or just-saved) edit to a restart-required field (HANDOFF §7). */
export function RestartBadge({ visible }: RestartBadgeProps) {
  if (!visible) return null;
  return (
    <Chip tone="warn" className={styles.badge}>
      Restart required
    </Chip>
  );
}
