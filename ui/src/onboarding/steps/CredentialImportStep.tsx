import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { EnvLookedFor } from "../../features/settings/EnvLookedFor";
import { useCredentials } from "../../features/settings/state/useCredentials";
import styles from "./CredentialImportStep.module.css";

export interface CredentialImportStepProps {
  onNext: () => void;
  onBack: () => void;
}

/**
 * Step 4/6: reuses useCredentials().envCandidates (HANDOFF §4/§6). Every
 * env-var value here is read server-side inside the Router process — only
 * NAMES ever reach this component, for both the candidate list and the
 * import call.
 */
export function CredentialImportStep({ onNext, onBack }: CredentialImportStepProps) {
  const credentials = useCredentials();
  const [selected, setSelected] = useState<string[]>([]);
  const [scanned, setScanned] = useState(false);

  useEffect(() => {
    void credentials.loadEnvCandidates().then(() => setScanned(true));
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

  const found = credentials.envCandidates.length > 0;

  return (
    <div className={styles.step}>
      <h2 className={styles.title}>Import credentials</h2>
      <p className={styles.body}>
        mads can reuse API keys and tokens already set in the environment it was started from, so you don&apos;t
        have to paste them in. Values are read and stored on this machine; only the names appear here.
      </p>
      {found ? (
        <>
          <p className={styles.body}>Found these — select the ones mads should use:</p>
          <ul className={styles.list}>
            {credentials.envCandidates.map((n) => (
              <li key={n}>
                <label>
                  <input type="checkbox" checked={selected.includes(n)} onChange={() => toggle(n)} />
                  {n}
                </label>
              </li>
            ))}
          </ul>
        </>
      ) : (
        scanned && (
          <p className={styles.empty}>
            Nothing to import — none of the variables mads looks for are set. That&apos;s normal: agent harnesses
            sign in on their own, and you can add credentials any time in Settings → Credentials.
          </p>
        )
      )}
      <EnvLookedFor names={credentials.envLookedFor} />
      <div className={styles.actions}>
        <Button variant="ghost" onClick={onBack}>
          Back
        </Button>
        {found ? (
          <>
            <Button variant="ghost" onClick={onNext}>
              Skip for now
            </Button>
            <Button variant="primary" onClick={() => void handleImport()} disabled={selected.length === 0}>
              Import selected
            </Button>
          </>
        ) : (
          <Button variant="primary" onClick={onNext}>
            Continue
          </Button>
        )}
      </div>
    </div>
  );
}
