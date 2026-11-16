import type { ReactNode } from "react";
import type { PushTarget, Session } from "../../router/types";
import { plural, uncommittedCopy } from "./worktreeCopy";
import {
  isIsolated,
  landRepos,
  uncommittedRepos,
  workState,
  type LandRepo,
  type PushFacts,
  type WorkState,
} from "./workState";
import styles from "./WorkPath.module.css";

export interface WorkPathProps {
  session: Session;
  /** Opens the commit flow. Unset: the Commit step has no button. */
  onCommit?: () => void;
  /** Opens the Land confirmation for that branch. Unset: the Land steps have no button. */
  onLand?: (repo: LandRepo) => void;
  /** The branches Land would act on (landRepos, with repo state). Unset: the isolated session's own only. */
  land?: LandRepo[];
  committing?: boolean;
  landing?: boolean;
  /** Opens the push confirmation for a target. Unset: the Push step has no button. */
  onPush?: (target: PushTarget) => void;
  /** What a push would send, from the repo state store (pushFacts). Undefined: not known yet. */
  push?: PushFacts;
  pushing?: boolean;
  /**
   * A push from here went through, so 0 to push means pushed. Repo state
   * says 0 for a repo with no remote too, so without it the step stays
   * quiet rather than claim ✓.
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
 * mockup commit-land-push.html §3): Commit, then Land — the isolated
 * session's own branch and each linked worktree with work to land (D12) —
 * then Push, each step with its state in words and its own button.
 * Read off the session record's counts and the repo state's (Push), like the
 * dock's WorkButton, so the dock's ⎇ popover and the Changes tab's ▾ say
 * the same thing. Before Land,
 * an isolated session's Push step offers its own branch (for a PR); after,
 * the base Land moved.
 */
export function WorkPath({
  session,
  onCommit,
  onLand,
  land,
  committing,
  landing,
  onPush,
  push,
  pushing,
  pushed,
}: WorkPathProps) {
  const isolated = isIsolated(session);
  const landList = land ?? landRepos(session, null);
  const state = workState(session, { push, land: landList });
  const uncommitted = session.uncommitted_count ?? 0;
  const unlanded = isolated ? (session.unlanded_count ?? 0) : 0;
  const outside = isolated ? (session.outside_count ?? 0) : 0;
  const base = session.worktree_base;
  // One Land step per branch with work to land (D12): the isolated session's
  // own (always shown, done or not), then each linked worktree an agent made.
  const own = isolated ? landList.find((r) => r.repo === undefined) : undefined;
  const linked = landList.filter((r) => r.repo !== undefined);

  const commitState: StepState = uncommitted > 0 ? "next" : "done";
  const landStepState = (commits: number): StepState =>
    commits === 0 ? "done" : state?.kind === "land" ? "next" : "wait";
  const landState = landStepState(unlanded);
  const unpushed = push?.commits ?? 0;
  const beforeLand = isolated && unlanded > 0;
  const pushState: StepState =
    beforeLand || unpushed > 0
      ? state?.kind === "push"
        ? "next"
        : "wait"
      : pushed && push
        ? "done"
        : "idle";
  const pushTarget: PushTarget = isolated ? "base" : "branch";
  const unpushedIn = push?.repos.length ?? 0;

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
  } else if (!push) {
    // The repo state hasn't answered: say nothing rather than 0, then N.
    pushDetail = "checking…";
  } else {
    pushDetail = pushed ? "pushed" : "nothing to push";
  }
  const landButton = (repo: LandRepo, st: StepState) => (
    <button
      type="button"
      className={[styles.btn, st === "next" && styles.btnPrimary].filter(Boolean).join(" ")}
      disabled={landing || st !== "next"}
      title={st === "next" ? undefined : landHeldTitle(state)}
      onClick={() => onLand?.(repo)}
    >
      {landing ? "Landing…" : "Land"}
    </button>
  );
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
                  {plural(unlanded, "commit")} on {session.worktree_branch} · lands as one commit on {base}
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
              landState !== "done" && onLand && own && landButton(own, landState)
            }
          />
        )}
        {linked.map((r, i) => {
          const st = landStepState(r.commits);
          return (
            <Step
              key={r.root}
              n={(isolated ? 3 : 2) + i}
              state={st}
              title={`Land ${r.branch ?? "this branch"} into ${r.base ?? "its base"}`}
              detail={plural(r.commits, "commit")}
              testId="work-step-land"
              branch={r.branch}
              action={onLand && landButton(r, st)}
            />
          );
        })}
        <Step
          n={(isolated ? 3 : 2) + linked.length}
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
  branch,
}: {
  n: number;
  state: StepState;
  title: string;
  detail: ReactNode;
  action?: ReactNode;
  testId: string;
  /** A linked worktree's Land step: which branch. */
  branch?: string;
}) {
  return (
    <li
      className={[styles.step, styles[state]].join(" ")}
      data-state={state}
      data-testid={testId}
      data-branch={branch}
    >
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
