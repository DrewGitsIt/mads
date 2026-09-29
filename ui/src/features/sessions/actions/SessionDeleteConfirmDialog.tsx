import { Fragment } from "react";
import { ConfirmDialog } from "../../../components/ConfirmDialog/ConfirmDialog";
import {
  EMPTY_AGENT_STATE,
  EMPTY_OPEN_TASK_BREAKDOWN,
  lastReplyKey,
  statusOf,
  totalOpenTasks,
} from "../rail/railGroups";
import type { OpenTaskBreakdown, RailStatus, SessionAgentState } from "../rail/railGroups";
import type { Session } from "../../../router/types";
import {
  formatAbsoluteStamp,
  formatCalendarStamp,
  formatElapsedStamp,
  useElapsedNow,
  useRelativeNow,
} from "../../../utils/relativeTime";
import styles from "./SessionDeleteConfirmDialog.module.css";

export interface SessionDeleteConfirmDialogProps {
  open: boolean;
  /** The session being deleted: every word below is about this record. */
  session: Session;
  /** Live agent state (useSessionAgentState) — the same input the rail's status line takes. */
  agentState?: SessionAgentState;
  /** This session's open work, as the rail already computes it. */
  openTaskBreakdown?: OpenTaskBreakdown;
  onConfirm: () => void;
  onCancel: () => void;
}

/** Which of the three dialogs you get. Additive on top: a non-zero open-task count. */
export type SessionDeleteState = "working" | "open" | "quiet";

/**
 * working › open › quiet, decided by the very status line the rail's card
 * paints rather than by re-deriving it from the record — so a card that says
 * "working" can never open a dialog that says "sleeping".
 */
export function sessionDeleteState(status: RailStatus, session: Session): SessionDeleteState {
  if (status.kind === "working") return "working";
  return session.activity === "active" ? "open" : "quiet";
}

/** The button carries the verb, because the consequences differ and it's the last thing read before the click. */
const CONFIRM_LABEL: Record<SessionDeleteState, string> = {
  working: "Stop and delete",
  open: "Close and delete",
  quiet: "Delete",
};

function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

function headline(state: SessionDeleteState, openTasks: number): string {
  if (state === "working") return "Working right now — this will interrupt it";
  if (state === "open") return "Open · waiting on you";
  // "nothing running" would contradict the task line right below it, so the
  // clause is dropped exactly when that line is there to say otherwise.
  return openTasks > 0 ? "Sleeping" : "Sleeping · nothing running";
}

/** What deleting costs. The count leads when there is one: "12 tasks" is the number that should stop someone's hand, not the word "permanently". */
function lossSentence(state: SessionDeleteState, openTasks: number): string {
  if (openTasks === 0) {
    return state === "working"
      ? "Removes its tasks, file-change history and commit records. The turn in flight is lost."
      : "Removes its tasks, file-change history and commit records.";
  }
  return state === "working"
    ? `Removes ${plural(openTasks, "task")} including the one in flight, plus its file-change history and commit records.`
    : `Removes ${plural(openTasks, "task")}, plus its file-change history and commit records.`;
}

/**
 * Shared delete-confirm copy so the workspace kebab and rail row menu render
 * byte-identical text — for the same session, which is why the session itself
 * is a prop: the dialog used to warn about permanence and nothing else, the
 * same words for a session asleep for three days and one mid-turn with a
 * pipeline running (mockup docs/mockups/session-delete-confirm.html).
 */
export function SessionDeleteConfirmDialog({
  open,
  session,
  agentState = EMPTY_AGENT_STATE,
  openTaskBreakdown = EMPTY_OPEN_TASK_BREAKDOWN,
  onConfirm,
  onCancel,
}: SessionDeleteConfirmDialogProps) {
  const status = statusOf(session, agentState, openTaskBreakdown);
  const state = sessionDeleteState(status, session);
  const openTasks = totalOpenTasks(openTaskBreakdown);
  return (
    <ConfirmDialog
      open={open}
      // The rail deletes sessions that are not the one on screen, so the title
      // has to say which one.
      title={`Delete “${session.name || "Untitled session"}”?`}
      description={
        <DeleteDescription
          state={state}
          status={status}
          session={session}
          openTaskBreakdown={openTaskBreakdown}
          openTasks={openTasks}
        />
      }
      confirmLabel={CONFIRM_LABEL[state]}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}

/**
 * Its own component so its clocks are only subscribed while the dialog is
 * actually on screen — ConfirmDialog renders nothing when closed, so nothing
 * here mounts either.
 */
function DeleteDescription({
  state,
  status,
  session,
  openTaskBreakdown,
  openTasks,
}: {
  state: SessionDeleteState;
  status: RailStatus;
  session: Session;
  openTaskBreakdown: OpenTaskBreakdown;
  openTasks: number;
}) {
  // Open work escalates any of the three states to the warn tone.
  const tone = openTasks > 0 ? "attn" : state === "working" ? "work" : "neutral";
  const buckets = [
    openTaskBreakdown.running > 0
      ? { text: `${openTaskBreakdown.running} running`, className: styles.bucketRunning }
      : undefined,
    openTaskBreakdown.ready > 0
      ? { text: `${openTaskBreakdown.ready} ready`, className: styles.bucketReady }
      : undefined,
    openTaskBreakdown.blocked > 0
      ? { text: `${openTaskBreakdown.blocked} blocked`, className: styles.bucketBlocked }
      : undefined,
  ].filter((b): b is { text: string; className: string } => b !== undefined);
  return (
    <div className={styles.body}>
      <div
        className={[
          styles.statePanel,
          tone === "attn" ? styles.panelAttn : tone === "work" ? styles.panelWork : "",
        ]
          .filter(Boolean)
          .join(" ")}
        data-delete-state={state}
        data-delete-tone={tone}
      >
        <div className={styles.stateHead}>
          {state === "working" ? (
            <span className={styles.spinner} aria-hidden="true" />
          ) : (
            <span
              className={[styles.stateDot, openTasks > 0 ? styles.stateDotAttn : ""]
                .filter(Boolean)
                .join(" ")}
              aria-hidden="true"
            />
          )}
          {headline(state, openTasks)}
        </div>
        {state === "working" && (
          <div className={styles.stateLine}>
            {/* The clause the card's status line is showing for this same turn. */}
            {status.label}
            {status.since && <Elapsed since={status.since} />}
          </div>
        )}
        {/* The case that used to 409 after you confirmed: say it up front instead. */}
        {state === "open" && <div className={styles.stateLine}>Deleting closes it first.</div>}
        {state === "quiet" && <LastActivity session={session} />}
        {openTasks > 0 && (
          <div className={styles.stateLine}>
            {plural(openTasks, "task")}
            {buckets.map((bucket) => (
              <Fragment key={bucket.text}>
                {" · "}
                <span className={bucket.className}>{bucket.text}</span>
              </Fragment>
            ))}
          </div>
        )}
      </div>
      <div className={styles.loss}>{lossSentence(state, openTasks)}</div>
      <div className={styles.keeps}>
        Provider transcripts and the event log survive. This can't be undone.
      </div>
    </div>
  );
}

/** The turn's elapsed counter, on the rail's own 1s clock. */
function Elapsed({ since }: { since: string }) {
  const elapsed = formatElapsedStamp(since, useElapsedNow());
  if (!elapsed) return null;
  return (
    <>
      {" · "}
      <span className={styles.elapsed}>{elapsed}</span>
    </>
  );
}

/** lastReplyKey: the rail's one recency value, so the dialog and the card's stamp read the same instant. */
function LastActivity({ session }: { session: Session }) {
  const stamp = lastReplyKey(session);
  const label = formatCalendarStamp(stamp, useRelativeNow());
  if (!label) return null;
  return (
    <div className={styles.stateLine} title={formatAbsoluteStamp(stamp)}>
      last activity {label}
    </div>
  );
}
