import { useState, type ReactNode } from "react";
import { Popover } from "../components/Popover/Popover";
import type { RouterClient } from "../router/client";
import type { CommitResult, LandResult, PushTarget, Session } from "../router/types";
import { CommitDialog } from "../conversation/Worktree/CommitDialog";
import { LandDialog } from "../conversation/Worktree/LandDialog";
import { PushDialog } from "../conversation/Worktree/PushDialog";
import { useCommitSettings } from "../conversation/Worktree/useCommitSettings";
import { WorkPath } from "../conversation/Worktree/WorkPath";
import { autoResolvedCopy, plural, pushedCopy, uncommittedCopy } from "../conversation/Worktree/worktreeCopy";
import { landRepos, pushFacts, workState, type LandRepo } from "../conversation/Worktree/workState";
import { refreshSessionRepos, useSessionRepos } from "../router/repoStateStore";
import styles from "./WorkChip.module.css";

export interface WorkChipProps {
  session: Session;
  routerClient: RouterClient;
  /** The totals text ("10 files · +229 −55", or "3 of 10 files · …" when filtered). */
  children: ReactNode;
  /** A commit was made from here — refetch what Changes shows. */
  onCommitted: (result: CommitResult) => void;
  /** Say what a Land or Push from here did ("Pushed 3 commits to origin/main"): this tab has no cards. */
  onNotice?: (tone: "ok" | "warn", title: string) => void;
}

/** A Land's outcome in one line, for the Changes tab's toast (the conversation's card says the rest). */
function landNotice(result: LandResult): string {
  const base = result.base ?? "its base";
  if (result.outcome === "landed") return `Landed into ${base}`;
  if (result.outcome === "auto_resolved") return autoResolvedCopy(result.base, result.files ?? []).title;
  return `${base} moved on: ${plural(result.files?.length ?? 0, "file")} conflict — see the conversation`;
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
 * same dialog as the dock, as do Land (its confirmation, D12) and Push (D5).
 * The "changed outside" state opens Commit (D6). What a Land did is a card
 * in the conversation; here, a toast.
 */
export function WorkChip({ session, routerClient, children, onCommitted, onNotice }: WorkChipProps) {
  const commitSettings = useCommitSettings(routerClient);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [pathOpen, setPathOpen] = useState(false);
  // The Land confirmation, open on a branch (undefined root: the first landable).
  const [landFor, setLandFor] = useState<{ root?: string } | null>(null);
  const [pushTarget, setPushTarget] = useState<PushTarget | null>(null);
  // A push from here went through: only then is 0 unpushed "pushed" — the
  // record says 0 for a repo with no remote too.
  const [pushed, setPushed] = useState(false);
  // What a push would send: the window's repo state (COMMIT-LAND-PUSH D10).
  const { repos } = useSessionRepos(routerClient, session.id);
  const push = pushFacts(session, repos);
  const land = landRepos(session, repos);
  const state = workState(session, { push, land });

  function startPush(target: PushTarget) {
    setPathOpen(false);
    setPushTarget(target);
  }

  function commit() {
    setPathOpen(false);
    setDialogOpen(true);
  }

  function startLand(repo?: LandRepo) {
    setPathOpen(false);
    setLandFor({ root: repo?.root });
  }

  let text: string;
  switch (state?.kind) {
    case "commit":
      // Both at once (D11): "4 uncommitted · 3 to push".
      text = uncommittedCopy(state.files, state.repos) + (state.push ? ` · ${state.push.commits} to push` : "");
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
  const alsoPush = state?.kind === "commit" ? state.push : undefined;

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
      {alsoPush && (
        <button
          type="button"
          className={styles.btn}
          title={`Push ${plural(alsoPush.commits, "commit")}`}
          onClick={() => startPush(alsoPush.target)}
        >
          Push
        </button>
      )}
      {state?.kind === "land" && (
        <button
          type="button"
          className={styles.btn}
          title={`Land ${plural(state.commits, "commit")}${state.base ? ` into ${state.base}` : ""}`}
          onClick={() => startLand()}
        >
          Land
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
          onClick={() => startPush(state.target)}
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
            onCommit={commit}
            onLand={startLand}
            land={land}
            onPush={startPush}
            push={push}
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
      <LandDialog
        open={landFor !== null}
        sessionId={session.id}
        routerClient={routerClient}
        session={session}
        fastModel={commitSettings.fastModel}
        initialRoot={landFor?.root}
        onClose={() => setLandFor(null)}
        onLanded={(result) => {
          onNotice?.("ok", landNotice(result));
          refreshSessionRepos(session.id);
        }}
        onResult={(result) => onNotice?.("warn", landNotice(result))}
        onCommitDialog={commit}
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
          onNotice?.("ok", pushedCopy(result));
          // Its repo.changed follows within a second or so; don't wait to drop the count.
          refreshSessionRepos(session.id);
        }}
      />
    </>
  );
}
