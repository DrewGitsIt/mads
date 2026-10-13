import { useState } from "react";
import type { RouterClient } from "../../../router/client";
import type { Session } from "../../../router/types";
import { isIsolated } from "../../../conversation/Worktree/workState";
import { ConfirmDialog } from "../../../components/ConfirmDialog/ConfirmDialog";
import { SessionCloseLandDialog } from "./SessionCloseLandDialog";

/**
 * Sleep and wake, in one place, for every control that offers them.
 *
 * There are two of those controls — the rail card's ⏸ and the workspace
 * kebab's "Sleep now" — and until this hook existed they were two code
 * paths that happened to end in the same request. Only the rail asked
 * about unlanded commits (D6), so the same word did two different things
 * depending on which menu you picked it from, and the menu that skipped
 * the question was the one you reach for while actually inside the session
 * you have been committing in. Nothing was lost either way (sleep keeps
 * the worktree and its commits), which is exactly why the drift went
 * unnoticed: the only symptom was a question that sometimes wasn't asked.
 *
 * So the decision, the request and the prompt all live here, and a caller
 * owns only the button. Wake is here too — not because it needs a gate
 * (it never asks anything) but because a caller that can sleep a session
 * should not have to reach somewhere else to wake it.
 *
 * So is the rail's ✓, which is Done-and-sleep: checking a session off puts
 * its agent down too, so it asks Sleep's land question — and, before that,
 * whether to stop a turn that is still running, because a check mark that
 * silently killed one would be a nasty surprise and one that silently did
 * nothing would be a confusing one.
 */
export type SleepVerb = "sleep" | "wake" | "mark done" | "bring back";

export interface SessionSleepController {
  /**
   * Put a session to sleep, asking first if an isolated session has commits
   * its base doesn't (D6): land them, or keep them for later. Sleep itself
   * never discards anything, so every other session goes straight through.
   */
  requestSleep: (session: Session) => void;
  /**
   * Pre-warm a sleeping session's agent process. Never prompts: it is not a
   * lifecycle step, just the start-up seconds bought early — see
   * sessionLifecycle.ts's resumeState.
   */
  wake: (session: Session) => void;
  /**
   * The rail's ✓: move a session to Done and put its agent to sleep. `working`
   * is the rail's read of the session mid-turn, which asks first.
   */
  requestDone: (session: Session, working: boolean) => void;
  /** Un-check: back to the top of the active list. Leaves the agent asleep. */
  bringBack: (session: Session) => void;
  /** The mid-turn session whose ✓ is waiting on a confirmation, if any. */
  stopTarget?: Session;
  /** "Mark done & stop": go on to the land question, then Done + sleep. */
  confirmStop: () => void;
  /** Keep it running: neither Done nor sleep. */
  dismissStop: () => void;
  /** The session whose unlanded commits are being asked about, if any. */
  landTarget?: Session;
  /** "Keep for later", or a Land that landed: go through with the sleep. */
  confirmSleep: () => void;
  /** Escape / backdrop: neither land nor sleep. */
  dismissSleep: () => void;
}

export function useSessionSleep(
  routerClient: RouterClient,
  onError: (verb: SleepVerb, session: Session, err: unknown) => void,
): SessionSleepController {
  const [landTarget, setLandTarget] = useState<Session>();
  // Whether the land question was asked on behalf of a ✓ rather than a ⏸:
  // answering it then finishes the check-off, not just the sleep.
  const [landForDone, setLandForDone] = useState(false);
  const [stopTarget, setStopTarget] = useState<Session>();

  function sleepNow(session: Session) {
    setLandTarget(undefined);
    routerClient.closeSession(session.id).catch((err: unknown) => {
      onError("sleep", session, err);
    });
  }

  // Done first, then sleep: the tag is the user's judgement and lands even
  // if the child is slow to stop, and a failed tag write leaves the agent
  // running rather than asleep in a list that still calls it active.
  function doneNow(session: Session) {
    setLandTarget(undefined);
    setLandForDone(false);
    routerClient
      .setSessionAside(session.id, true)
      .then(() => routerClient.closeSession(session.id))
      .catch((err: unknown) => {
        onError("mark done", session, err);
      });
  }

  function needsLandQuestion(session: Session) {
    return isIsolated(session) && (session.unlanded_count ?? 0) > 0;
  }

  function proceedDone(session: Session) {
    if (needsLandQuestion(session)) {
      setLandForDone(true);
      setLandTarget(session);
      return;
    }
    doneNow(session);
  }

  return {
    requestSleep(session) {
      if (needsLandQuestion(session)) {
        setLandForDone(false);
        setLandTarget(session);
        return;
      }
      sleepNow(session);
    },
    requestDone(session, working) {
      if (working) {
        setStopTarget(session);
        return;
      }
      proceedDone(session);
    },
    bringBack(session) {
      routerClient.setSessionAside(session.id, false).catch((err: unknown) => {
        onError("bring back", session, err);
      });
    },
    stopTarget,
    confirmStop() {
      const session = stopTarget;
      setStopTarget(undefined);
      if (session) proceedDone(session);
    },
    dismissStop() {
      setStopTarget(undefined);
    },
    wake(session) {
      routerClient.resumeSession(session.id).catch((err: unknown) => {
        onError("wake", session, err);
      });
    },
    landTarget,
    confirmSleep() {
      if (!landTarget) return;
      if (landForDone) doneNow(landTarget);
      else sleepNow(landTarget);
    },
    dismissSleep() {
      setLandTarget(undefined);
      setLandForDone(false);
    },
  };
}

/**
 * The land-or-keep prompt for whichever control asked. Rendered once by the
 * owner of the controller, so the rail's ⏸ and the kebab's "Sleep now" put
 * up the same dialog rather than each keeping one of their own.
 */
export function SessionSleepPrompt({
  sleep,
  routerClient,
}: {
  sleep: SessionSleepController;
  routerClient: RouterClient;
}) {
  if (sleep.stopTarget) {
    const name = sleep.stopTarget.name || "Untitled session";
    return (
      <ConfirmDialog
        open
        title="Stop this session mid-turn?"
        description={`“${name}” is still working. Marking it done puts its agent to sleep, which stops the turn it is running now. Nothing is lost — it picks up again from your next message.`}
        confirmLabel="Mark done & stop"
        cancelLabel="Keep it running"
        onConfirm={sleep.confirmStop}
        onCancel={sleep.dismissStop}
      />
    );
  }
  if (!sleep.landTarget) return null;
  return (
    <SessionCloseLandDialog
      session={sleep.landTarget}
      routerClient={routerClient}
      onClose={sleep.confirmSleep}
      onDismiss={sleep.dismissSleep}
    />
  );
}
