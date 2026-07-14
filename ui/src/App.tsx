import { useState } from "react";
import { IconRail, type IconRailItem } from "./components/IconRail/IconRail";
import styles from "./App.module.css";

const RAIL_ITEMS: IconRailItem[] = [
  { id: "sessions", label: "Sessions", icon: "S" },
  { id: "retro", label: "Retro", icon: "R" },
  { id: "library", label: "Library", icon: "L" },
];

export function App() {
  const [activeId, setActiveId] = useState("sessions");

  return (
    <div className={styles.shell}>
      <IconRail items={RAIL_ITEMS} activeId={activeId} onSelect={setActiveId} />
      <div className={styles.empty}>
        <div className={styles.emptyInner}>
          <div className={styles.emptyTitle}>No surfaces wired up yet</div>
          <div className={styles.emptyBody}>
            This is the app shell scaffold — sessions, retro, and library views
            land in later work items. Run <code>npm run gallery</code> to
            review the component kit this shell is built from.
          </div>
        </div>
      </div>
    </div>
  );
}
