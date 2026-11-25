import { useRef, useState } from "react";
import { ConfirmDialog } from "../../../components/ConfirmDialog/ConfirmDialog";
import type { RouterClient } from "../../../router/client";
import type { LandResult, Session } from "../../../router/types";
import { LandDialog } from "../../../conversation/Worktree/LandDialog";
import { useCommitSettings } from "../../../conversation/Worktree/useCommitSettings";
import { autoResolvedCopy, nameFiles, plural } from "../../../conversation/Worktree/worktreeCopy";
import styles from "./SessionDeleteConfirmDialog.module.css";

export interface SessionCloseLandDialogProps {
  /** An isolated session with commits its base doesn't have yet. */
  session: Session;
  routerClient: RouterClient;
  /** Put the session to sleep — "Keep for later", or after a successful Land. */
  onClose: () => void;
  /** Escape / backdrop: neither close nor land. */
  onDismiss: () => void;
}

/**
 * Closing a session with unlanded commits asks once (docs/SESSION-WORKTREES-
 * DESIGN.md D6, mockup §5): land them and close, or keep them for later.
 * Close never discards anything. Land goes through its confirmation like
 * every Land (COMMIT-LAND-PUSH D12: the base, the commits, a message the
 * user writes); the session closes only once that lands. Cancelling the
 * Land cancels the close. A Land that conflicts or stops for a check comes
 * back here, the session still open, saying why — the same outcome is a
 * card in its conversation.
 */
export function SessionCloseLandDialog({ session, routerClient, onClose, onDismiss }: SessionCloseLandDialogProps) {
  const [confirming, setConfirming] = useState(false);
  const [outcome, setOutcome] = useState<string>();
  // How the Land confirmation ended, read as it closes.
  const ended = useRef<"landed" | "result" | null>(null);
  const base = session.worktree_base ?? "its base branch";
  const commits = session.unlanded_count ?? 0;

  function describe(res: LandResult): string {
    if (res.outcome === "conflicted") {
      const files = res.files ?? [];
      return `${base} moved on and ${files.length > 0 ? nameFiles(files) : "some files"} conflict. Nothing was merged; ask the agent to resolve it from the conversation.`;
    }
    const copy = autoResolvedCopy(res.base ?? session.worktree_base, res.files ?? []);
    return `${copy.title}. ${copy.body}`;
  }

  if (confirming) {
    return (
      <CloseLand
        session={session}
        routerClient={routerClient}
        onLanded={() => {
          ended.current = "landed";
          onClose();
        }}
        onResult={(res) => {
          ended.current = "result";
          setOutcome(describe(res));
        }}
        onClose={() => {
          const how = ended.current;
          ended.current = null;
          setConfirming(false);
          // Cancelled: no Land, so no close either.
          if (how === null) onDismiss();
        }}
      />
    );
  }

  return (
    <ConfirmDialog
      open
      title={`Sleep “${session.name || "Untitled session"}”?`}
      confirmTone="primary"
      confirmLabel={`Land into ${base} and sleep…`}
      cancelLabel="Keep for later"
      onConfirm={() => {
        setOutcome(undefined);
        setConfirming(true);
      }}
      onCancel={onClose}
      onDismiss={onDismiss}
      description={
        <div className={styles.body}>
          <span>
            This session has <b>{plural(commits, "commit")}</b> that {commits === 1 ? "isn't" : "aren't"} in{" "}
            {base} yet. They're safe on the session's branch either way.
          </span>
          {outcome && (
            <div className={[styles.statePanel, styles.panelAttn].join(" ")} role="alert">
              <div className={styles.stateHead}>
                <span className={[styles.stateDot, styles.stateDotAttn].join(" ")} aria-hidden="true" />
                Didn't land — still open
              </div>
              <div className={styles.stateLine}>{outcome}</div>
            </div>
          )}
        </div>
      }
    />
  );
}

/**
 * The Land confirmation for a close. Its own component so the Commit
 * settings (and the event bus they listen on) are read only once the user
 * chose Land, not for every close prompt.
 */
function CloseLand({
  session,
  routerClient,
  onLanded,
  onResult,
  onClose,
}: {
  session: Session;
  routerClient: RouterClient;
  onLanded: () => void;
  onResult: (res: LandResult) => void;
  onClose: () => void;
}) {
  const { fastModel } = useCommitSettings(routerClient);
  return (
    <LandDialog
      open
      sessionId={session.id}
      routerClient={routerClient}
      session={session}
      fastModel={fastModel}
      initialRoot={session.worktree_dir}
      onLanded={onLanded}
      onResult={onResult}
      onClose={onClose}
    />
  );
}
