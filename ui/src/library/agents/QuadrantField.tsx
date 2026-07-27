import { Card } from "../../components";
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
  return (
    <div className={styles.field}>
      <span className={styles.label} id="quadrant-label">
        Quadrant
      </span>
      <div className={styles.grid} role="group" aria-labelledby="quadrant-label">
        {QUADRANTS.map((q) => (
          <Card key={q.value} selected={value === q.value} className={styles.option}>
            <button
              type="button"
              className={styles.optionButton}
              aria-pressed={value === q.value}
              onClick={() => onChange(q.value)}
            >
              <span className={styles.optionLabel}>{q.label}</span>
              <span className={styles.optionDescription}>{q.description}</span>
            </button>
          </Card>
        ))}
      </div>
    </div>
  );
}
