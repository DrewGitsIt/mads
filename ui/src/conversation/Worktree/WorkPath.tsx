import type { ReactNode } from "react";
import type { PushTarget, Session } from "../../router/types";
import { plural, uncommittedCopy } from "./worktreeCopy";
import { isIsolated, uncommittedRepos, unpushedRepos, workState, type WorkState } from "./workState";
import styles from "./WorkPath.module.css";

export interface WorkPathProps {
  session: Session;
  /** Opens the commit flow. Unset: the Commit step has no button. */
  onCommit?: () => void;
  /** Runs Land. Unset: the Land step has no button. */
  onLand?: () => void;
  committing?: boolean;
  landing?: boolean;
  /** Opens the push confirmation for a target. Unset: the Push step has no button. */
  onPush?: (target: PushTarget) => void;
  pushing?: boolean;
  /**
   * A push from here went through, so 0 unpushed means pushed. The record
   * can't tell that from a repo with no remote (0 either way), so without it
   * the step stays quiet rather than claim ✓.
   */
  pushed?: boolean;
}

/** idle: nothing to do that the record can vouch for — no ✓, no number lit. */
type StepState = "done" | "next" | "wait" | "idle";

export function landHeldTitle(state: WorkState): string {
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
 * Where this session's work goes (docs/COMMIT-LAND-PUSH-DESIGN.md D3,
 * mockup commit-land-push.html §3): Commit, then Land for an isolated
 * session, then Push, each step with its state in words and its own button.
 * Read off the session record's counts, like the dock's WorkButton, so the
 * dock's ⎇ popover and the Changes tab's ▾ say the same thing. Before Land,
 * an isolated session's Push step offers its own branch (for a PR); after,
 * the base Land moved.
 */
export function WorkPath({
  session,
  onCommit,
  onLand,
  committing,
  landing,
  onPush,
  pushing,
  pushed,
}: WorkPathProps) {
  const isolated = isIsolated(session);
  const state = workState(session);
  const uncommitted = session.uncommitted_count ?? 0;
  const unlanded = isolated ? (session.unlanded_count ?? 0) : 0;
  const outside = isolated ? (session.outside_count ?? 0) : 0;
  const base = session.worktree_base;

  const commitState: StepState = uncommitted > 0 ? "next" : "done";
  const landState: StepState = unlanded === 0 ? "done" : state?.kind === "land" ? "next" : "wait";
  const unpushed = session.unpushed_count ?? 0;
  const beforeLand = isolated && unlanded > 0;
  const pushState: StepState =
    beforeLand || unpushed > 0 ? (state?.kind === "push" ? "next" : "wait") : pushed ? "done" : "idle";
  const pushTarget: PushTarget = isolated ? "base" : "branch";
  const unpushedIn = unpushedRepos(session);

  let pushDetail: ReactNode;
  if (beforeLand) {
    pushDetail = (
      <>
        before Land: pushes {session.worktree_branch} (for a PR)
        <br />
        after Land: pushes {base}
      </>
    );
  } else if (unpushed > 0) {
    pushDetail = `${unpushed} to push${unpushedIn > 1 ? ` in ${unpushedIn} repos` : ""}${isolated ? ` · pushes ${base}` : ""}`;
  } else {
    pushDetail = pushed ? "pushed" : "nothing to push";
  }
  const pushButton = (target: PushTarget, label: string, primary: boolean) => (
    <button
      type="button"
      className={[styles.btn, primary && styles.btnPrimary].filter(Boolean).join(" ")}
      disabled={pushing}
      onClick={() => onPush?.(target)}
    >
      {pushing ? "Pushing…" : label}
    </button>
  );

  return (
    <div className={styles.path} data-testid="work-path">
      <div className={styles.head}>Where this session's work goes</div>
      <ol className={styles.steps}>
        <Step
          n={1}
          state={commitState}
          title="Commit"
          detail={commitState === "done" ? "all committed" : uncommittedCopy(uncommitted, uncommittedRepos(session))}
          testId="work-step-commit"
          action={
            commitState !== "done" &&
            onCommit && (
              <button
                type="button"
                className={[styles.btn, styles.btnPrimary].join(" ")}
                disabled={committing}
                onClick={onCommit}
              >
                {committing ? "Committing…" : "Commit…"}
              </button>
            )
          }
        />
        {isolated && (
          <Step
            n={2}
            state={landState}
            title={`Land into ${base}`}
            detail={
              landState === "done" ? (
                "nothing to land"
              ) : (
                <>
                  {plural(unlanded, "commit")} on {session.worktree_branch} · merges into {base}
                  {outside > 0 && (
                    <>
                      <br />
                      {plural(outside, "file")} changed outside this session {outside === 1 ? "holds" : "hold"} it back
                    </>
                  )}
                </>
              )
            }
            testId="work-step-land"
            action={
              landState !== "done" &&
              onLand && (
                <button
                  type="button"
                  className={[styles.btn, landState === "next" && styles.btnPrimary].filter(Boolean).join(" ")}
                  disabled={landing || landState !== "next"}
                  title={landState === "next" ? undefined : landHeldTitle(state)}
                  onClick={onLand}
                >
                  {landing ? "Landing…" : "Land"}
                </button>
              )
            }
          />
        )}
        <Step
          n={isolated ? 3 : 2}
          state={pushState}
          title="Push"
          detail={pushDetail}
          testId="work-step-push"
          action={
            onPush &&
            (beforeLand
              ? pushButton("branch", "Push branch", false)
              : unpushed > 0 && pushButton(pushTarget, "Push", pushState === "next"))
          }
        />
      </ol>
    </div>
  );
}

function Step({
  n,
  state,
  title,
  detail,
  action,
  testId,
}: {
  n: number;
  state: StepState;
  title: string;
  detail: ReactNode;
  action?: ReactNode;
  testId: string;
}) {
  return (
    <li className={[styles.step, styles[state]].join(" ")} data-state={state} data-testid={testId}>
      <span className={styles.icon} aria-hidden="true">
        {state === "done" ? "✓" : n}
      </span>
      <div className={styles.text}>
        <div className={styles.title}>{title}</div>
        <div className={styles.detail}>{detail}</div>
      </div>
      {action || <span />}
    </li>
  );
}
