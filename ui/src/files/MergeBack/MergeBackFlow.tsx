import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Chip, type ChipTone } from "../../components/Chip/Chip";
import type { RouterClient } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import { WORKSPACE_SESSION_EVENT_TYPES } from "../../router/sessionEventTypes";
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

  // Shares SessionWorkspace's session-scoped subscription (see
  // WORKSPACE_SESSION_EVENT_TYPES) rather than opening its own — an
  // unpooled EventSource here would push a Files-tab-open session over
  // Chromium's 6-per-origin socket cap and starve unrelated fetches.
  useEffect(() => {
    return subscribeToEvents({ sessionId, types: WORKSPACE_SESSION_EVENT_TYPES }, (envelope) => {
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
    });
  }, [sessionId]);

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
        {/* placeholder="main" is display-only — an empty input sends no target
            branch, and mergeSession() applies no real default yet. */}
        <input
          className={styles.branchInput}
          value={targetBranch}
          onChange={(e) => setTargetBranch(e.target.value)}
          placeholder="main"
          aria-label="Target branch"
          disabled={status === "attempting"}
        />
        <div className={styles.spacer} />
        <Chip tone={STATUS_TONE[status]} className={styles.statusChip}>
          {STATUS_LABEL[status]}
        </Chip>
        <Button
          variant="primary"
          size="sm"
          className={styles.mergeButton}
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
