import { useState } from "react";
import { Button } from "../components/Button/Button";
import { useConfig } from "../features/settings/state/useConfig";
import { WelcomeStep } from "./steps/WelcomeStep";
import { PreflightStep } from "./steps/PreflightStep";
import { CredentialImportStep } from "./steps/CredentialImportStep";
import { WorkspaceStep } from "./steps/WorkspaceStep";
import styles from "./FirstRunWizard.module.css";

export interface FirstRunWizardProps {
  onComplete: () => void;
}

const STEP_COUNT = 5;

/**
 * welcome -> preflight -> credential import -> first workspace -> done
 * (HANDOFF §6). Owns its own useConfig instance — separate from App.tsx's
 * gating fetch — needed to PUT the onboarding_complete flag on the final
 * step.
 */
export function FirstRunWizard({ onComplete }: FirstRunWizardProps) {
  const [step, setStep] = useState(0);
  const config = useConfig();
  const [finishing, setFinishing] = useState(false);

  const next = () => setStep((s) => Math.min(s + 1, STEP_COUNT - 1));
  const back = () => setStep((s) => Math.max(s - 1, 0));

  const finish = async () => {
    if (!config.draft) return;
    setFinishing(true);
    const ok = await config.save({ ...config.draft, onboarding_complete: true });
    setFinishing(false);
    if (ok) onComplete();
  };

  return (
    <div className={styles.wizard}>
      <div className={styles.card}>
        {step === 0 && <WelcomeStep onNext={next} />}
        {step === 1 && <PreflightStep onNext={next} onBack={back} />}
        {step === 2 && <CredentialImportStep onNext={next} onBack={back} />}
        {step === 3 && <WorkspaceStep onNext={next} onBack={back} config={config} />}
        {step === 4 && (
          <div className={styles.step}>
            <h2 className={styles.title}>All set</h2>
            <p className={styles.body}>mads is ready to go.</p>
            <div className={styles.actions}>
              <Button variant="ghost" onClick={back}>
                Back
              </Button>
              <Button
                variant="primary"
                onClick={() => void finish()}
                disabled={finishing || config.loading || !config.draft}
              >
                Go to Launchpad
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
