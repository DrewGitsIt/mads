import { useEffect } from "react";
import { Button } from "../../components/Button/Button";
import { ConversationsLookedFor } from "../../features/settings/ConversationsLookedFor";
import { useConversationImport } from "../../features/settings/state/useConversationImport";
import styles from "./ConversationImportStep.module.css";

export interface ConversationImportStepProps {
  onNext: () => void;
  onBack: () => void;
}

const PROVIDER_LABELS: Record<string, string> = {
  "claude-code": "Claude Code",
};

function providerLabel(name: string): string {
  return PROVIDER_LABELS[name] ?? name;
}

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

function joinNames(names: string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/**
 * Step 3/6: reuses useConversationImport (the same hook a later Settings
 * entry point would use). The server only scans harnesses that are
 * installed, so "Claude Code not installed" arrives as total 0 and gets the
 * same honest empty state as "installed but nothing to import".
 *
 * Like PreflightStep, nothing here gates the user: a failed scan or a failed
 * import still leaves Continue / Skip for now available. Import is
 * all-or-nothing, so there is no per-conversation selection — only counts
 * and the folders they came from.
 */
export function ConversationImportStep({ onNext, onBack }: ConversationImportStepProps) {
  const imports = useConversationImport();
  const { scan, scanError, rescan } = imports;

  useEffect(() => {
    void rescan();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- scan once on mount, rescan is stable but the hook object is not
  }, []);

  const handleImport = async () => {
    const result = await imports.importAll();
    if (result) onNext();
  };

  const loading = scan === null && scanError === null;
  const found = scan !== null && scan.total > 0;

  return (
    <div className={styles.step}>
      <h2 className={styles.title}>Import conversations</h2>
      <p className={styles.body}>
        mads can bring in conversations you&apos;ve already had with your agent harnesses, so you can read them here.
        Scanning reads the conversation files on this machine; importing creates mads sessions that point at those
        files. Nothing is uploaded or copied anywhere.
      </p>
      {loading && <p className={styles.body}>Looking for conversations…</p>}
      {scanError !== null && (
        <p className={styles.error} role="alert">
          Couldn&apos;t look for existing conversations ({scanError}). You can continue without importing.
        </p>
      )}
      {scan !== null && found && (
        <>
          <p className={styles.body}>
            Found {plural(scan.total, "conversation", "conversations")} across{" "}
            {plural(scan.folders.length, "folder", "folders")} from{" "}
            {joinNames(scan.providers.map((p) => providerLabel(p.provider)))}.
          </p>
          {scan.folders.length > 0 && (
            <ul className={styles.list} aria-label="Folders">
              {scan.folders.map((f) => (
                <li key={f.dir} className={styles.folder}>
                  <code className={styles.dir}>{f.dir}</code>
                  <span className={styles.count}>{f.count}</span>
                  {!f.exists && <span className={styles.missing}>folder no longer exists</span>}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      {scan !== null && !found && (
        <p className={styles.empty}>
          Nothing to import — mads didn&apos;t find any existing conversations that it doesn&apos;t already have.
          That&apos;s normal if you haven&apos;t used an agent harness on this machine before.
        </p>
      )}
      {imports.importError !== null && (
        <p className={styles.error} role="alert">
          Couldn&apos;t import conversations ({imports.importError}). You can try again or skip for now.
        </p>
      )}
      {scan !== null && <ConversationsLookedFor places={scan.looked_for} />}
      <div className={styles.actions}>
        <Button variant="ghost" onClick={onBack}>
          Back
        </Button>
        {found ? (
          <>
            <Button variant="ghost" onClick={onNext} disabled={imports.importing}>
              Skip for now
            </Button>
            <Button variant="primary" onClick={() => void handleImport()} disabled={imports.importing}>
              {imports.importing ? "Importing…" : "Import conversations"}
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

