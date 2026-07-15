import { useEffect, useRef, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Chip, type ChipTone } from "../../components/Chip/Chip";
import type { RouterClient, SessionEventSource } from "../../router/client";
import type { ControlEvent } from "../../router/types";
import { ConflictResolution } from "./ConflictResolution";
import styles from "./MergeBackFlow.module.css";

export type MergeStatus = "idle" | "attempting" | "conflicted" | "completed" | "error";

interface ConflictInfo {
  branch?: string;
  files: string[];
}

interface MergePayload {
  branch?: string;
  target_branch?: string;
  files?: string[];
}

export interface MergeBackFlowProps {
  sessionId: string;
  routerClient: RouterClient;
  /** Session.worktree_dir — the external-editor handoff target on conflict (HANDOFF §5). */
  worktreeDir?: string;
  /**
   * Seed-only conflict-marker preview to exercise the embedded merge editor
   * stub (HANDOFF §5): no real backend source for this exists yet (the
   * merge aborts and discards conflict-marker content), so this is only
   * ever supplied by the demo mount / tests, never by a real RouterClient
   * caller.
   */
  conflictPreview?: { path: string; text: string };
}

const STATUS_TONE: Record<MergeStatus, ChipTone> = {
  idle: "neutral",
  attempting: "info",
  conflicted: "bad",
  completed: "ok",
  error: "bad",
};

const STATUS_LABEL: Record<MergeStatus, string> = {
  idle: "Not merged",
  attempting: "Merging…",
  conflicted: "Conflicted",
  completed: "Merged",
  error: "Merge failed",
};

const MERGE_EVENT_TYPES = ["merge.attempted", "merge.conflicted", "merge.completed"];

/**
 * Target-branch pick -> auto-attempt via mergeSession() -> status driven by
 * merge.* control events (HANDOFF §7), not by the mergeSession() promise
 * result directly — the promise kicks the attempt off, the event stream is
 * the source of truth for status, matching how the real Session Service
 * runner reports outcomes (services/session/internal/runner/runner.go
 * `Merge()`).
 */
export function MergeBackFlow({
  sessionId,
  routerClient,
  worktreeDir,
  conflictPreview,
}: MergeBackFlowProps) {
  const [targetBranch, setTargetBranch] = useState("");
  const [status, setStatus] = useState<MergeStatus>("idle");
  const [conflict, setConflict] = useState<ConflictInfo | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | undefined>();
  const sourceRef = useRef<SessionEventSource | null>(null);

  useEffect(() => {
    const source = routerClient.streamControlEvents({
      sessionId,
      types: MERGE_EVENT_TYPES,
    });
    sourceRef.current = source;

    function onEvent(ev: MessageEvent<string>) {
      let envelope: ControlEvent;
      try {
        envelope = JSON.parse(ev.data) as ControlEvent;
      } catch {
        return;
      }
      const payload = (envelope.payload ?? {}) as MergePayload;
      if (envelope.type === "merge.attempted") {
        setStatus("attempting");
      } else if (envelope.type === "merge.conflicted") {
        setConflict({ branch: payload.branch, files: payload.files ?? [] });
        setStatus("conflicted");
      } else if (envelope.type === "merge.completed") {
        setConflict(null);
        setStatus("completed");
      }
    }

    for (const type of MERGE_EVENT_TYPES) {
      source.addEventListener(type, onEvent);
    }
    return () => {
      for (const type of MERGE_EVENT_TYPES) {
        source.removeEventListener(type, onEvent);
      }
      source.close();
    };
  }, [routerClient, sessionId]);

  function attemptMerge() {
    setStatus("attempting");
    setErrorMessage(undefined);
    routerClient.mergeSession(sessionId, targetBranch.trim() || undefined).catch((err: unknown) => {
      setStatus("error");
      setErrorMessage(String(err));
    });
  }

  return (
    <div className={styles.flow}>
      <div className={styles.row}>
        <input
          className={styles.branchInput}
          value={targetBranch}
          onChange={(e) => setTargetBranch(e.target.value)}
          placeholder="main"
          aria-label="Target branch"
          disabled={status === "attempting"}
        />
        <span className={styles.hint}>placeholder only — no real default yet</span>
        <div className={styles.spacer} />
        <Chip tone={STATUS_TONE[status]}>{STATUS_LABEL[status]}</Chip>
        <Button
          variant="primary"
          size="sm"
          disabled={status === "attempting"}
          onClick={attemptMerge}
        >
          Merge back
        </Button>
      </div>

      {status === "error" && errorMessage && (
        <div className={styles.error}>{errorMessage}</div>
      )}

      {status === "conflicted" && conflict && (
        <ConflictResolution
          branch={conflict.branch}
          files={conflict.files}
          worktreeDir={worktreeDir}
          onOpenInEditor={(path) => void routerClient.openInEditor(path)}
          onRetry={attemptMerge}
          conflictPreview={conflictPreview}
        />
      )}
    </div>
  );
}
