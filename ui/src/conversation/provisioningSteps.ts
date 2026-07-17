// Ordered known-step catalog for the provisioning strip. The spec's 5-step
// model (worktree -> hook -> agent starting -> MCP up -> delivered) doesn't
// exist server-side yet: services/session/internal/runner only ever emits a
// single "provider_ready" session.provisioning event today. A later step
// implies every earlier step completed, so this renders correctly both for
// today's single-event reality and a richer future emission with no UI
// changes.
export const KNOWN_STEPS = [
  "worktree",
  "hook",
  "agent_starting",
  "mcp_up",
  "provider_ready",
] as const;

export type StepId = (typeof KNOWN_STEPS)[number];

export interface StepState {
  id: StepId;
  status: "pending" | "active" | "done";
}

export function reduceSteps(receivedSteps: string[], sessionActive: boolean): StepState[] {
  const latestKnownIndex = receivedSteps.reduce((max, step) => {
    const i = KNOWN_STEPS.indexOf(step as StepId);
    return i > max ? i : max;
  }, -1);
  return KNOWN_STEPS.map((id, i) => ({
    id,
    status:
      sessionActive || i <= latestKnownIndex
        ? "done"
        : i === latestKnownIndex + 1
          ? "active"
          : "pending",
  }));
}
