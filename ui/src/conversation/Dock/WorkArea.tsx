import { useEffect, useState } from "react";
import { requestOpenSession } from "../../appNavigation";
import { Popover } from "../../components/Popover/Popover";
import type { PushTarget, Session, WorktreeTenant } from "../../router/types";
import { sessionLocation } from "../../router/sessionLocation";
import { useElectronClipboardBridge } from "../TerminalRenderer/ptyBridge";
import { outsideControlTitle, plural, tidyOutsidePrompt } from "../Worktree/worktreeCopy";
import { WorkPath } from "../Worktree/WorkPath";
import { isIsolated, workState, type LandRepo, type PushNext, type WorkState } from "../Worktree/workState";
import { shortBranch } from "./dockLabels";
import { UPWARD_RIGHT, cx } from "./Pickers";
import type { DockWork } from "./SessionDock";
import line from "./StatusLine.module.css";
import styles from "./WorkArea.module.css";

export interface WorkAreaProps {
  session: Session;
  workspaceName?: string;
  checkoutBranch?: string;
  work?: DockWork;
  open: boolean;
  onOpen: () => void;
  onClose: () => void;
}

/** outsideControlTitle without its last sentence, the click: the popover's explainer says why, its buttons what to do. */
function outsideWhy(files: number): string {
  return outsideControlTitle(files).replace(/\s*Click to choose what to commit\.$/, "");
}

/** Why an in-place session has no Land, from its worktree status. */
interface NoLandNote {
  sessionId: string;
  detail?: string;
  owner?: WorktreeTenant;
}

/**
 * The status line's right end: the `⎇ {workspace} / {branch} → {base}`
 * segment, then the Commit/Land control, sharing one upward popover that
 * says where the session works and where its work goes (WorkPath: Commit,
 * then Land, then Push, each with its button). Land opens its confirmation
 * (COMMIT-LAND-PUSH D12, LandDialog), where its refusals show; what it
 * did is a card in the transcript.
 */
export function WorkArea({
  session,
  workspaceName,
  checkoutBranch,
  work,
  open,
  onOpen,
  onClose,
}: WorkAreaProps) {
  const where = sessionLocation(session, workspaceName);
  const isolated = isIsolated(session);
  const branch = isolated ? session.worktree_branch : session.worktree_branch || checkoutBranch;
  const base = isolated ? session.worktree_base : undefined;
  const state = workState(session, { push: work?.push, land: work?.land });
  const landing = Boolean(work?.landing);

  // An in-place session never offers Land; on opening, the popover asks the
  // Router why (land_blocked) and whose worktree this is, if another
  // session's (owner_session). Not before: the status runs git.
  const [noLand, setNoLand] = useState<NoLandNote>();
  const loadStatus = work?.loadStatus;
  useEffect(() => {
    if (!open || isolated || !loadStatus) return;
    let cancelled = false;
    loadStatus().then(
      (st) =>
        !cancelled &&
        setNoLand({
          sessionId: session.id,
          detail: st.land_blocked?.reason === "not_isolated" ? st.land_blocked.detail : undefined,
          owner: st.owner_session,
        }),
      () => {},
    );
    return () => {
      cancelled = true;
    };
  }, [open, isolated, loadStatus, session.id]);
  const note = !isolated && noLand?.sessionId === session.id ? noLand : undefined;
  const owner = note?.owner;
  const ownerName = owner && (owner.name || owner.id);

  // The old ConversationHeader subline's text, exactly: "mads / branch → main".
  const branchText = branch && (base ? `${branch} → ${base}` : branch);
  const locText = [where.name, branchText].filter(Boolean).join(" / ");
  const locTitle = [where.folder && `Folder: ${where.folder}`, where.worktree && `Worktree: ${where.worktree}`]
    .filter(Boolean)
    .join("\n");

  const showControl = Boolean(work) && (landing || state !== null);
  if (!locText && !showControl) return null;

  function close() {
    onClose();
  }
  function toggleFresh() {
    if (open) close();
    else onOpen();
  }
  // Land always asks (D12): its confirmation opens, on that branch or the first landable one.
  function land(repo?: LandRepo) {
    if (!work || landing) return;
    close();
    work.onLand(repo);
  }
  function ask(text: string) {
    work?.onAskAgent?.(text);
    close();
  }
  function commitDialog() {
    close();
    work?.onCommitDialog?.();
  }

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
          onLand={() => land()}
          onOutside={work.onCommitDialog ?? toggleFresh}
          outsideOpensCommit={Boolean(work.onCommitDialog)}
          onPush={work.onPush}
        />
      )}
    </>
  );

  // The path needs git: an isolated session, a branch, or counts the
  // record has for some repo (a session started in a non-repo folder).
  const inGit =
    isolated ||
    Boolean(branch) ||
    (session.uncommitted_count ?? 0) > 0 ||
    (session.repos?.length ?? 0) > 0 ||
    (work?.push?.repos.length ?? 0) > 0 ||
    (work?.land?.length ?? 0) > 0;

  // Where the agent actually runs: an isolated session's own worktree, else
  // the folder it works in place.
  const workDir = where.worktree ?? where.folder;

  return (
    <div className={styles.area}>
      <Popover open={open} onClose={close} anchor={anchor} style={UPWARD_RIGHT}>
        <div className={styles.pop} role="dialog" aria-label="Where this session works" data-testid="work-popover">
          <div className={line.menuHead}>Where this session works</div>
          <dl className={styles.kv}>
            {where.folder && (
              <>
                <dt>Folder</dt>
                <PathRow path={where.folder} what="folder" workDir={where.folder === workDir} />
              </>
            )}
            {where.worktree && (
              <>
                <dt>Worktree</dt>
                <PathRow path={where.worktree} what="worktree" workDir={where.worktree === workDir} />
              </>
            )}
            {branchText && (
              <>
                <dt>Branch</dt>
                <dd title={branchText}>{branchText}</dd>
              </>
            )}
          </dl>
          {inGit && (
            <WorkPath
              session={session}
              committing={work?.committing}
              landing={landing}
              onCommit={
                work &&
                (() => {
                  close();
                  work.onCommit();
                })
              }
              onLand={work && land}
              land={work?.land}
              onPush={
                work?.onPush &&
                ((target) => {
                  close();
                  work.onPush?.(target);
                })
              }
              pushed={work?.pushed}
              push={work?.push}
            />
          )}
          {note && (note.detail || owner) && (
            <div className={cx(styles.outcome, styles.tone_quiet)} data-testid="no-land-note">
              {owner && <b>Changes here land from {ownerName}.</b>} {note.detail}
              {owner && (
                <div className={styles.outcomeActs}>
                  <button
                    type="button"
                    className={line.btn}
                    onClick={() => {
                      close();
                      requestOpenSession(owner.id);
                    }}
                  >
                    Open {ownerName}
                  </button>
                </div>
              )}
            </div>
          )}
          {state?.kind === "outside" && (
            <div className={cx(styles.outcome, styles.tone_warn)} data-testid="outside-explainer">
              <b>Changes outside this session are holding Land back.</b> {outsideWhy(state.files)}
              {(work?.onCommitDialog || work?.onAskAgent) && (
                <div className={styles.outcomeActs}>
                  {work.onCommitDialog && (
                    <button type="button" className={cx(line.btn, line.btnPrimary)} onClick={commitDialog}>
                      Commit them…
                    </button>
                  )}
                  {work.onAskAgent && (
                    <button
                      type="button"
                      className={cx(line.btn, !work.onCommitDialog && line.btnPrimary)}
                      onClick={() => ask(tidyOutsidePrompt())}
                    >
                      Ask the agent
                    </button>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      </Popover>
    </div>
  );
}

/** How long "Copied" stays on a copy button — the same beat as Settings' Copy and the session-id copy. */
const COPIED_MS = 1500;

/**
 * One path in the ⎇ popover: the full absolute path (selectable, ellipsized
 * from the right, whole in the tooltip) and a Copy button beside it. The row
 * the agent runs in is marked `data-workdir`.
 */
function PathRow({ path, what, workDir }: { path: string; what: "folder" | "worktree"; workDir: boolean }) {
  const clipboard = useElectronClipboardBridge();
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), COPIED_MS);
    return () => clearTimeout(timer);
  }, [copied]);

  // TerminalView's split: Electron's clipboard bridge (navigator.clipboard
  // is unreliable in a BrowserWindow), else navigator.clipboard.
  function copy() {
    const write = clipboard
      ? clipboard.writeText(path)
      : navigator.clipboard?.writeText
        ? navigator.clipboard.writeText(path)
        : undefined;
    write?.then(
      () => setCopied(true),
      () => {},
    );
  }

  return (
    <dd className={styles.path} data-workdir={workDir || undefined} data-testid={`work-path-${what}`}>
      <span className={styles.pathText} title={workDir ? `${path}\nThe agent works here.` : path}>
        {path}
      </span>
      <button
        type="button"
        className={cx(styles.copy, copied && styles.copied)}
        aria-label={copied ? `Copied ${what} path` : `Copy ${what} path`}
        title={copied ? undefined : `Copy the full ${what} path`}
        onClick={copy}
      >
        {copied ? "Copied" : "Copy"}
      </button>
    </dd>
  );
}

/**
 * The status-line Commit/Land/Push control (docs/SESSION-WORKTREES-DESIGN.md
 * D4, COMMIT-LAND-PUSH D5), driven by workState(): Commit… while files are
 * uncommitted, Land once the folder is clean with commits ahead, a
 * warn-styled "Changes outside · N" when files off the change list hold Land
 * back, Push ↑N once everything is committed (and landed) with commits the
 * remote lacks, hidden otherwise. It never collapses in a narrow dock; only
 * its counts and long words drop. `data-work-state` ("commit" | "land" |
 * "landing" | "outside" | "push") is what the e2e suite reads.
 */
function WorkButton({
  state,
  landing,
  committing,
  onCommit,
  onLand,
  onOutside,
  outsideOpensCommit,
  onPush,
}: {
  state: WorkState;
  landing: boolean;
  committing?: boolean;
  onCommit: () => void;
  onLand: () => void;
  /** Opens the Commit dialog when the caller can, else the popover. */
  onOutside: () => void;
  /** onOutside opens the Commit dialog: the title says so. */
  outsideOpensCommit?: boolean;
  onPush?: (target: PushTarget) => void;
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
        title={
          outsideOpensCommit ? outsideControlTitle(state.files) : `${outsideWhy(state.files)} Click for what to do.`
        }
        data-work-state="outside"
      >
        <span className={line.vFull}>Changes outside</span>
        <span className={cx(line.vMid, line.vMidKeep)}>Outside</span>
        <span className={styles.count}> · {state.files}</span>
      </button>
    );
  }
  const pushTitle = (p: PushNext) => `Push ${plural(p.commits, "commit")}${p.repos > 1 ? ` in ${p.repos} repos` : ""}`;
  if (state.kind === "push") {
    if (!onPush) return null;
    return (
      <button
        type="button"
        className={styles.work}
        onClick={() => onPush(state.target)}
        title={pushTitle(state)}
        data-work-state="push"
      >
        Push
        <span className={cx(styles.count, line.lblFull)}> ↑{state.commits}</span>
      </button>
    );
  }
  if (state.kind === "land") {
    return (
      <button
        type="button"
        className={styles.work}
        onClick={onLand}
        title={
          `Land ${plural(state.commits, "commit")}` +
          (state.repos > 1 ? ` from ${state.repos} branches` : "") +
          (state.base ? ` into ${state.base}` : "")
        }
        data-work-state="land"
      >
        Land{state.base && <span className={line.vFull}> into {state.base}</span>}
        <span className={cx(styles.count, line.lblFull)}> ↑{state.commits}</span>
      </button>
    );
  }
  // Commits to push beside the files to commit (D11): "Commit… 4" then
  // "Push ↑3". Push's word drops first as the dock narrows, leaving "↑3";
  // its count never does — it's all that says what the button would send.
  const push = state.push;
  const alongside = push && onPush && (
    <button
      type="button"
      className={styles.work}
      onClick={() => onPush(push.target)}
      title={pushTitle(push)}
      aria-label={pushTitle(push)}
      data-work-state="push"
    >
      <span className={line.vFull}>Push </span>
      <span className={styles.count}>↑{push.commits}</span>
    </button>
  );
  const commit = (
    <button
      type="button"
      className={styles.work}
      disabled={committing}
      onClick={onCommit}
      title={
        `Commit ${plural(state.files, "file")}` +
        (state.repos > 1 ? ` in ${state.repos} repos` : "") +
        (state.thenLand > 0 ? `, then land ${plural(state.thenLand, "commit")}` : "")
      }
      data-work-state="commit"
    >
      {committing ? "Committing…" : "Commit…"}
      <span className={cx(styles.count, line.lblFull)}>
        {" "}
        {state.files}
        {state.repos > 1 && ` · ${state.repos} repos`}
      </span>
    </button>
  );
  return alongside ? (
    <>
      {commit}
      {alongside}
    </>
  ) : (
    commit
  );
}
