import { useState } from "react";
import type { RouterClient } from "../../../router/client";
import type { Session } from "../../../router/types";
import { isIsolated } from "../../../conversation/Worktree/workState";
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
 */
export type SleepVerb = "sleep" | "wake";

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

  function sleepNow(session: Session) {
    setLandTarget(undefined);
    routerClient.closeSession(session.id).catch((err: unknown) => {
      onError("sleep", session, err);
    });
  }

  return {
    requestSleep(session) {
      if (isIsolated(session) && (session.unlanded_count ?? 0) > 0) {
        setLandTarget(session);
        return;
      }
      sleepNow(session);
    },
    wake(session) {
      routerClient.resumeSession(session.id).catch((err: unknown) => {
        onError("wake", session, err);
      });
    },
    landTarget,
    confirmSleep() {
      if (landTarget) sleepNow(landTarget);
    },
    dismissSleep() {
      setLandTarget(undefined);
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
