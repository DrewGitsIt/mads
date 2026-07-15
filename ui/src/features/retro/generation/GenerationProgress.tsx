import { Button } from "../../../components/Button/Button";
import type { GenerationPhase } from "../state/useRetroGeneration";
import { GENERATION_STEPS, stepStatus } from "./stepOrder";
import styles from "./GenerationProgress.module.css";

export interface GenerationProgressProps {
  phase: GenerationPhase;
  onCancel: () => void;
}

/** 3-step (Starting/Gathering/Generating) progress indicator with a Cancel action. */
export function GenerationProgress({ phase, onCancel }: GenerationProgressProps) {
  return (
    <div className={styles.progress} role="status" aria-label="Retro generation progress">
      <ol className={styles.steps}>
        {GENERATION_STEPS.map((label, i) => (
          <li key={label} className={[styles.step, styles[stepStatus(i, phase)]].join(" ")}>
            {label}
          </li>
        ))}
      </ol>
      <Button variant="ghost" size="sm" onClick={onCancel}>
        Cancel
      </Button>
    </div>
  );
}
