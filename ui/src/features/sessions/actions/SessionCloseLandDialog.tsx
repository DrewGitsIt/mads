import { useState } from "react";
import { ConfirmDialog } from "../../../components/ConfirmDialog/ConfirmDialog";
import type { RouterClient } from "../../../router/client";
import type { Session } from "../../../router/types";
import { autoResolvedCopy, nameFiles, plural, refusalCopy } from "../../../conversation/Worktree/worktreeCopy";
import styles from "./SessionDeleteConfirmDialog.module.css";

export interface SessionCloseLandDialogProps {
  /** An isolated session with commits its base doesn't have yet. */
  session: Session;
  routerClient: RouterClient;
  /** Close (pause) the session — "Keep for later", or after a successful Land. */
  onClose: () => void;
  /** Escape / backdrop: neither close nor land. */
  onDismiss: () => void;
}

/**
 * Closing a session with unlanded commits asks once (docs/SESSION-WORKTREES-
 * DESIGN.md D6, mockup §5): land them and close, or keep them for later.
 * Close never discards anything. If Land doesn't land, the session stays
 * open and the dialog says why — the same outcome is a card in its
 * conversation.
 */
export function SessionCloseLandDialog({ session, routerClient, onClose, onDismiss }: SessionCloseLandDialogProps) {
  const [landing, setLanding] = useState(false);
  const [outcome, setOutcome] = useState<string>();
  const base = session.worktree_base ?? "its base branch";
  const commits = session.unlanded_count ?? 0;

  async function landAndClose() {
    setLanding(true);
    setOutcome(undefined);
    try {
      const res = await routerClient.landSession(session.id);
      if (res.outcome === "landed") {
        onClose();
        return;
      }
      if (res.outcome === "conflicted") {
        const files = res.files ?? [];
        setOutcome(
          `${base} moved on and ${files.length > 0 ? nameFiles(files) : "some files"} conflict. Nothing was merged; ask the agent to resolve it from the conversation.`,
        );
      } else if (res.outcome === "auto_resolved") {
        const copy = autoResolvedCopy(res.base ?? session.worktree_base, res.files ?? []);
        setOutcome(`${copy.title}. ${copy.body}`);
      } else {
        const copy = refusalCopy(res.reason ?? "", res.base ?? session.worktree_base, res.files, res.outside);
        setOutcome(`${copy.title}. ${copy.body}`);
      }
    } catch (err) {
      setOutcome(err instanceof Error ? err.message : "Couldn't land.");
    } finally {
      setLanding(false);
    }
  }

  return (
    <ConfirmDialog
      open
      title={`Close “${session.name || "Untitled session"}”?`}
      confirmTone="primary"
      confirmLabel={landing ? "Landing…" : `Land into ${base} and close`}
      confirmDisabled={landing}
      cancelLabel="Keep for later"
      onConfirm={() => void landAndClose()}
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
