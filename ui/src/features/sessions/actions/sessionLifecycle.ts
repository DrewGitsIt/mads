import type { SessionActivity } from "../../../router/types";

export interface LifecycleActionState {
  disabled: boolean;
  reason?: string;
}

export function closeState(activity: SessionActivity): LifecycleActionState {
  return activity === "active"
    ? { disabled: false }
    : { disabled: true, reason: "Session is already inactive" };
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
