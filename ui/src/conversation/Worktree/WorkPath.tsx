import type { ReactNode } from "react";
import type { Session } from "../../router/types";
import { plural, uncommittedCopy } from "./worktreeCopy";
import { isIsolated, uncommittedRepos, workState, type WorkState } from "./workState";
import styles from "./WorkPath.module.css";

export interface WorkPathProps {
  session: Session;
  /** Opens the commit flow. Unset: the Commit step has no button. */
  onCommit?: () => void;
  /** Runs Land. Unset: the Land step has no button. */
  onLand?: () => void;
  committing?: boolean;
  landing?: boolean;
}

type StepState = "done" | "next" | "wait";

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
 * session, each step with its state in words and its own button. Read off
 * the session record's counts, like the dock's WorkButton, so the dock's ⎇
 * popover and the Changes tab's ▾ say the same thing. Push comes later.
 */
export function WorkPath({ session, onCommit, onLand, committing, landing }: WorkPathProps) {
  const isolated = isIsolated(session);
  const state = workState(session);
  const uncommitted = session.uncommitted_count ?? 0;
  const unlanded = isolated ? (session.unlanded_count ?? 0) : 0;
  const outside = isolated ? (session.outside_count ?? 0) : 0;
  const base = session.worktree_base;

  const commitState: StepState = uncommitted > 0 ? "next" : "done";
  const landState: StepState = unlanded === 0 ? "done" : state?.kind === "land" ? "next" : "wait";

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
