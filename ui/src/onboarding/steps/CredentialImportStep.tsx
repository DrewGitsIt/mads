import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { useCredentials } from "../../features/settings/state/useCredentials";
import styles from "./CredentialImportStep.module.css";

export interface CredentialImportStepProps {
  onNext: () => void;
  onBack: () => void;
}

/**
 * Step 3/5: reuses useCredentials().envCandidates (HANDOFF §4/§6). Every
 * env-var value here is read server-side inside the Router process — only
 * NAMES ever reach this component, for both the candidate list and the
 * import call.
 */
export function CredentialImportStep({ onNext, onBack }: CredentialImportStepProps) {
  const credentials = useCredentials();
  const [selected, setSelected] = useState<string[]>([]);

  useEffect(() => {
    void credentials.loadEnvCandidates();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- load once on mount, credentials identity changes every render
  }, []);

  const toggle = (name: string) => {
    setSelected((prev) => (prev.includes(name) ? prev.filter((n) => n !== name) : [...prev, name]));
  };

  const handleImport = async () => {
    await credentials.importFromEnv(selected);
    setSelected([]);
    onNext();
  };

  return (
    <div className={styles.step}>
      <h2 className={styles.title}>Import credentials</h2>
      <p className={styles.body}>
        We detected these environment variables. Select the ones mads should use — only
        the names are ever sent anywhere; values are read locally.
      </p>
      <ul className={styles.list}>
        {credentials.envCandidates.map((n) => (
          <li key={n}>
            <label>
              <input type="checkbox" checked={selected.includes(n)} onChange={() => toggle(n)} />
              {n}
            </label>
          </li>
        ))}
        {credentials.envCandidates.length === 0 && (
          <li className={styles.empty}>No unimported env vars detected.</li>
        )}
      </ul>
      <div className={styles.actions}>
        <Button variant="ghost" onClick={onBack}>
          Back
        </Button>
        <Button variant="ghost" onClick={onNext}>
          Skip for now
        </Button>
        <Button variant="primary" onClick={() => void handleImport()} disabled={selected.length === 0}>
          Import selected
        </Button>
      </div>
    </div>
  );
}
