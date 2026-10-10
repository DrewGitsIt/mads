import { useEffect, useMemo, useRef, useState } from "react";
import type { RouterClient } from "../../router/client";
import type { DelegateTranscript } from "../../router/types";

/**
 * How long to wait after the last sign of activity before re-reading a
 * delegate's transcript file. The harness writes the file as the delegate
 * runs, but nothing announces a write: a live event for the delegate (or a
 * change to its record) is the cue, and this batches a burst of them into one
 * read.
 */
export const DELEGATE_FILE_REFRESH_MS = 1000;

/**
 * A delegate's own transcript (GET .../delegates/{harnessId}/transcript),
 * read when `harnessId` is known and re-read once `activity` — any string
 * that changes as the delegate does — has settled. Undefined until the first
 * read lands; `available: false` when its harness keeps no file for it.
 * Shared by the full-pane delegate view and the conversation's subagent dock.
 */
export function useDelegateFile(
  client: Pick<RouterClient, "getDelegateTranscript">,
  sessionId: string,
  harnessId: string | undefined,
  activity: string,
): DelegateTranscript | undefined {
  const [file, setFile] = useState<DelegateTranscript>();
  // Only the newest read may land: a slow early read must not replace a
  // later, longer one, and a read for another delegate must not land at all.
  const reads = useRef(0);
  const read = useMemo(
    () => () => {
      if (!harnessId) return;
      const n = ++reads.current;
      client
        .getDelegateTranscript(sessionId, harnessId)
        .then((t) => {
          if (n === reads.current) setFile(t);
        })
        .catch(() => {});
    },
    [client, sessionId, harnessId],
  );

  useEffect(() => {
    setFile(undefined);
    read();
    const current = reads;
    return () => {
      current.current++; // an in-flight read for this delegate must not land
    };
  }, [read]);

  // Re-read once activity settles. The first run for each delegate only
  // arms: the read above already covers what's there now.
  const armed = useRef<typeof read>(undefined);
  useEffect(() => {
    if (armed.current !== read) {
      armed.current = read;
      return;
    }
    const t = setTimeout(read, DELEGATE_FILE_REFRESH_MS);
    return () => clearTimeout(t);
  }, [activity, read]);

  return file;
}
