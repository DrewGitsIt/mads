import { useEffect, useState } from "react";
import type { RouterClient } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import { fastModelAvailable } from "../../router/fastModel";

export interface CommitSettings {
  /** Settings' "Write commit messages for me" (Config.auto_commit_messages). */
  autoCommitMessages: boolean;
  /** A Fast Model can draft right now — gates "Draft with Fast Model" and the one-click Commit. */
  fastModel: boolean;
}

const OFF: CommitSettings = { autoCommitMessages: false, fastModel: false };

/**
 * The two Config facts Commit's UI turns on, read once per mount and again
 * whenever Config changes (same shape as useStreamSpeed). Until both
 * answers are in, it reads as off: the dialog is the safe default.
 */
export function useCommitSettings(client: RouterClient): CommitSettings {
  const [settings, setSettings] = useState<CommitSettings>(OFF);
  useEffect(() => {
    let cancelled = false;
    const load = () =>
      Promise.all([client.getConfig(), client.getFastModelStatus().catch(() => null)])
        .then(([config, status]) => {
          if (cancelled) return;
          setSettings({
            autoCommitMessages: config.auto_commit_messages === true,
            fastModel: fastModelAvailable(config, status),
          });
        })
        .catch(() => {});
    void load();
    const unsubscribe = subscribeToEvents({ types: ["config.updated"] }, () => void load());
    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [client]);
  return settings;
}
