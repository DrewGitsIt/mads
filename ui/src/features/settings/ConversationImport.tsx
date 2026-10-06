import { useState } from "react";
import { Button } from "../../components/Button/Button";
import type { ConversationImportResult } from "../../router/types";
import { ConversationsLookedFor } from "./ConversationsLookedFor";
import { summarizeConversationScan } from "./conversationImportSummary";
import { useConversationImport } from "./state/useConversationImport";
import styles from "./ConversationImport.module.css";

/**
 * Settings > General's way back into the conversation import, for anyone who
 * skipped the first-run wizard's step or has new conversations since. The
 * shorter cousin of ConversationImportStep, on the same hook: nothing is read
 * until the user asks (Scan), a scan is one sentence rather than a folder
 * list, and Import brings in everything found — all or nothing, as in the
 * wizard. Re-running is safe: the server skips what mads already has.
 */
export function ConversationImport() {
  const imports = useConversationImport();
  const { scan, scanning, scanError, importing, importError } = imports;
  const [result, setResult] = useState<ConversationImportResult | null>(null);

  const handleScan = () => {
    setResult(null);
    void imports.rescan();
  };

  const handleImport = async () => {
    const r = await imports.importAll();
    if (r) setResult(r);
  };

  const missing = scan?.folders.filter((f) => !f.exists).length ?? 0;

  return (
    <div className={styles.import}>
      <p className={styles.body}>
        Bring in conversations you&apos;ve had with your agent harnesses so you
        can pick them up where you left off. Scanning reads the conversation
        files on this machine; importing creates mads sessions that point at
        those files. Nothing is uploaded or copied anywhere.
      </p>
      {scanError !== null && (
        <p className={styles.error} role="alert">
          Couldn&apos;t look for existing conversations ({scanError}).
        </p>
      )}
      {importError !== null && (
        <p className={styles.error} role="alert">
          Couldn&apos;t import conversations ({importError}). You can try again.
        </p>
      )}
      {result !== null && (
        <p className={styles.body} role="status">
          Imported{" "}
          {result.imported === 1
            ? "1 conversation"
            : `${result.imported} conversations`}
          . They&apos;re in the session rail.
        </p>
      )}
      {result === null && scan !== null && scan.total > 0 && (
        <p className={styles.body}>
          Found {summarizeConversationScan(scan)}
          {missing > 0 &&
            ` (${missing === 1 ? "1 folder no longer exists" : `${missing} folders no longer exist`})`}
          .
        </p>
      )}
      {result === null && scan !== null && scan.total === 0 && (
        <p className={styles.empty}>
          Nothing new to import
          {scan.already_imported > 0 &&
            ` — mads already has ${scan.already_imported === 1 ? "the 1 conversation" : `all ${scan.already_imported} conversations`} it found`}
          .
        </p>
      )}
      {scan !== null && <ConversationsLookedFor places={scan.looked_for} />}
      <div className={styles.actions}>
        <Button
          variant={scan !== null && scan.total > 0 ? "ghost" : "secondary"}
          onClick={handleScan}
          disabled={scanning || importing}
        >
          {scanning
            ? "Scanning…"
            : scan === null
              ? "Scan for conversations"
              : "Scan again"}
        </Button>
        {result === null && scan !== null && scan.total > 0 && (
          <Button
            variant="primary"
            onClick={() => void handleImport()}
            disabled={importing}
          >
            {importing
              ? "Importing…"
              : `Import ${scan.total === 1 ? "1 conversation" : `${scan.total} conversations`}`}
          </Button>
        )}
      </div>
    </div>
  );
}
