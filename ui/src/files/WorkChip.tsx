import { useState, type ReactNode } from "react";
import { Popover } from "../components/Popover/Popover";
import type { RouterClient } from "../router/client";
import type { CommitResult, PushTarget, Session } from "../router/types";
import { CommitDialog } from "../conversation/Worktree/CommitDialog";
import { PushDialog } from "../conversation/Worktree/PushDialog";
import { useCommitSettings } from "../conversation/Worktree/useCommitSettings";
import { WorkPath } from "../conversation/Worktree/WorkPath";
import { plural, pushedCopy, uncommittedCopy } from "../conversation/Worktree/worktreeCopy";
import { workState } from "../conversation/Worktree/workState";
import styles from "./WorkChip.module.css";

export interface WorkChipProps {
  session: Session;
  routerClient: RouterClient;
  /** The totals text ("10 files · +229 −55", or "3 of 10 files · …" when filtered). */
  children: ReactNode;
  /** A commit was made from here — refetch what Changes shows. */
  onCommitted: (result: CommitResult) => void;
  /** Land failed without an outcome (no card is coming). */
  onError: (message: string) => void;
  /** A push went through: say so ("Pushed 3 commits to origin/main"). */
  onPushed?: (message: string) => void;
}

function cx(...names: (string | false | undefined)[]): string {
  return names.filter(Boolean).join(" ");
}

/**
 * The Changes tab's status chip (docs/COMMIT-LAND-PUSH-DESIGN.md D3, mockup
 * commit-land-push.html §4 G3): the totals, the session's state in words,
 * its next step, and ▾ for where the work goes. The state and the next step
 * come from the session record's counts, never from the Compare to choice —
 * switching baselines changes the totals, not "Commit". Commit opens the
 * same dialog as the dock; Land's outcome is a card in the conversation, so
 * here it only shows that it's running — and a Land refused over
 * uncommitted files opens Commit, as does the "changed outside" state (D6).
 * Push opens the same confirmation as the dock (D5).
 */
export function WorkChip({ session, routerClient, children, onCommitted, onError, onPushed }: WorkChipProps) {
  const commitSettings = useCommitSettings(routerClient);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [pathOpen, setPathOpen] = useState(false);
  const [landing, setLanding] = useState(false);
  const [pushTarget, setPushTarget] = useState<PushTarget | null>(null);
  // A push from here went through: only then is 0 unpushed "pushed" — the
  // record says 0 for a repo with no remote too.
  const [pushed, setPushed] = useState(false);
  const state = workState(session);

  function push(target: PushTarget) {
    setPathOpen(false);
    setPushTarget(target);
  }

  function commit() {
    setPathOpen(false);
    setDialogOpen(true);
  }

  function land() {
    if (landing) return;
    setLanding(true);
    routerClient
      .landSession(session.id)
      .then((result) => {
        // Refused over uncommitted files: they're Commit's to take (D6).
        if (result.outcome === "refused" && result.reason === "dirty") commit();
      })
      .catch((err: unknown) => onError(err instanceof Error ? err.message : "Couldn't land."))
      .finally(() => setLanding(false));
  }

  let text: string;
  switch (state?.kind) {
    case "commit":
      text = uncommittedCopy(state.files, state.repos);
      break;
    case "land":
      text = `${plural(state.commits, "commit")} to land`;
      break;
    case "outside":
      text = `${plural(state.files, "file")} changed outside`;
      break;
    case "push":
      text = `committed · ${state.commits} to push${state.repos > 1 ? ` in ${state.repos} repos` : ""}`;
      break;
    default:
      text = pushed ? "committed and pushed" : "committed";
  }
  // Committed: the chip goes quiet, Push or not (mockup G3).
  const done = state === null || state.kind === "push";

  const chip = (
    <span className={cx(styles.chip, done && styles.done)} data-testid="changes-workchip" data-state={state?.kind ?? "done"}>
      <span className={styles.totals} data-testid="changes-totals">
        {children}
      </span>
      <span className={styles.state} data-testid="changes-work-state">
        <span className={cx(styles.dot, done && styles.dotOk)} aria-hidden="true" />
        {text}
      </span>
      {state?.kind === "commit" && (
        <button type="button" className={styles.btn} onClick={commit}>
          Commit
        </button>
      )}
      {state?.kind === "land" && (
        <button
          type="button"
          className={styles.btn}
          disabled={landing}
          title={`Land ${plural(state.commits, "commit")} into ${state.base}`}
          onClick={land}
        >
          {landing ? "Landing…" : "Land"}
        </button>
      )}
      {state?.kind === "outside" && (
        <button
          type="button"
          className={styles.btn}
          title="Commit can take them as changes by someone else, so Land can run"
          onClick={commit}
        >
          Commit…
        </button>
      )}
      {state?.kind === "push" && (
        <button
          type="button"
          className={styles.btn}
          title={`Push ${plural(state.commits, "commit")}`}
          onClick={() => push(state.target)}
        >
          Push
        </button>
      )}
      <button
        type="button"
        className={cx(styles.btn, styles.caret)}
        aria-label="Where this session's work goes"
        aria-haspopup="dialog"
        aria-expanded={pathOpen}
        onClick={() => setPathOpen((o) => !o)}
      >
        ▾
      </button>
    </span>
  );

  return (
    <>
      <Popover open={pathOpen} onClose={() => setPathOpen(false)} anchor={chip} className={styles.pop}>
        <div role="dialog" aria-label="Where this session's work goes">
          <WorkPath
            session={session}
            landing={landing}
            onCommit={commit}
            onLand={land}
            onPush={push}
            pushed={pushed}
          />
        </div>
      </Popover>
      <CommitDialog
        open={dialogOpen}
        sessionId={session.id}
        routerClient={routerClient}
        fastModel={commitSettings.fastModel}
        onClose={() => setDialogOpen(false)}
        onCommitted={onCommitted}
      />
      {/* No "Ask the agent to rebase" here: this tab has no composer to draft into. */}
      <PushDialog
        open={pushTarget !== null}
        sessionId={session.id}
        routerClient={routerClient}
        session={session}
        target={pushTarget ?? "branch"}
        onClose={() => setPushTarget(null)}
        onPushed={(result) => {
          setPushed(true);
          onPushed?.(pushedCopy(result));
        }}
      />
    </>
  );
}
