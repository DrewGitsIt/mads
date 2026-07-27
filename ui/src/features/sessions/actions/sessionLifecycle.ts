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

export function deleteState(activity: SessionActivity): LifecycleActionState {
  return activity === "inactive"
    ? { disabled: false }
    : { disabled: true, reason: "Close the session first" };
}
