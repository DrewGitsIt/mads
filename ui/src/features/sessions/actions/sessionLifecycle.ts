import type { SessionActivity } from "../../../router/types";

export interface LifecycleActionState {
  disabled: boolean;
  reason?: string;
}

// Close/terminate is safe against the Supervisor in every activity state —
// TerminateSession kills a tracked child if one exists, or reconciles an
// already-inactive record as a harmless no-op otherwise. The activity
// record is a cache that can drift from the real child state (see
// supervisor.go's ReconcileOrphanedSessions), so it must never be the thing
// that hides the one control that recovers from that drift. Always offer
// it and let the server decide; any real failure surfaces via the caller's
// action-error toast.
export function closeState(): LifecycleActionState {
  return { disabled: false };
}

export function resumeState(activity: SessionActivity): LifecycleActionState {
  return activity === "inactive"
    ? { disabled: false }
    : { disabled: true, reason: "Session is already active" };
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
