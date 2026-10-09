import type { SessionActivity } from "../../../router/types";

export interface LifecycleActionState {
  disabled: boolean;
  reason?: string;
}

// Sleep (terminate) is safe against the Supervisor in every activity state —
// TerminateSession kills a tracked child if one exists, or reconciles an
// already-inactive record as a harmless no-op otherwise. The activity
// record is a cache that can drift from the real child state (see
// supervisor.go's ReconcileOrphanedSessions), so it must never be the thing
// that hides the one control that recovers from that drift. Always offer
// it and let the server decide; any real failure surfaces via the caller's
// action-error toast.
export function sleepState(): LifecycleActionState {
  return { disabled: false };
}

// "Wake now" is a pre-warm, not a lifecycle step: a sleeping session wakes on
// its own at the first send, so this only ever buys back the start-up seconds.
// That is why the rail's hover cluster no longer carries it and this menu
// still does — a deliberate warm-up is a thing you can want, but not a thing
// the common path should ask you to think about.
export function wakeState(activity: SessionActivity): LifecycleActionState {
  return activity === "inactive"
    ? { disabled: false }
    : { disabled: true, reason: "Already awake" };
}

// Deleting no longer requires closing first: the router terminates the
// session as part of the delete (handleDeleteSession), and the confirm dialog
// is what says so — its button reads "Close and delete" for a live session and
// "Stop and delete" for one mid-turn. Disabling the item here would pre-empt
// that warning with a dead control, which is the same mistake the 409 made at
// the other end of the click.
export function deleteState(): LifecycleActionState {
  return { disabled: false };
}
