import { applySessionOption } from "./pickOption";
import type { CommandDescriptor } from "./types";

/** /agent ‹name› — rebinds the session to a registry agent. */
export const agentCommand: CommandDescriptor = {
  name: "agent",
  description: "Switch this session's agent",
  options: async ({ routerClient }) =>
    (await routerClient.listAgents()).map((a) => ({
      id: a.id,
      label: a.name,
      detail: a.description,
    })),
  execute: (invocation, api) =>
    applySessionOption(agentCommand, invocation, api, "agent", "Agent"),
};
