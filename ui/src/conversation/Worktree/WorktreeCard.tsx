import { useState, type ReactNode } from "react";
import { Button } from "../../components/Button/Button";
import { trustLabel } from "../../features/sessions/launchpad/pills/trustLevels";
import type { UndoCommitResult } from "../../router/types";
import { formatMessageTime, parseMessageTime } from "../ChatRenderer/messageTime";
import type { WorktreeCardModel } from "./worktreeCards";
import {
  autoResolvedCopy,
  checkResolutionsPrompt,
  nameFiles,
  pipelineMergedCopy,
  pipelineSavedCopy,
  plural,
  refusalCopy,
  refusalDetailIsGit,
  resolveConflictPrompt,
  shortHash,
  tidyOutsidePrompt,
  undoRefusalCopy,
  type CardTone,
} from "./worktreeCopy";
import styles from "./WorktreeCard.module.css";

export interface WorktreeCardActions {
  /** Takes back a one-click commit (POST /commit/undo). */
  onUndo?: (hash: string) => Promise<UndoCommitResult>;
  /** Puts an editable message in the composer — never sends on its own. */
  onAskAgent?: (text: string) => void;
  /** Runs Land again from a refusal the user has since fixed. */
  onLandAgain?: () => void;
  /** A Land is in flight: "Land again" waits. */
  landing?: boolean;
  /** Shows one file's diff in a recorded commit — the Files tab's commit view. */
  onOpenCommitFile?: (hash: string, path: string) => void;
}

/**
 * One Commit/Land result in the transcript (docs/mockups/session-worktrees.html
 * §3–§4): a system row in the session's history, not a chip on a side panel.
 */
export function WorktreeCard({ card, actions = {} }: { card: WorktreeCardModel; actions?: WorktreeCardActions }) {
  const at = parseMessageTime(card.timestamp);
  const when = at && (
    <time className={styles.when} dateTime={at.toISOString()} title={at.toLocaleString()}>
      {formatMessageTime(at)}
    </time>
  );

  switch (card.kind) {
    case "committed":
      return <CommittedCard card={card} when={when} onUndo={actions.onUndo} />;
    case "landed":
      return (
        <Shell tone="ok" title={`Landed into ${card.base ?? "its base"}`} when={when} kind="landed">
          <div className={styles.body}>
            {card.mergeCommit ? (
              <>
                Merge commit <span className={styles.hash}>{shortHash(card.mergeCommit)}</span>.{" "}
              </>
            ) : null}
            {card.fastForwarded ? "This session keeps going on top of it." : null}
          </div>
        </Shell>
      );
    case "conflicted": {
      const base = card.base ?? "The base branch";
      return (
        <Shell
          tone="warn"
          title={`${base} moved on: ${plural(card.files.length, "file")} conflict${card.files.length === 1 ? "s" : ""}`}
          when={when}
          kind="conflicted"
        >
          <div className={styles.body}>
            {card.files.length > 0 && (
              <>
                <span className={styles.mono}>{nameFiles(card.files, 4)}</span> changed on {base} too.{" "}
              </>
            )}
            Nothing was merged, and your checkout is untouched.
          </div>
          {actions.onAskAgent && (
            <div className={styles.acts} data-find-skip>
              <Button
                variant="primary"
                size="sm"
                onClick={() => actions.onAskAgent!(resolveConflictPrompt(card.base, card.files))}
              >
                Ask the agent to resolve
              </Button>
            </div>
          )}
        </Shell>
      );
    }
    case "autoResolved": {
      const copy = autoResolvedCopy(card.base, card.files);
      const first = card.files[0];
      const canSee = Boolean(actions.onOpenCommitFile && card.mergeCommit && first);
      const canLand = card.latest && Boolean(actions.onLandAgain);
      return (
        <Shell tone="warn" title={copy.title} when={when} kind="auto-resolved">
          <div className={styles.body}>{copy.body}</div>
          {(actions.onAskAgent || canSee || canLand) && (
            <div className={styles.acts} data-find-skip>
              {actions.onAskAgent && (
                <Button
                  variant="primary"
                  size="sm"
                  onClick={() => actions.onAskAgent!(checkResolutionsPrompt(card.base, card.files))}
                >
                  Ask the agent to check
                </Button>
              )}
              {canSee && (
                <Button variant="ghost" size="sm" onClick={() => actions.onOpenCommitFile!(card.mergeCommit!, first)}>
                  See the changes
                </Button>
              )}
              {canLand && (
                <Button variant="ghost" size="sm" disabled={actions.landing} onClick={actions.onLandAgain}>
                  {actions.landing ? "Landing…" : "Land again"}
                </Button>
              )}
            </div>
          )}
        </Shell>
      );
    }
    case "refused": {
      const copy = refusalCopy(card.reason, card.base, card.files, card.outside);
      const showDetail = Boolean(card.detail) && (refusalDetailIsGit(card.reason) || copy.title === "Land didn't run");
      const canRetry = copy.retry && card.latest && Boolean(actions.onLandAgain);
      // Files Commit can't take: the agent can commit or revert them.
      const tidy =
        card.reason === "dirty" && card.latest && actions.onAskAgent && card.outside?.length
          ? card.outside
          : undefined;
      return (
        <Shell tone={copy.tone} title={copy.title} when={when} kind="refused" reason={card.reason} repeats={card.repeats}>
          <div className={styles.body}>{copy.body}</div>
          {showDetail && <pre className={styles.detail}>{card.detail}</pre>}
          {(canRetry || tidy) && (
            <div className={styles.acts} data-find-skip>
              {tidy && (
                <Button variant="primary" size="sm" onClick={() => actions.onAskAgent!(tidyOutsidePrompt(tidy))}>
                  Ask the agent to commit them
                </Button>
              )}
              {canRetry && (
                <Button variant="ghost" size="sm" disabled={actions.landing} onClick={actions.onLandAgain}>
                  {actions.landing ? "Landing…" : "Land again"}
                </Button>
              )}
            </div>
          )}
        </Shell>
      );
    }
    case "ensured":
      return (
        <Shell tone="quiet" title={card.title} when={when} kind="ensured">
          <div className={styles.body}>{card.body}</div>
        </Shell>
      );
    case "pipelineSaved":
      return (
        <Shell tone="quiet" title="Pipeline saved your edits" when={when} kind="pipeline-saved">
          <div className={styles.body}>{pipelineSavedCopy(card.files, card.task)}</div>
        </Shell>
      );
    case "pipelineMerged":
      return (
        <Shell tone="warn" title="A pipeline merged while the agent was working" when={when} kind="pipeline-merged">
          <div className={styles.body}>{pipelineMergedCopy(card.files, card.task)}</div>
        </Shell>
      );
    case "trustChanged":
      return <TrustNotice card={card} when={when} />;
  }
}

/** The trust notice's one line, e.g. "Trust changed: Limited → Full access · agent restarted". */
export function trustNoticeText(card: Extract<WorktreeCardModel, { kind: "trustChanged" }>): string {
  const outcome = card.restarted ? "agent restarted" : "applies when the agent next starts";
  return `Trust changed: ${trustLabel(card.from)} → ${trustLabel(card.to)} · ${outcome}`;
}

/**
 * A trust change as a divider across the history rather than a card: it
 * marks where the agent's permissions changed, and asks nothing of the user.
 * A failed restart keeps its reason on hover — the change still stands, and
 * the line already says when it takes effect.
 */
function TrustNotice({
  card,
  when,
}: {
  card: Extract<WorktreeCardModel, { kind: "trustChanged" }>;
  when: ReactNode;
}) {
  const text = trustNoticeText(card);
  return (
    <div className={styles.divider} role="note" aria-label={text} data-worktree-card="trust-changed">
      <span className={styles.rule} aria-hidden="true" />
      <span
        className={styles.dividerText}
        title={card.restartError ? `Restarting the agent failed: ${card.restartError}` : undefined}
      >
        {text}
      </span>
      {when}
      <span className={styles.rule} aria-hidden="true" />
    </div>
  );
}

function Shell({
  tone,
  title,
  when,
  kind,
  reason,
  repeats,
  children,
}: {
  tone: CardTone;
  title: string;
  when: ReactNode;
  kind: string;
  reason?: string;
  /** Identical outcomes this card stands for; a count shows past one. */
  repeats?: number;
  children: ReactNode;
}) {
  return (
    <div
      className={[styles.card, styles[tone]].join(" ")}
      role="note"
      aria-label={title}
      data-worktree-card={kind}
      data-reason={reason}
    >
      <div className={styles.head}>
        <span className={styles.stateDot} aria-hidden="true" />
        <span className={styles.title}>{title}</span>
        {repeats !== undefined && repeats > 1 && (
          <span className={styles.repeats} title={`The same outcome ${repeats} times in a row`}>
            ×{repeats}
          </span>
        )}
        {when}
      </div>
      {children}
    </div>
  );
}

function CommittedCard({
  card,
  when,
  onUndo,
}: {
  card: Extract<WorktreeCardModel, { kind: "committed" }>;
  when: ReactNode;
  onUndo?: (hash: string) => Promise<UndoCommitResult>;
}) {
  const [undoing, setUndoing] = useState(false);
  const [undoError, setUndoError] = useState<string>();
  const title = card.files ? `Committed ${plural(card.files, "file")}` : "Committed";
  // Undo is offered only on a commit the Fast Model wrote without the
  // dialog, and only while nothing has landed or been committed on top.
  const canUndo = card.drafted && card.tip && !card.undone && onUndo;

  async function undo() {
    setUndoing(true);
    setUndoError(undefined);
    try {
      const res = await onUndo!(card.hash);
      if (!res.undone) setUndoError(undoRefusalCopy(res.reason));
    } catch (err) {
      setUndoError(err instanceof Error ? err.message : "Couldn't undo that commit.");
    } finally {
      setUndoing(false);
    }
  }

  return (
    <Shell tone={card.undone ? "quiet" : "ok"} title={title} when={when} kind="committed">
      <div className={styles.body}>
        <span className={[styles.hash, card.undone ? styles.struck : ""].filter(Boolean).join(" ")}>
          {shortHash(card.hash)}
        </span>{" "}
        {card.subject}
      </div>
      {card.undone && <div className={styles.body}>Undone — its files are uncommitted again.</div>}
      {undoError && <div className={styles.error}>{undoError}</div>}
      {canUndo && (
        <div className={styles.acts} data-find-skip>
          <Button variant="ghost" size="sm" disabled={undoing} onClick={() => void undo()}>
            {undoing ? "Undoing…" : "Undo"}
          </Button>
        </div>
      )}
    </Shell>
  );
}
