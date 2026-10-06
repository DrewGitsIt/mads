import { useCallback, useEffect, useRef, useState } from "react";
import { router } from "../../../router/client";
import type { ConversationImportResult, ConversationImportScan } from "../../../router/types";

export interface UseConversationImport {
  /** What an import would bring in right now; null until a scan has succeeded. */
  scan: ConversationImportScan | null;
  scanning: boolean;
  /** Set when the last scan failed (the server was unreachable or errored), cleared by the next one. */
  scanError: string | null;
  importing: boolean;
  /** Set when the last import failed, cleared by the next attempt. */
  importError: string | null;
  /** Reads the conversation files on this machine and summarizes them; creates nothing. */
  rescan: () => Promise<void>;
  /** Imports everything the server currently finds: all or nothing, no per-conversation selection. Resolves null on failure (importError says why). */
  importAll: () => Promise<ConversationImportResult | null>;
}

function messageOf(err: unknown): string {
  return err instanceof Error && err.message ? err.message : "unknown error";
}

/**
 * Shared by the first-run wizard's ConversationImportStep and any later
 * Settings entry point, the way useCredentials is shared for env-var import.
 * Holds only the counts the scan returns, never the conversations, and
 * never throws: both calls turn a failure into state the caller can render.
 */
export function useConversationImport(): UseConversationImport {
  const [scan, setScan] = useState<ConversationImportScan | null>(null);
  const [scanning, setScanning] = useState(false);
  const [scanError, setScanError] = useState<string | null>(null);
  const [importing, setImporting] = useState(false);
  const [importError, setImportError] = useState<string | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const rescan = useCallback(async () => {
    setScanning(true);
    setScanError(null);
    try {
      const result = await router.scanConversationImports();
      if (mounted.current) setScan(result);
    } catch (err) {
      if (mounted.current) setScanError(messageOf(err));
    } finally {
      if (mounted.current) setScanning(false);
    }
  }, []);

  const importAll = useCallback(async () => {
    setImporting(true);
    setImportError(null);
    try {
      const result = await router.importConversations();
      // Whatever was found is in mads now; a stale "N to import" must not linger.
      if (mounted.current) setScan((prev) => (prev ? { ...prev, providers: [], folders: [], total: 0 } : prev));
      return result;
    } catch (err) {
      if (mounted.current) setImportError(messageOf(err));
      return null;
    } finally {
      if (mounted.current) setImporting(false);
    }
  }, []);

  return { scan, scanning, scanError, importing, importError, rescan, importAll };
}

