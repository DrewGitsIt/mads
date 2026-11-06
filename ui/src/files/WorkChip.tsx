import { useState, type ReactNode } from "react";
import { Popover } from "../components/Popover/Popover";
import type { RouterClient } from "../router/client";
import type { CommitResult, Session } from "../router/types";
import { CommitDialog } from "../conversation/Worktree/CommitDialog";
import { useCommitSettings } from "../conversation/Worktree/useCommitSettings";
import { WorkPath } from "../conversation/Worktree/WorkPath";
import { plural, uncommittedCopy } from "../conversation/Worktree/worktreeCopy";
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
 * here it only shows that it's running.
 */
export function WorkChip({ session, routerClient, children, onCommitted, onError }: WorkChipProps) {
  const commitSettings = useCommitSettings(routerClient);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [pathOpen, setPathOpen] = useState(false);
  const [landing, setLanding] = useState(false);
  const state = workState(session);

  function commit() {
    setPathOpen(false);
    setDialogOpen(true);
  }

  function land() {
    if (landing) return;
    setLanding(true);
    routerClient
      .landSession(session.id)
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
    default:
      text = "committed";
  }
  const done = state === null;

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
          <WorkPath session={session} landing={landing} onCommit={commit} onLand={land} />
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
    </>
  );
}
