import { useState } from "react";
import { Popover } from "../../components/Popover/Popover";
import type { Session } from "../../router/types";
import { sessionLocation } from "../../router/sessionLocation";
import type { LandCardModel } from "../Worktree/worktreeCards";
import {
  autoResolvedCopy,
  checkResolutionsPrompt,
  nameFiles,
  outsideControlTitle,
  plural,
  refusalCopy,
  refusalDetailIsGit,
  resolveConflictPrompt,
  shortHash,
  tidyOutsidePrompt,
} from "../Worktree/worktreeCopy";
import { isIsolated, workState, type WorkState } from "../Worktree/workState";
import { landOutcomeKey, shortBranch } from "./dockLabels";
import { UPWARD_RIGHT, cx } from "./Pickers";
import type { DockWork } from "./SessionDock";
import line from "./StatusLine.module.css";
import styles from "./WorkArea.module.css";

export interface WorkAreaProps {
  session: Session;
  workspaceName?: string;
  checkoutBranch?: string;
  work?: DockWork;
  landOutcome?: LandCardModel;
  open: boolean;
  onOpen: () => void;
  onClose: () => void;
}

/** outsideControlTitle ends by telling the old header button's click; the dock's click opens the popover. */
function outsideWhy(files: number): string {
  return outsideControlTitle(files).replace(/\s*Click to ask the agent to tidy up\.$/, "");
}

/**
 * The status line's right end: the `⎇ {workspace} / {branch} → {base}`
 * segment, then the Commit/Land control, sharing one upward popover that
 * says where the session works and offers Commit… and Land. A Land click
 * opens the popover and its outcome (landed, conflicted, refused — with the
 * refusal's own fix, e.g. "Ask the agent" for changes outside the session)
 * renders there, at the button, as well as in the transcript.
 */
export function WorkArea({
  session,
  workspaceName,
  checkoutBranch,
  work,
  landOutcome,
  open,
  onOpen,
  onClose,
}: WorkAreaProps) {
  // Set by a Land click: the outcome key at that moment. Any other key that
  // shows up afterwards is this click's answer.
  const [awaiting, setAwaiting] = useState<{ before?: string } | null>(null);

  const where = sessionLocation(session, workspaceName);
  const isolated = isIsolated(session);
  const branch = isolated ? session.worktree_branch : session.worktree_branch || checkoutBranch;
  const base = isolated ? session.worktree_base : undefined;
  const state = workState(session);
  const landing = Boolean(work?.landing);

  // The old ConversationHeader subline's text, exactly: "mads / branch → main".
  const branchText = branch && (base ? `${branch} → ${base}` : branch);
  const locText = [where.name, branchText].filter(Boolean).join(" / ");
  const locTitle = [where.folder && `Folder: ${where.folder}`, where.worktree && `Worktree: ${where.worktree}`]
    .filter(Boolean)
    .join("\n");

  const showControl = Boolean(work) && (landing || state !== null);
  if (!locText && !showControl) return null;

  function close() {
    setAwaiting(null);
    onClose();
  }
  function toggleFresh() {
    if (open) {
      close();
    } else {
      setAwaiting(null);
      onOpen();
    }
  }
  function land() {
    if (!work || landing) return;
    setAwaiting({ before: landOutcomeKey(landOutcome) });
    onOpen();
    work.onLand();
  }
  function ask(text: string) {
    work?.onAskAgent?.(text);
    close();
  }

  const fresh = awaiting && landOutcomeKey(landOutcome) !== awaiting.before ? landOutcome : undefined;

  const anchor = (
    <>
      {locText && (
        <button
          type="button"
          className={cx(line.seg, styles.loc)}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-label={locText}
          title={locTitle || undefined}
          data-testid="dock-location"
          onClick={toggleFresh}
        >
          <span aria-hidden="true">⎇</span>
          {/* No trailing space after "/" or before "→": the segment's flex gap is the one space. */}
          {where.name && <span className={cx(line.text, line.vFull, styles.where)}>{where.name} /</span>}
          {branch && (
            <>
              <span className={cx(styles.branch, line.vFull)}>{branch}</span>
              <span className={cx(styles.branch, line.vMid)}>{shortBranch(branch)}</span>
            </>
          )}
          {base && <span className={styles.base}>→ {base}</span>}
        </button>
      )}
      {showControl && work && (
        <WorkButton
          state={state}
          landing={landing}
          committing={work.committing}
          onCommit={work.onCommit}
          onLand={land}
          onOutside={toggleFresh}
        />
      )}
    </>
  );

  const uncommitted = session.uncommitted_count ?? 0;
  const unlanded = isolated ? (session.unlanded_count ?? 0) : 0;
  const outside = isolated ? (session.outside_count ?? 0) : 0;
  const status = [
    unlanded > 0 && `${plural(unlanded, "commit")} to land`,
    uncommitted > 0 && `${plural(uncommitted, "file")} uncommitted`,
    outside > 0 && `${plural(outside, "file")} changed outside this session`,
  ].filter(Boolean);

  return (
    <div className={styles.area}>
      <Popover open={open} onClose={close} anchor={anchor} style={UPWARD_RIGHT}>
        <div className={styles.pop} role="dialog" aria-label="Where this session works" data-testid="work-popover">
          <div className={line.menuHead}>Where this session works</div>
          <dl className={styles.kv}>
            {where.folder && (
              <>
                <dt>Folder</dt>
                <dd title={where.folder}>{where.folder}</dd>
              </>
            )}
            {where.worktree && (
              <>
                <dt>Worktree</dt>
                <dd title={where.worktree}>{where.worktree}</dd>
              </>
            )}
            {branchText && (
              <>
                <dt>Branch</dt>
                <dd title={branchText}>{branchText}</dd>
              </>
            )}
            <dt>Status</dt>
            <dd className={status.length > 0 ? styles.warn : styles.plain} data-testid="work-status">
              {status.length > 0 ? status.join(" · ") : "Nothing to commit or land"}
            </dd>
          </dl>
          {work && (
            <div className={styles.acts}>
              <button
                type="button"
                className={line.btn}
                disabled={uncommitted === 0 || work.committing}
                onClick={() => {
                  close();
                  work.onCommit();
                }}
              >
                {work.committing ? "Committing…" : "Commit…"}
              </button>
              {isolated && (
                <button
                  type="button"
                  className={cx(line.btn, line.btnPrimary)}
                  disabled={landing || state?.kind !== "land"}
                  title={state?.kind === "land" ? undefined : landHeldTitle(state)}
                  onClick={land}
                >
                  {landing ? "Landing…" : `Land into ${base}`}
                </button>
              )}
            </div>
          )}
          {state?.kind === "outside" && !fresh && (
            <div className={cx(styles.outcome, styles.tone_warn)} data-testid="outside-explainer">
              <b>Changes outside this session are holding Land back.</b> {outsideWhy(state.files)}
              {work?.onAskAgent && (
                <div className={styles.outcomeActs}>
                  <button
                    type="button"
                    className={cx(line.btn, line.btnPrimary)}
                    onClick={() => ask(tidyOutsidePrompt())}
                  >
                    Ask the agent
                  </button>
                </div>
              )}
            </div>
          )}
          {awaiting && !fresh && landing && (
            <div className={cx(styles.outcome, styles.tone_quiet)} data-testid="land-outcome" data-kind="landing">
              Landing into {base}…
            </div>
          )}
          {fresh && (
            <LandOutcome
              card={fresh}
              landing={landing}
              onAskAgent={work?.onAskAgent ? ask : undefined}
              onLandAgain={work ? land : undefined}
            />
          )}
        </div>
      </Popover>
    </div>
  );
}

function landHeldTitle(state: WorkState): string {
  switch (state?.kind) {
    case "commit":
      return "Commit this session's changes first. Land only moves committed work.";
    case "outside":
      return "Files changed outside this session's change list hold Land back.";
    default:
      return "Nothing to land.";
  }
}

/**
 * The status-line Commit/Land control (docs/SESSION-WORKTREES-DESIGN.md D4),
 * driven by workState(): Commit… while files are uncommitted, Land once the
 * folder is clean with commits ahead, a warn-styled "Changes outside · N"
 * when files off the change list hold Land back, hidden otherwise. It never
 * collapses in a narrow dock; only its counts and long words drop.
 * `data-work-state` ("commit" | "land" | "landing" | "outside") is what the
 * e2e suite reads.
 */
function WorkButton({
  state,
  landing,
  committing,
  onCommit,
  onLand,
  onOutside,
}: {
  state: WorkState;
  landing: boolean;
  committing?: boolean;
  onCommit: () => void;
  onLand: () => void;
  onOutside: () => void;
}) {
  if (landing) {
    return (
      <button type="button" className={styles.work} disabled data-work-state="landing">
        Landing…
      </button>
    );
  }
  if (!state) return null;
  if (state.kind === "outside") {
    return (
      <button
        type="button"
        className={cx(styles.work, styles.outside)}
        onClick={onOutside}
        aria-label={`Changes outside this session · ${plural(state.files, "file")}`}
        title={`${outsideWhy(state.files)} Click for what to do.`}
        data-work-state="outside"
      >
        <span className={line.vFull}>Changes outside</span>
        <span className={cx(line.vMid, line.vMidKeep)}>Outside</span>
        <span className={styles.count}> · {state.files}</span>
      </button>
    );
  }
  if (state.kind === "land") {
    return (
      <button
        type="button"
        className={styles.work}
        onClick={onLand}
        title={`Land ${plural(state.commits, "commit")} into ${state.base}`}
        data-work-state="land"
      >
        Land<span className={line.vFull}> into {state.base}</span>
        <span className={cx(styles.count, line.lblFull)}> ↑{state.commits}</span>
      </button>
    );
  }
  return (
    <button
      type="button"
      className={styles.work}
      disabled={committing}
      onClick={onCommit}
      title={
        `Commit ${plural(state.files, "file")}` +
        (state.thenLand > 0 ? `, then land ${plural(state.thenLand, "commit")}` : "")
      }
      data-work-state="commit"
    >
      {committing ? "Committing…" : "Commit…"}
      <span className={cx(styles.count, line.lblFull)}> {state.files}</span>
    </button>
  );
}

/** One Land outcome, in worktreeCopy's words, with the fix it offers. */
function LandOutcome({
  card,
  landing,
  onAskAgent,
  onLandAgain,
}: {
  card: LandCardModel;
  landing: boolean;
  onAskAgent?: (text: string) => void;
  onLandAgain?: () => void;
}) {
  const again = onLandAgain && (
    <button type="button" className={line.btn} disabled={landing} onClick={onLandAgain}>
      {landing ? "Landing…" : "Land again"}
    </button>
  );
  switch (card.kind) {
    case "landed":
      return (
        <div className={cx(styles.outcome, styles.tone_ok)} data-testid="land-outcome" data-kind="landed">
          <b>Landed into {card.base ?? "its base"}.</b>
          {card.mergeCommit && (
            <>
              {" "}
              Merge commit <code>{shortHash(card.mergeCommit)}</code>.
            </>
          )}
          {card.fastForwarded && " This session keeps going on top of it."}
        </div>
      );
    case "conflicted": {
      const base = card.base ?? "The base branch";
      return (
        <div className={cx(styles.outcome, styles.tone_warn)} data-testid="land-outcome" data-kind="conflicted">
          <b>
            {base} moved on: {plural(card.files.length, "file")} conflict{card.files.length === 1 ? "s" : ""}.
          </b>{" "}
          {card.files.length > 0 && (
            <>
              <code>{nameFiles(card.files, 4)}</code> changed on {base} too.{" "}
            </>
          )}
          Nothing was merged, and your checkout is untouched.
          {onAskAgent && (
            <div className={styles.outcomeActs}>
              <button
                type="button"
                className={cx(line.btn, line.btnPrimary)}
                onClick={() => onAskAgent(resolveConflictPrompt(card.base, card.files))}
              >
                Ask the agent to resolve
              </button>
            </div>
          )}
        </div>
      );
    }
    case "autoResolved": {
      const copy = autoResolvedCopy(card.base, card.files);
      return (
        <div className={cx(styles.outcome, styles.tone_warn)} data-testid="land-outcome" data-kind="autoResolved">
          <b>{copy.title}.</b> {copy.body}
          <div className={styles.outcomeActs}>
            {onAskAgent && (
              <button
                type="button"
                className={cx(line.btn, line.btnPrimary)}
                onClick={() => onAskAgent(checkResolutionsPrompt(card.base, card.files))}
              >
                Ask the agent to check
              </button>
            )}
            {again}
          </div>
        </div>
      );
    }
    case "refused": {
      const copy = refusalCopy(card.reason, card.base, card.files, card.outside);
      const showDetail = Boolean(card.detail) && (refusalDetailIsGit(card.reason) || copy.title === "Land didn't run");
      const tidy = card.reason === "dirty" && card.outside?.length ? card.outside : undefined;
      return (
        <div
          className={cx(styles.outcome, styles[`tone_${copy.tone}`])}
          data-testid="land-outcome"
          data-kind="refused"
          data-reason={card.reason}
        >
          <b>{copy.title}.</b> {copy.body}
          {showDetail && <pre className={styles.detail}>{card.detail}</pre>}
          {((tidy && onAskAgent) || (copy.retry && again)) && (
            <div className={styles.outcomeActs}>
              {tidy && onAskAgent && (
                <button
                  type="button"
                  className={cx(line.btn, line.btnPrimary)}
                  onClick={() => onAskAgent(tidyOutsidePrompt(tidy))}
                >
                  Ask the agent
                </button>
              )}
              {copy.retry && again}
            </div>
          )}
        </div>
      );
    }
  }
}
