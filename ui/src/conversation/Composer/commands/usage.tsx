import { contextWindowForModel } from "../../../router/modelDisplay";
import type { CommandDescriptor } from "./types";
import { UsageCard } from "./UsageCard";

/**
 * /usage (also /status, /context) — the session's health, context, token
 * and plan/spend report in the Ledger card. UI only: nothing is sent to the
 * agent or written to the transcript.
 */
export const usageCommand: CommandDescriptor = {
  name: "usage",
  aliases: ["status", "context"],
  description: "Health, context and token usage",
  execute: (_invocation, api) => {
    const { routerClient, sessionId, session, compaction } = api.ctx;
    const compact = api.compact;
    // Compaction is never emulated: the link needs both the Router's
    // per-report verdict (can_compact) and a host that can run it.
    const onCompact =
      compact && compaction?.compacts !== false ? () => compact() : undefined;
    api.openCard(() => (
      <UsageCard
        routerClient={routerClient}
        sessionId={sessionId}
        fallbackWindow={contextWindowForModel(session?.model)}
        onCompact={onCompact}
      />
    ));
  },
};
