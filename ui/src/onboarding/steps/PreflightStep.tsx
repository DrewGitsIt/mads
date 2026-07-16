import { Button } from "../../components/Button/Button";
import { usePreflight } from "../../features/settings/state/usePreflight";
import styles from "./PreflightStep.module.css";

export interface PreflightStepProps {
  onNext: () => void;
  onBack: () => void;
}

/**
 * Step 2/5: shares usePreflight with Settings > General (HANDOFF §4/§6).
 * "Continue" is never gated on preflight results — this is an informational
 * health check with fix guidance (spec §14.8-14.9), not a hard gate; a user
 * with a real environment problem still needs to reach Settings to see the
 * guidance and fix it, not get stuck in the wizard.
 */
export function PreflightStep({ onNext, onBack }: PreflightStepProps) {
  const preflight = usePreflight();

  return (
    <div className={styles.step}>
      <h2 className={styles.title}>Provider preflight</h2>
      <ul className={styles.checks}>
        {preflight.checks.map((check) => (
          <li key={check.name} className={styles.check}>
            <span aria-hidden="true">{check.healthy ? "✓" : "✗"}</span>
            {check.name}
            {!check.healthy && check.fix_hint && <span className={styles.hint}>{check.fix_hint}</span>}
          </li>
        ))}
      </ul>
      <div className={styles.actions}>
        <Button variant="ghost" onClick={onBack}>
          Back
        </Button>
        <Button variant="primary" onClick={onNext}>
          Continue
        </Button>
      </div>
    </div>
  );
}
