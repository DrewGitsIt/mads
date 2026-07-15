import { useState } from "react";
import { Popover } from "../../components/Popover/Popover";
import type { Quadrant } from "../../router/types";
import styles from "./QuadrantField.module.css";

// REBUILD-SPEC §9's clarity/agency table — hardcoded, no server-side
// description field exists for these.
const QUADRANTS: { value: Quadrant; label: string; description: string }[] = [
  {
    value: "autonomous_executor",
    label: "Autonomous executor",
    description: "High clarity, high agency — knows the problem, directs its own work.",
  },
  {
    value: "technical_partner",
    label: "Technical partner",
    description: "Low clarity, high agency — doesn't know the problem yet, but drives forward.",
  },
  {
    value: "investigator",
    label: "Investigator",
    description: "Low clarity, low agency — explores an unclear problem before acting.",
  },
  {
    value: "research_analyst",
    label: "Research analyst",
    description: "High clarity, low agency — knows the problem, explores rather than directs.",
  },
];

export interface QuadrantFieldProps {
  value?: Quadrant;
  onChange: (value: Quadrant) => void;
}

export function QuadrantField({ value, onChange }: QuadrantFieldProps) {
  const [open, setOpen] = useState(false);

  return (
    <div className={styles.field}>
      <label className={styles.label} htmlFor="quadrant-select">
        Quadrant
      </label>
      <div className={styles.row}>
        <select
          id="quadrant-select"
          value={value ?? ""}
          onChange={(e) => onChange(e.target.value as Quadrant)}
        >
          <option value="" disabled>
            Select a quadrant
          </option>
          {QUADRANTS.map((q) => (
            <option key={q.value} value={q.value}>
              {q.label}
            </option>
          ))}
        </select>
        <Popover
          open={open}
          onClose={() => setOpen(false)}
          anchor={
            <button
              type="button"
              className={styles.help}
              aria-label="What is a quadrant?"
              onClick={() => setOpen((o) => !o)}
            >
              ?
            </button>
          }
        >
          <ul className={styles.list}>
            {QUADRANTS.map((q) => (
              <li key={q.value}>
                <strong>{q.label}</strong>: {q.description}
              </li>
            ))}
          </ul>
        </Popover>
      </div>
    </div>
  );
}
