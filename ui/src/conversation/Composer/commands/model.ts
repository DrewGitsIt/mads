import { applySessionOption } from "./pickOption";
import type { CommandDescriptor } from "./types";

/** /model ‹name› — switches the model within the session's provider. */
export const modelCommand: CommandDescriptor = {
  name: "model",
  description: "Switch this session's model",
  available: ({ provider }) =>
    provider
      ? { ok: true }
      : { ok: false, reason: "This session has no provider yet" },
  options: async ({ routerClient, provider }) =>
    provider
      ? (await routerClient.listModels(provider)).map((m) => ({
          id: m.id,
          label: m.name,
        }))
      : [],
  execute: (invocation, api) =>
    applySessionOption(modelCommand, invocation, api, "model", "Model"),
};
