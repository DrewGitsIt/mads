import type { CommandDescriptor } from "./types";

/**
 * /compact [focus] — runs the harness's own compaction, exactly like the
 * "Compact now" button (progress, errors and the marker all come from that
 * path). Compaction is never emulated: without native support the command is
 * unavailable, and where the harness compacts but takes no focus text, the
 * argument hint goes away and text after /compact is refused.
 */
export const compactCommand: CommandDescriptor = {
  name: "compact",
  description: "Compact the conversation",
  argHint: (ctx) => (ctx.compaction?.takesFocus === false ? undefined : "[focus]"),
  available: (ctx) =>
    ctx.compaction && !ctx.compaction.compacts
      ? { ok: false, reason: `${ctx.provider ?? "This harness"} can't compact` }
      : { ok: true },
  argError: (args, ctx) =>
    args && ctx.compaction?.takesFocus === false
      ? `${ctx.provider ?? "This harness"} can't take compaction instructions — send /compact alone`
      : undefined,
  execute: ({ args }, api) => {
    api.compact?.(args.trim() || undefined);
  },
};
