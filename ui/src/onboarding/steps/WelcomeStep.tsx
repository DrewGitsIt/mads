import { Button } from "../../components/Button/Button";
import styles from "./WelcomeStep.module.css";

export interface WelcomeStepProps {
  onNext: () => void;
}

/** Step 1/6: static copy, no backend (HANDOFF §6). */
export function WelcomeStep({ onNext }: WelcomeStepProps) {
  return (
    <div className={styles.step}>
      <h2 className={styles.title}>Welcome to mads</h2>
      <p className={styles.body}>
        Let&apos;s get your machine set up: a quick provider check, importing any
        credentials you already have, and creating your first workspace.
      </p>
      <Button variant="primary" onClick={onNext}>
        Get started
      </Button>
    </div>
  );
}
