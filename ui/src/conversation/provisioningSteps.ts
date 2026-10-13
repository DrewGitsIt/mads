// Ordered known-step catalog for the provisioning strip, in the order
// services/session/internal/runner Start emits session.provisioning steps:
// capabilities (skills, MCP servers) are set up before the agent process
// spawns, so mcp_up precedes agent_starting. Each step is emitted when it
// finishes, and a later step implies every earlier one completed, so a
// missed or skipped event never stalls the strip.
export const KNOWN_STEPS = [
  "worktree",
  "hook",
  "mcp_up",
  "agent_starting",
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
